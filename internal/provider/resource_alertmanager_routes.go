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

// AlertmanagerRoutesResource is a singleton-per-tenant resource that owns
// the entire child route list of the root Alertmanager route.
//
// # Why singleton, not per-route
//
// Alertmanager routes are positional — order determines evaluation, and
// the underlying Mimir AM API does not give individual routes stable IDs.
// Modelling each route as its own terraform resource and keying by index
// would mean inserting or reordering shifts every subsequent index, which
// produces large false-positive diffs every time the user edits the list.
// A singleton resource that owns the whole list lets terraform diff the
// list elements semantically and produce minimal, accurate plans.
//
// The singleton owns only the child routes — the root route itself
// (receiver, group_by defaults) is implicit and managed by the kupe-api
// handler. The kupe-api `PUT /alertmanager/routes` endpoint replaces the
// entire child list atomically.
//
// # Schema
//
// One field: routes_json — a JSON-encoded array of route objects. Use
// HCL's jsonencode() to author. Each route object follows the
// Alertmanager Route schema (receiver, matchers, group_by, group_wait,
// group_interval, repeat_interval, routes for nested children).
//
// Example HCL:
//
//	resource "kupe_alertmanager_routes" "main" {
//	  routes_json = jsonencode([
//	    {
//	      matchers       = ["severity=\"critical\""]
//	      receiver       = "pagerduty"
//	      group_wait     = "10s"
//	      repeat_interval = "1h"
//	    },
//	    {
//	      matchers = ["team=\"infra\""]
//	      receiver = "slack"
//	    },
//	  ])
//	}
type AlertmanagerRoutesResource struct {
	client *client.Client
}

type AlertmanagerRoutesResourceModel struct {
	ID         types.String    `tfsdk:"id"`
	RoutesJSON JSONStringValue `tfsdk:"routes_json"`
	ETag       types.String    `tfsdk:"etag"`
}

var (
	_ resource.Resource                = &AlertmanagerRoutesResource{}
	_ resource.ResourceWithImportState = &AlertmanagerRoutesResource{}
)

// NewAlertmanagerRoutesResource is the constructor referenced from
// provider.go's Resources() registration.
func NewAlertmanagerRoutesResource() resource.Resource {
	return &AlertmanagerRoutesResource{}
}

func (r *AlertmanagerRoutesResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_alertmanager_routes"
}

func (r *AlertmanagerRoutesResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages the entire ordered list of child routes on the root Alertmanager route " +
			"for a tenant. Singleton — only one of these resources should exist per tenant. The root " +
			"route itself (receiver, group_by defaults) is implicit and managed by kupe-api.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "Always equal to the tenant name. Singleton-per-tenant resource.",
				Computed:    true,
			},
			"routes_json": schema.StringAttribute{
				Description: "Ordered list of routes as a JSON array. Use `jsonencode([...])` to " +
					"author. Each element follows the Alertmanager Route schema (receiver, matchers, " +
					"group_by, group_wait, group_interval, repeat_interval, nested routes).",
				Required:   true,
				CustomType: JSONStringTypeInstance,
			},
			"etag": schema.StringAttribute{
				Description: "Wrapper ETag from the most recent read or write. Used for optimistic " +
					"locking on subsequent updates.",
				Computed: true,
			},
		},
	}
}

func (r *AlertmanagerRoutesResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// parseRoutes decodes the routes_json string into a slice of AlertmanagerRoute.
// JSON unmarshalling into typed structs sorts unknown fields out, so any
// extras the user adds will be silently dropped — the kupe-api validator
// rejects malformed routes before they reach Mimir.
func parseRoutes(raw string) ([]*client.AlertmanagerRoute, error) {
	if raw == "" {
		return nil, nil
	}
	var out []*client.AlertmanagerRoute
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, fmt.Errorf("invalid routes_json: %w", err)
	}
	return out, nil
}

// renderRoutes serialises the typed slice back to JSON. encoding/json
// sorts struct fields by declaration order, which produces stable output
// across reads.
func renderRoutes(routes []*client.AlertmanagerRoute) (string, error) {
	if len(routes) == 0 {
		return "[]", nil
	}
	out, err := json.Marshal(routes)
	if err != nil {
		return "", fmt.Errorf("rendering routes: %w", err)
	}
	return string(out), nil
}

func (r *AlertmanagerRoutesResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan AlertmanagerRoutesResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	routes, err := parseRoutes(plan.RoutesJSON.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("invalid routes", err.Error())
		return
	}
	out, etag, err := r.client.PutAlertmanagerRoutes(ctx, "", routes)
	if err != nil {
		resp.Diagnostics.AddError("failed to create alertmanager routes", err.Error())
		return
	}
	if err := mapRoutesToState(out, etag, &plan); err != nil {
		resp.Diagnostics.AddError("failed to render routes state", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *AlertmanagerRoutesResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state AlertmanagerRoutesResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, etag, err := r.client.GetAlertmanagerRoutes(ctx)
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("failed to read alertmanager routes", err.Error())
		return
	}
	if err := mapRoutesToState(out, etag, &state); err != nil {
		resp.Diagnostics.AddError("failed to render routes state", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *AlertmanagerRoutesResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state AlertmanagerRoutesResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	routes, err := parseRoutes(plan.RoutesJSON.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("invalid routes", err.Error())
		return
	}
	out, etag, err := r.client.PutAlertmanagerRoutes(ctx, state.ETag.ValueString(), routes)
	if err != nil {
		resp.Diagnostics.AddError("failed to update alertmanager routes", err.Error())
		return
	}
	if err := mapRoutesToState(out, etag, &plan); err != nil {
		resp.Diagnostics.AddError("failed to render routes state", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete clears the route list. We do not support a "leave routes alone"
// path on destroy because that would silently leak routes when the
// terraform resource is removed; replacing the list with an empty array
// makes the destruction observable.
func (r *AlertmanagerRoutesResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state AlertmanagerRoutesResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if _, _, err := r.client.PutAlertmanagerRoutes(ctx, state.ETag.ValueString(), nil); err != nil && !client.IsNotFound(err) {
		resp.Diagnostics.AddError("failed to clear alertmanager routes", err.Error())
	}
}

func (r *AlertmanagerRoutesResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

func mapRoutesToState(routes []*client.AlertmanagerRoute, etag string, state *AlertmanagerRoutesResourceModel) error {
	state.ID = types.StringValue("alertmanager-routes")
	state.ETag = types.StringValue(etag)
	body, err := renderRoutes(routes)
	if err != nil {
		return err
	}
	state.RoutesJSON = JSONStringValue{StringValue: basetypes.NewStringValue(body)}
	return nil
}
