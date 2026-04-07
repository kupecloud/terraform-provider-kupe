package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/kupecloud/terraform-provider-kupe/internal/client"
)

var (
	_ resource.Resource                = &SecretResource{}
	_ resource.ResourceWithImportState = &SecretResource{}
)

type SecretResource struct {
	client *client.Client
}

type SecretResourceModel struct {
	Name       types.String `tfsdk:"name"`
	SecretPath types.String `tfsdk:"secret_path"`
	Sync       types.List   `tfsdk:"sync"`
	Phase      types.String `tfsdk:"phase"`
	ETag       types.String `tfsdk:"etag"`
	CreatedAt  types.String `tfsdk:"created_at"`
}

var syncTargetAttrTypes = map[string]attr.Type{
	"cluster":     types.StringType,
	"namespace":   types.StringType,
	"secret_name": types.StringType,
}

func NewSecretResource() resource.Resource {
	return &SecretResource{}
}

func (r *SecretResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_secret"
}

func (r *SecretResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a Kupe Cloud secret definition and its sync targets.",
		Attributes: map[string]schema.Attribute{
			"name": schema.StringAttribute{
				Description: "Secret name (immutable after creation).",
				Required:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"secret_path": schema.StringAttribute{
				Description: "OpenBao KV v2 key path for the stored value. Immutable after creation.",
				Required:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"sync": schema.ListNestedAttribute{
				Description: "Cluster/namespace targets to sync this secret to.",
				Optional:    true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"cluster": schema.StringAttribute{
							Description: "Target cluster name.",
							Required:    true,
						},
						"namespace": schema.StringAttribute{
							Description: "Target namespace in the cluster.",
							Required:    true,
						},
						"secret_name": schema.StringAttribute{
							Description: "Override the K8s secret name (defaults to the managed secret name).",
							Optional:    true,
						},
					},
				},
			},
			"phase": schema.StringAttribute{
				Description: "Current sync phase for the managed secret.",
				Computed:    true,
			},
			"etag": schema.StringAttribute{
				Description: "Resource version used for optimistic locking during updates.",
				Computed:    true,
			},
			"created_at": schema.StringAttribute{
				Description: "Timestamp when the managed secret definition was created.",
				Computed:    true,
			},
		},
	}
}

func (r *SecretResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *SecretResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan SecretResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	createReq := client.CreateSecretRequest{
		Name:       plan.Name.ValueString(),
		SecretPath: plan.SecretPath.ValueString(),
		Sync:       extractSyncTargets(plan.Sync),
	}

	secret, etag, err := r.client.CreateSecret(ctx, createReq)
	if err != nil {
		resp.Diagnostics.AddError("failed to create secret", err.Error())
		return
	}

	mapSecretToState(secret, etag, &plan, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *SecretResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state SecretResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	secret, etag, err := r.client.GetSecret(ctx, state.Name.ValueString())
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("failed to read secret", err.Error())
		return
	}

	mapSecretToState(secret, etag, &state, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *SecretResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state SecretResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	patchReq := client.PatchSecretRequest{
		Sync: extractSyncTargets(plan.Sync),
	}

	secret, etag, err := r.client.UpdateSecret(ctx, plan.Name.ValueString(), state.ETag.ValueString(), patchReq)
	if err != nil {
		resp.Diagnostics.AddError("failed to update secret", err.Error())
		return
	}

	mapSecretToState(secret, etag, &plan, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *SecretResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state SecretResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	err := r.client.DeleteSecret(ctx, state.Name.ValueString())
	if err != nil && !client.IsNotFound(err) {
		resp.Diagnostics.AddError("failed to delete secret", err.Error())
	}
}

func (r *SecretResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("name"), req, resp)
}

func extractSyncTargets(list types.List) []client.SyncTarget {
	if list.IsNull() || list.IsUnknown() {
		return nil
	}
	var targets []client.SyncTarget
	for _, elem := range list.Elements() {
		obj, ok := elem.(types.Object)
		if !ok {
			continue
		}
		attrs := obj.Attributes()
		cluster, _ := attrs["cluster"].(types.String)
		namespace, _ := attrs["namespace"].(types.String)
		t := client.SyncTarget{
			Cluster:   cluster.ValueString(),
			Namespace: namespace.ValueString(),
		}
		if sn, ok := attrs["secret_name"].(types.String); ok && !sn.IsNull() {
			t.SecretName = sn.ValueString()
		}
		targets = append(targets, t)
	}
	return targets
}

func mapSecretToState(s *client.Secret, etag string, state *SecretResourceModel, diags *diag.Diagnostics) {
	state.Name = types.StringValue(s.Name)
	state.SecretPath = types.StringValue(s.SecretPath)
	state.ETag = types.StringValue(etag)
	state.CreatedAt = types.StringValue(s.CreatedAt)

	if s.Status != nil {
		state.Phase = types.StringValue(s.Status.Phase)
	} else {
		state.Phase = types.StringValue("")
	}

	if len(s.Sync) > 0 {
		syncElements := make([]attr.Value, 0, len(s.Sync))
		for _, t := range s.Sync {
			secretName := types.StringNull()
			if t.SecretName != "" {
				secretName = types.StringValue(t.SecretName)
			}
			attrs := map[string]attr.Value{
				"cluster":     types.StringValue(t.Cluster),
				"namespace":   types.StringValue(t.Namespace),
				"secret_name": secretName,
			}
			objVal, d := types.ObjectValue(syncTargetAttrTypes, attrs)
			diags.Append(d...)
			syncElements = append(syncElements, objVal)
		}
		listVal, d := types.ListValue(types.ObjectType{AttrTypes: syncTargetAttrTypes}, syncElements)
		diags.Append(d...)
		state.Sync = listVal
	} else {
		state.Sync = types.ListNull(types.ObjectType{AttrTypes: syncTargetAttrTypes})
	}
}
