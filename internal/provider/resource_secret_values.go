package provider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/kupecloud/terraform-provider-kupe/internal/client"
)

type secretSourceVersionModifier struct{}

func (secretSourceVersionModifier) Description(context.Context) string {
	return "Keeps the observed source version unless the write-only change counter changes."
}

func (m secretSourceVersionModifier) MarkdownDescription(ctx context.Context) string {
	return m.Description(ctx)
}

func (secretSourceVersionModifier) PlanModifyInt64(ctx context.Context, req planmodifier.Int64Request, resp *planmodifier.Int64Response) {
	if req.State.Raw.IsNull() || req.Plan.Raw.IsNull() {
		return
	}
	var planned, prior types.Int64
	resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, path.Root("values_wo_version"), &planned)...)
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("values_wo_version"), &prior)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if planned.Equal(prior) {
		resp.PlanValue = req.StateValue
	} else {
		resp.PlanValue = types.Int64Unknown()
	}
}

// Read uses the shared definition lifecycle, then only the entry metadata.
func (r *SecretResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	r.managedResource.Read(ctx, req, resp)
	if resp.Diagnostics.HasError() || resp.State.Raw.IsNull() {
		return
	}
	var state SecretResourceModel
	resp.Diagnostics.Append(resp.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	r.readSourceVersion(ctx, &state, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *SecretResource) readSourceVersion(ctx context.Context, m *SecretResourceModel, diags *diag.Diagnostics) {
	entry, err := r.client.GetVaultEntry(ctx, m.SecretPath.ValueString())
	if err == nil {
		m.SourceVersion = types.Int64Value(entry.Version)
		return
	}
	if client.IsNotFound(err) {
		m.SourceVersion = types.Int64Null()
		return
	}
	var apiErr *client.APIError
	if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusServiceUnavailable {
		diags.AddWarning("secret source version unavailable", "The stored entry metadata is temporarily unavailable. The previous source_version is kept; run plan again before changing values.")
		return
	}
	// The response to a value endpoint must never echo values into a diagnostic,
	// even if a faulty upstream sends them in its error body.
	diags.AddError("failed to read secret source version", vaultFailureDetail(err))
}

func vaultFailureDetail(err error) string {
	switch client.ErrorCode(err) {
	case client.CodeVaultNotConfigured:
		return "Secret storage is not configured in this Kupe Cloud environment. State is kept. Contact Kupe Cloud support."
	case client.CodeSecretPathReserved:
		return "This secret path is reserved by the platform. Choose another secret_path."
	case client.CodeVaultUnavailable:
		return "Secret storage is temporarily unavailable. Run plan again and retry later."
	default:
		return "The secret storage request could not be completed. Check access to this entry and retry; contact Kupe Cloud support if it persists."
	}
}

func (r *SecretResource) writeValues(ctx context.Context, config tfsdk.Config, plan, prior *SecretResourceModel, create bool, diags *diag.Diagnostics) *client.VaultWrite {
	var configured types.Map
	diags.Append(config.GetAttribute(ctx, path.Root("values_wo"), &configured)...)
	if diags.HasError() {
		return nil
	}
	if configured.IsNull() || configured.IsUnknown() || len(configured.Elements()) == 0 {
		diags.AddError("secret values unavailable", "values_wo must be a known, non-empty map when values_wo_version changes.")
		return nil
	}
	values := make(map[string]string, len(configured.Elements()))
	for key, value := range configured.Elements() {
		str, ok := value.(types.String)
		if !ok || str.IsNull() || str.IsUnknown() {
			diags.AddError("secret values unavailable", "Every values_wo value must be a known, non-null string at apply.")
			return nil
		}
		values[key] = str.ValueString()
	}
	var ifMatch *int64
	if prior != nil && prior.SourceVersion.IsUnknown() {
		diags.AddError("secret source version unavailable", "The planned source_version is unknown. Run plan again before writing values.")
		return nil
	}
	if prior != nil && !prior.SourceVersion.IsNull() && !prior.SourceVersion.IsUnknown() {
		version := prior.SourceVersion.ValueInt64()
		ifMatch = &version
	}
	written, err := r.client.PutVaultValues(ctx, plan.SecretPath.ValueString(), values, ifMatch)
	if err == nil {
		return written
	}
	if client.IsPreconditionFailed(err) {
		if create {
			diags.AddError("secret entry already exists", fmt.Sprintf("a secret entry already exists at %s. To manage an existing entry, apply this resource once without values_wo, then add values_wo and values_wo_version", plan.SecretPath.ValueString()))
		} else {
			version := "absent"
			if ifMatch != nil {
				version = fmt.Sprintf("%d", *ifMatch)
			}
			diags.AddError("secret values changed since the plan", fmt.Sprintf("the values at %s changed since the plan (planned against version %s); run plan again to review", plan.SecretPath.ValueString(), version))
		}
		return nil
	}
	var apiErr *client.APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode >= 500 || apiErr.Code == client.CodeSecretWriteUnconfirmed {
		diags.AddError("secret write not confirmed", "the write may or may not have been saved. The previous values_wo_version is kept. Run plan again to review the current source version, then apply to send the configured values again.")
		return nil
	}
	diags.AddError("failed to write secret values", vaultFailureDetail(err))
	return nil
}

func (r *SecretResource) deliveryToken(ctx context.Context, name string, written *client.VaultWrite) (string, error) {
	if slices.Contains(written.RefreshRequested, name) {
		secret, _, err := r.client.GetSecret(ctx, name)
		if err != nil {
			return "", err
		}
		if secret.Status != nil && secret.Status.Refresh != nil && secret.Status.Refresh.RequestedAt != "" {
			return secret.Status.Refresh.RequestedAt, nil
		}
	}
	return r.client.RefreshSecret(ctx, name)
}

// savedValuesNext is what a delivery timeout after a confirmed write leaves
// in state and what to do next.
const savedValuesNext = "The confirmed values_wo_version and source_version are recorded in state. Running apply again does not send the values again. Check delivery in the Kupe Console. Set a longer timeout to wait longer for future changes."

func savedValuesDetail(version int64) string {
	return fmt.Sprintf("values saved as version %d; delivery is still in progress. ", version) + savedValuesNext
}

// savedValuesWarning is the detail under the "secret delivery still in
// progress" summary, so it does not say that again.
func savedValuesWarning(version int64) string {
	return fmt.Sprintf("Values saved as version %d. ", version) + savedValuesNext
}

// Copy only the lifecycle descriptor for this operation: the token is local
// to the wait and never becomes an attribute, shared mutable state or a log.
func (r *SecretResource) valuesLifecycle(version types.Int64, token string) managedResource[SecretResourceModel, client.Secret] {
	lifecycle := r.managedResource
	if version.IsNull() || version.IsUnknown() {
		return lifecycle
	}
	kind := *r.kind
	kind.ready.waitOnRead = true
	kind.ready.createTimeoutWarning = func(*SecretResourceModel, *client.Secret) string {
		return savedValuesWarning(version.ValueInt64())
	}
	kind.ready.converged = func(s *client.Secret) bool {
		if !secretDelivered(s) {
			return false
		}
		if token == "" {
			return true
		} // new definitions wait for their first delivery
		if s.Status.Refresh == nil {
			return false
		}
		if s.Status.Refresh.RequestedAt != "" && s.Status.Refresh.RequestedAt != token {
			token = s.Status.Refresh.RequestedAt
		}
		if s.Status.Refresh.ObservedAt != token {
			return false
		}
		for _, status := range s.Status.SyncStatuses {
			if status.Phase != "Withdrawing" && (status.Phase != "Synced" || status.DeliveredRefresh != token) {
				return false
			}
		}
		return true
	}
	kind.ready.timedOutDetail = func(*SecretResourceModel, *client.Secret) string {
		return " " + savedValuesDetail(version.ValueInt64())
	}
	lifecycle.kind = &kind
	return lifecycle
}
