package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/kupecloud/terraform-provider-kupe/internal/client"
)

var (
	_ resource.Resource                = &AppResource{}
	_ resource.ResourceWithConfigure   = &AppResource{}
	_ resource.ResourceWithImportState = &AppResource{}
)

// AppResource manages the identity and protection independently of deployments.
type AppResource struct {
	managedResource[AppResourceModel, client.App]
}

type AppResourceModel struct {
	Name               types.String   `tfsdk:"name"`
	DeletionProtection types.Bool     `tfsdk:"deletion_protection"`
	Phase              types.String   `tfsdk:"phase"`
	ETag               types.String   `tfsdk:"etag"`
	CreatedAt          types.String   `tfsdk:"created_at"`
	Timeouts           timeouts.Value `tfsdk:"timeouts"`
}

func NewAppResource() resource.Resource {
	return &AppResource{managedResource[AppResourceModel, client.App]{kind: &appKind}}
}

var appKind = serviceKind[AppResourceModel, client.App]{
	noun: "app",
	name: func(m *AppResourceModel) string { return m.Name.ValueString() },
	get: func(ctx context.Context, c *client.Client, m *AppResourceModel) (*client.App, string, error) {
		return c.GetApp(ctx, m.Name.ValueString())
	},
	del: func(ctx context.Context, c *client.Client, m *AppResourceModel) error {
		return c.DeleteApp(ctx, m.Name.ValueString(), m.ETag.ValueString())
	},
	mapToState: mapAppToState,
	timeoutsOf: func(m *AppResourceModel) timeouts.Value { return m.Timeouts },
	defaults:   operationTimeouts{create: 30 * time.Minute, update: 20 * time.Minute, delete: 20 * time.Minute},
	ready: readiness[AppResourceModel, client.App]{
		target: "the app's observed generation",
		phase:  appPhase,
		converged: func(app *client.App) bool {
			return app != nil && app.Status != nil && app.Status.Phase != phaseTerminating &&
				app.Status.ObservedGeneration >= app.Generation
		},
		failed: func(app *client.App) (time.Duration, bool) { return 0, appPhase(app) == phaseTerminating },
	},
	settingsChanged: func(prior *AppResourceModel, current *client.App) []string {
		if prior.DeletionProtection.ValueBool() != current.DeletionProtection {
			return []string{"deletion_protection"}
		}
		return nil
	},
	deleteRefused: func(m *AppResourceModel, err error) (string, string, bool) {
		if !client.IsDeletionProtected(err) {
			return "", "", false
		}
		return "app has deletion protection enabled", fmt.Sprintf("App %q was not deleted. Set `deletion_protection = false` on the app and its environments, run `terraform apply`, then destroy them. Terraform deletes environment resources before the app; an app with any remaining environment is kept.", m.Name.ValueString()), true
	},
	stillDeleting: func(m *AppResourceModel) string {
		return fmt.Sprintf("DELETE was accepted but app %q is still terminating. Terraform has removed it from state; cleanup continues in the background and its name stays reserved until it is gone. Check the app in the Kupe Console before creating it again.", m.Name.ValueString())
	},
}

func (r *AppResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_app"
}

func (r *AppResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a Kupe app's identity and deletion protection. Deploy each environment with `kupe_app_environment`. Create and update wait for the app's observed generation, independently of its environments. To destroy, turn protection off and apply first, then destroy the environments before the app. The cluster is kept.",
		Attributes: map[string]schema.Attribute{
			"name":                schema.StringAttribute{Required: true, Description: "App name: an RFC 1123 label of at most 32 characters without `--`.", Validators: appNameValidators(32), PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}},
			"deletion_protection": schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(true), Description: "Protects the app from deletion. Set to false and apply before destroying it.", PlanModifiers: []planmodifier.Bool{boolplanmodifier.UseStateForUnknown()}},
			"phase":               schema.StringAttribute{Computed: true, Description: "Current app phase."},
			"etag":                etagAttribute(),
			"created_at":          createdAtAttribute("the app"),
			"timeouts":            timeouts.Attributes(ctx, timeouts.Opts{Create: true, Update: true, Delete: true}),
		},
	}
}

func (r *AppResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state AppResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	app, etag, err := r.client.GetApp(ctx, state.Name.ValueString())
	if client.ErrorCode(err) == client.CodeAppsUnavailable {
		resp.Diagnostics.AddWarning("apps unavailable", apiErrorDetail(err))
		return
	}
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("failed to read app", apiErrorDetail(err))
		return
	}
	mapAppToState(app, etag, &state)
	if resp.Private != nil {
		resp.Diagnostics.Append(resp.Private.SetKey(ctx, "app_uid", []byte(strconv.Quote(app.UID)))...)
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *AppResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan AppResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	protected := plan.DeletionProtection.ValueBool()
	app, etag, err := r.client.CreateApp(ctx, client.CreateAppRequest{Name: plan.Name.ValueString(), DeletionProtection: &protected})
	if err != nil {
		resp.Diagnostics.AddError("failed to create app", apiErrorDetail(err))
		return
	}
	if resp.Private != nil {
		resp.Diagnostics.Append(resp.Private.SetKey(ctx, "app_uid", []byte(strconv.Quote(app.UID)))...)
	}
	mapAppToState(app, etag, &plan)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	timeout, diags := plan.Timeouts.Create(ctx, appKind.defaults.create)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	r.waitAppObserved(ctx, timeout, "create", app, &plan, &resp.State, &resp.Diagnostics)
}

func (r *AppResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state AppResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	uidData, uidDiags := req.Private.GetKey(ctx, "app_uid")
	resp.Diagnostics.Append(uidDiags...)
	var uid string
	if err := json.Unmarshal(uidData, &uid); err != nil || uid == "" {
		resp.Diagnostics.AddError("failed to update app", "Refresh the app before applying again.")
		return
	}
	if resp.Diagnostics.HasError() {
		return
	}
	// A request-scoped descriptor keeps UID checks local to this operation;
	// other managed resources retain their existing lifecycle policy.
	guarded := r.managedResource
	kind := *r.kind
	guarded.kind = &kind
	kind.get = func(ctx context.Context, c *client.Client, m *AppResourceModel) (*client.App, string, error) {
		app, etag, err := c.GetApp(ctx, m.Name.ValueString())
		if err == nil && app.UID != uid {
			return nil, "", errAppIdentityChanged
		}
		return app, etag, err
	}
	var accepted *client.App
	var send patchFunc[client.App]
	if !plan.DeletionProtection.Equal(state.DeletionProtection) {
		protected := plan.DeletionProtection.ValueBool()
		send = func(ctx context.Context, etag string) (*client.App, string, error) {
			if _, _, err := kind.get(ctx, r.client, &state); err != nil {
				return nil, "", err
			}
			app, nextETag, err := r.client.PatchApp(ctx, plan.Name.ValueString(), etag, client.PatchAppRequest{DeletionProtection: &protected})
			if err == nil {
				accepted = app
			}
			return app, nextETag, err
		}
	}
	guarded.update(ctx, &plan, &state, state.ETag.ValueString(), send, false, &resp.State, &resp.Diagnostics)
	if resp.Diagnostics.HasError() || accepted == nil {
		return
	}
	timeout, diags := plan.Timeouts.Update(ctx, appKind.defaults.update)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	r.waitAppObserved(ctx, timeout, "update", accepted, &plan, &resp.State, &resp.Diagnostics)
}

func appPhase(app *client.App) string {
	if app == nil || app.Status == nil {
		return ""
	}
	return app.Status.Phase
}

func mapAppToState(app *client.App, etag string, state *AppResourceModel) {
	state.Name = types.StringValue(app.Name)
	state.DeletionProtection = types.BoolValue(app.DeletionProtection)
	state.Phase = types.StringValue(appPhase(app))
	state.ETag = types.StringValue(etag)
	state.CreatedAt = types.StringValue(app.CreatedAt)
}

// App identity is independent of its opaque ETag. A same-name recreation
// cannot verify an accepted write or receive its stale retry.
const appIdentityChangedMessage = "The app identity changed since the plan. Run plan again before applying."

type appIdentityChangedError struct{}

func (appIdentityChangedError) Error() string { return appIdentityChangedMessage }

var errAppIdentityChanged error = appIdentityChangedError{}

func (r *AppResource) waitAppObserved(ctx context.Context, timeout time.Duration, op string, accepted *client.App, m *AppResourceModel, state *tfsdk.State, diags *diag.Diagnostics) {
	last := accepted
	err := pollUntil(ctx, timeout, func(ctx context.Context) (bool, error) {
		app, etag, err := r.client.GetApp(ctx, accepted.Name)
		if err != nil {
			return false, err
		}
		if app.UID != accepted.UID {
			return true, errAppIdentityChanged
		}
		last = app
		mapAppToState(app, etag, m)
		diags.Append(state.Set(ctx, m)...)
		if diags.HasError() {
			return true, errReadyFailed
		}
		if appPhase(app) == phaseTerminating {
			return true, errReadyFailed
		}
		return app.Status != nil && app.Status.ObservedGeneration >= accepted.Generation, nil
	})
	if err == nil {
		return
	}
	summary := "failed to update app"
	if op == "create" {
		summary = "failed to create app"
	}
	switch {
	case errors.Is(err, errAppIdentityChanged):
		diags.AddError(summary, appIdentityChangedMessage)
	case errors.Is(err, errReadyFailed):
		diags.AddError(summary, r.failedDetail(op, m, last))
	case isTerminalAPIError(err):
		diags.AddError(summary, apiErrorDetail(err))
	default:
		diags.AddError(summary, r.timedOutDetail(op, timeout, m, last))
	}
}
