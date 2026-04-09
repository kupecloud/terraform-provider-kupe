package provider

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"

	"github.com/kupecloud/terraform-provider-kupe/internal/client"
)

// AlertmanagerGlobalResource is a singleton-per-tenant resource that owns
// the `global` section of the Alertmanager configuration: SMTP defaults,
// slack_api_url, resolve_timeout, and other top-level fields.
//
// Modelled as a JSON blob for the same reason as receivers — the
// Alertmanager global schema evolves frequently and modelling each field
// as a typed attribute would force a provider release on every upstream
// change. The kupe-api validator catches structural and SSRF issues.
//
// Example HCL:
//
//	resource "kupe_alertmanager_global" "main" {
//	  body_json = jsonencode({
//	    smtp_from         = "alerts@example.com"
//	    smtp_smarthost    = "smtp.example.com:587"
//	    smtp_auth_username = "alerts@example.com"
//	    smtp_auth_password = var.smtp_password
//	    resolve_timeout   = "5m"
//	  })
//	}
//
// Singleton: only one instance per tenant should exist. The kupe-api
// PUT endpoint replaces the section atomically.
type AlertmanagerGlobalResource struct {
	client *client.Client
}

type AlertmanagerGlobalResourceModel struct {
	ID       types.String    `tfsdk:"id"`
	BodyJSON JSONStringValue `tfsdk:"body_json"`
	ETag     types.String    `tfsdk:"etag"`
}

var (
	_ resource.Resource                = &AlertmanagerGlobalResource{}
	_ resource.ResourceWithImportState = &AlertmanagerGlobalResource{}
)

// NewAlertmanagerGlobalResource is the constructor referenced from
// provider.go's Resources() registration.
func NewAlertmanagerGlobalResource() resource.Resource {
	return &AlertmanagerGlobalResource{}
}

func (r *AlertmanagerGlobalResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_alertmanager_global"
}

func (r *AlertmanagerGlobalResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages the `global` section of the Alertmanager configuration for a tenant. " +
			"Singleton — only one of these resources should exist per tenant.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "Always equal to a singleton constant. Singleton-per-tenant resource.",
				Computed:    true,
			},
			"body_json": schema.StringAttribute{
				Description: "Alertmanager global section as a JSON document. Use HCL's `jsonencode()` " +
					"to author. See the upstream Alertmanager docs for the field list.",
				Required:   true,
				CustomType: JSONStringTypeInstance,
			},
			"etag": schema.StringAttribute{
				Description: "Wrapper ETag from the most recent read or write. Used for optimistic locking.",
				Computed:    true,
			},
		},
	}
}

func (r *AlertmanagerGlobalResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError("unexpected provider data type", fmt.Sprintf("expected *client.Client, got %T", req.ProviderData))
		return
	}
	r.client = c
}

func parseGlobal(raw string) (client.AlertmanagerGlobal, error) {
	if raw == "" {
		return client.AlertmanagerGlobal{}, nil
	}
	body := map[string]any{}
	if err := json.Unmarshal([]byte(raw), &body); err != nil {
		return nil, fmt.Errorf("invalid body_json: %w", err)
	}
	return client.AlertmanagerGlobal(body), nil
}

func renderGlobal(g client.AlertmanagerGlobal) (string, error) {
	if len(g) == 0 {
		return "{}", nil
	}
	out, err := json.Marshal(g)
	if err != nil {
		return "", fmt.Errorf("rendering global: %w", err)
	}
	return string(out), nil
}

func (r *AlertmanagerGlobalResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan AlertmanagerGlobalResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	body, err := parseGlobal(plan.BodyJSON.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("invalid global body", err.Error())
		return
	}
	out, etag, err := r.client.PutAlertmanagerGlobal(ctx, "", body)
	if err != nil {
		resp.Diagnostics.AddError("failed to create alertmanager global", err.Error())
		return
	}
	if err := mapGlobalToState(out, etag, &plan); err != nil {
		resp.Diagnostics.AddError("failed to render global state", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *AlertmanagerGlobalResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state AlertmanagerGlobalResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, etag, err := r.client.GetAlertmanagerGlobal(ctx)
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("failed to read alertmanager global", err.Error())
		return
	}
	if err := mapGlobalToState(out, etag, &state); err != nil {
		resp.Diagnostics.AddError("failed to render global state", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *AlertmanagerGlobalResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state AlertmanagerGlobalResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	body, err := parseGlobal(plan.BodyJSON.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("invalid global body", err.Error())
		return
	}
	out, etag, err := r.client.PutAlertmanagerGlobal(ctx, state.ETag.ValueString(), body)
	if err != nil {
		resp.Diagnostics.AddError("failed to update alertmanager global", err.Error())
		return
	}
	if err := mapGlobalToState(out, etag, &plan); err != nil {
		resp.Diagnostics.AddError("failed to render global state", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete clears the global section by replacing it with an empty object.
// As with the routes resource, we explicitly clear rather than leak state.
func (r *AlertmanagerGlobalResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state AlertmanagerGlobalResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if _, _, err := r.client.PutAlertmanagerGlobal(ctx, state.ETag.ValueString(), client.AlertmanagerGlobal{}); err != nil && !client.IsNotFound(err) {
		resp.Diagnostics.AddError("failed to clear alertmanager global", err.Error())
	}
}

func (r *AlertmanagerGlobalResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

func mapGlobalToState(g client.AlertmanagerGlobal, etag string, state *AlertmanagerGlobalResourceModel) error {
	state.ID = types.StringValue("alertmanager-global")
	state.ETag = types.StringValue(etag)
	body, err := renderGlobal(g)
	if err != nil {
		return err
	}
	state.BodyJSON = JSONStringValue{StringValue: basetypes.NewStringValue(body)}
	return nil
}
