package provider

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"

	"github.com/kupecloud/terraform-provider-kupe/internal/client"
)

// AlertmanagerReceiverResource manages a single named receiver in a
// tenant's Mimir Alertmanager configuration.
//
// # Schema
//
// Receivers are modelled with two fields:
//
//   - name      — required, immutable, the receiver identifier referenced
//     by routes.
//   - body_json — required, an Alertmanager receiver body as a JSON
//     document (e.g. slack_configs / email_configs / webhook_configs).
//     Users author the body via HCL's jsonencode() function for readability.
//
// # Why JSON, not nested HCL blocks
//
// Alertmanager defines dozens of receiver sub-block types and adds new
// ones every release. Modelling each as a typed nested attribute would
// trap users on whatever subset the provider knows about, force a
// provider upgrade for every Alertmanager release, and produce poor
// error messages when fields evolve. Passing the receiver as a JSON
// string keeps the provider permanently forward-compatible at the cost of
// schema-level type checks. The kupe-api validator catches structural
// errors and SSRF attempts before the config reaches Mimir, so the loss
// of provider-side type-checking is mostly cosmetic.
//
// Example HCL:
//
//	resource "kupe_alertmanager_receiver" "slack" {
//	  name      = "slack"
//	  body_json = jsonencode({
//	    slack_configs = [{
//	      api_url       = var.slack_webhook_url
//	      channel       = "#alerts"
//	      send_resolved = true
//	    }]
//	  })
//	}
//
// # Drift detection
//
// State stores the canonical JSON round-tripped through encoding/json.
// After a successful Read, the stored body_json key order is sorted
// alphabetically by encoding/json's deterministic marshaller, so plans
// remain stable across reads.
type AlertmanagerReceiverResource struct {
	client *client.Client
}

type AlertmanagerReceiverResourceModel struct {
	Name     types.String    `tfsdk:"name"`
	BodyJSON JSONStringValue `tfsdk:"body_json"`
	ETag     types.String    `tfsdk:"etag"`
}

var (
	_ resource.Resource                = &AlertmanagerReceiverResource{}
	_ resource.ResourceWithImportState = &AlertmanagerReceiverResource{}
)

// NewAlertmanagerReceiverResource is the constructor referenced from
// provider.go's Resources() registration.
func NewAlertmanagerReceiverResource() resource.Resource {
	return &AlertmanagerReceiverResource{}
}

func (r *AlertmanagerReceiverResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_alertmanager_receiver"
}

func (r *AlertmanagerReceiverResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a single Alertmanager receiver in the tenant's Mimir Alertmanager configuration. " +
			"Receivers are referenced by routes via their name. The body is provided as a JSON document via " +
			"`jsonencode()` so any receiver type supported by Alertmanager (slack_configs, email_configs, " +
			"webhook_configs, pagerduty_configs, msteams_configs, etc.) can be used without a provider upgrade.",
		Attributes: map[string]schema.Attribute{
			"name": schema.StringAttribute{
				Description: "Receiver name. Immutable after creation; rename requires destroy + create.",
				Required:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"body_json": schema.StringAttribute{
				Description: "Alertmanager receiver body as a JSON document. Use HCL's `jsonencode()` " +
					"to author the value. The `name` field is set automatically from the resource " +
					"`name` attribute and any value embedded in the body is overridden.",
				Required:   true,
				CustomType: JSONStringTypeInstance,
			},
			"etag": schema.StringAttribute{
				Description: "Wrapper ETag from the most recent read or write. Used for optimistic locking " +
					"on subsequent updates to detect concurrent edits from the Console UI or other API clients.",
				Computed: true,
			},
		},
	}
}

func (r *AlertmanagerReceiverResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// parseReceiverBody parses the user-supplied body_json into a generic map
// and overrides the `name` field with the resource `name` attribute. The
// kupe-api handler does the same override server-side; doing it here too
// catches user mistakes earlier and produces stable diffs.
func parseReceiverBody(name, raw string) (client.AlertmanagerReceiver, error) {
	if raw == "" {
		return client.AlertmanagerReceiver{"name": name}, nil
	}
	body := map[string]any{}
	if err := json.Unmarshal([]byte(raw), &body); err != nil {
		return nil, fmt.Errorf("invalid body_json: %w", err)
	}
	body["name"] = name
	return client.AlertmanagerReceiver(body), nil
}

// renderReceiverBody serialises a receiver back to JSON, stripping the
// `name` field so the user-visible body_json stays focused on the
// receiver's actual configuration. encoding/json sorts map keys
// alphabetically so successive Reads produce identical output and
// terraform plans stay clean.
func renderReceiverBody(recv client.AlertmanagerReceiver) (string, error) {
	body := map[string]any{}
	for k, v := range recv {
		if k == "name" {
			continue
		}
		body[k] = v
	}
	if len(body) == 0 {
		return "{}", nil
	}
	out, err := json.Marshal(body)
	if err != nil {
		return "", fmt.Errorf("rendering receiver body: %w", err)
	}
	return string(out), nil
}

func (r *AlertmanagerReceiverResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan AlertmanagerReceiverResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	body, err := parseReceiverBody(plan.Name.ValueString(), plan.BodyJSON.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("invalid receiver body", err.Error())
		return
	}
	out, etag, err := r.client.PutAlertmanagerReceiver(ctx, plan.Name.ValueString(), "", body)
	if err != nil {
		resp.Diagnostics.AddError("failed to create alertmanager receiver", err.Error())
		return
	}
	if err := mapReceiverToState(plan.Name.ValueString(), out, etag, &plan); err != nil {
		resp.Diagnostics.AddError("failed to render receiver state", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *AlertmanagerReceiverResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state AlertmanagerReceiverResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, etag, err := r.client.GetAlertmanagerReceiver(ctx, state.Name.ValueString())
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("failed to read alertmanager receiver", err.Error())
		return
	}
	if err := mapReceiverToState(state.Name.ValueString(), out, etag, &state); err != nil {
		resp.Diagnostics.AddError("failed to render receiver state", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *AlertmanagerReceiverResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state AlertmanagerReceiverResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	body, err := parseReceiverBody(plan.Name.ValueString(), plan.BodyJSON.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("invalid receiver body", err.Error())
		return
	}
	out, etag, err := r.client.PutAlertmanagerReceiver(ctx, plan.Name.ValueString(), state.ETag.ValueString(), body)
	if err != nil {
		resp.Diagnostics.AddError("failed to update alertmanager receiver", err.Error())
		return
	}
	if err := mapReceiverToState(plan.Name.ValueString(), out, etag, &plan); err != nil {
		resp.Diagnostics.AddError("failed to render receiver state", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *AlertmanagerReceiverResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state AlertmanagerReceiverResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.DeleteAlertmanagerReceiver(ctx, state.Name.ValueString()); err != nil && !client.IsNotFound(err) {
		resp.Diagnostics.AddError("failed to delete alertmanager receiver", err.Error())
	}
}

func (r *AlertmanagerReceiverResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("name"), req, resp)
}

func mapReceiverToState(name string, recv client.AlertmanagerReceiver, etag string, state *AlertmanagerReceiverResourceModel) error {
	state.Name = types.StringValue(name)
	state.ETag = types.StringValue(etag)
	body, err := renderReceiverBody(recv)
	if err != nil {
		return err
	}
	state.BodyJSON = JSONStringValue{StringValue: basetypes.NewStringValue(body)}
	return nil
}
