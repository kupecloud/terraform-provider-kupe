package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/kupecloud/terraform-provider-kupe/internal/client"
)

var (
	_ resource.Resource                = &ClusterResource{}
	_ resource.ResourceWithImportState = &ClusterResource{}
)

type ClusterResource struct {
	client *client.Client
}

type ClusterResourceModel struct {
	Name        types.String           `tfsdk:"name"`
	DisplayName types.String           `tfsdk:"display_name"`
	Type        types.String           `tfsdk:"type"`
	Version     types.String           `tfsdk:"version"`
	Resources   *ClusterResourcesModel `tfsdk:"resources"`
	Phase       types.String           `tfsdk:"phase"`
	Endpoint    types.String           `tfsdk:"endpoint"`
	ETag        types.String           `tfsdk:"etag"`
	CreatedAt   types.String           `tfsdk:"created_at"`
}

type ClusterResourcesModel struct {
	CPU     types.String `tfsdk:"cpu"`
	Memory  types.String `tfsdk:"memory"`
	Storage types.String `tfsdk:"storage"`
}

func NewClusterResource() resource.Resource {
	return &ClusterResource{}
}

func (r *ClusterResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_cluster"
}

func (r *ClusterResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a Kupe Cloud cluster for a tenant.",
		Attributes: map[string]schema.Attribute{
			"name": schema.StringAttribute{
				Description: "Cluster name (immutable after creation).",
				Required:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"display_name": schema.StringAttribute{
				Description: "Human-readable display name (immutable after creation).",
				Required:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"type": schema.StringAttribute{
				Description: "Cluster type. Valid values are shared and dedicated. Immutable after creation.",
				Required:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
				Validators: []validator.String{
					stringvalidator.OneOf("shared", "dedicated"),
				},
			},
			"version": schema.StringAttribute{
				Description: "Kubernetes version (e.g., 1.31).",
				Optional:    true,
				Computed:    true,
			},
			"resources": schema.SingleNestedAttribute{
				Description: "Resource limits for the cluster.",
				Optional:    true,
				Attributes: map[string]schema.Attribute{
					"cpu": schema.StringAttribute{
						Description: "CPU limit (e.g., 4, 500m).",
						Optional:    true,
					},
					"memory": schema.StringAttribute{
						Description: "Memory limit (e.g., 16Gi, 512Mi).",
						Optional:    true,
					},
					"storage": schema.StringAttribute{
						Description: "Storage limit (e.g., 100Gi).",
						Optional:    true,
					},
				},
			},
			"phase": schema.StringAttribute{
				Description: "Current cluster phase, for example Pending, Provisioning, or Running.",
				Computed:    true,
			},
			"endpoint": schema.StringAttribute{
				Description: "Cluster API server endpoint.",
				Computed:    true,
			},
			"etag": schema.StringAttribute{
				Description: "Resource version used for optimistic locking during updates.",
				Computed:    true,
			},
			"created_at": schema.StringAttribute{
				Description: "Timestamp when the cluster was created.",
				Computed:    true,
			},
		},
	}
}

func (r *ClusterResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *ClusterResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan ClusterResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	createReq := client.CreateClusterRequest{
		Name:        plan.Name.ValueString(),
		DisplayName: plan.DisplayName.ValueString(),
		Type:        plan.Type.ValueString(),
		Version:     plan.Version.ValueString(),
	}

	if plan.Resources != nil {
		createReq.Resources = &client.ClusterResource{
			CPU:     plan.Resources.CPU.ValueString(),
			Memory:  plan.Resources.Memory.ValueString(),
			Storage: plan.Resources.Storage.ValueString(),
		}
	}

	cluster, etag, err := r.client.CreateCluster(ctx, createReq)
	if err != nil {
		resp.Diagnostics.AddError("failed to create cluster", err.Error())
		return
	}

	mapClusterToState(cluster, etag, &plan)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *ClusterResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state ClusterResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	cluster, etag, err := r.client.GetCluster(ctx, state.Name.ValueString())
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("failed to read cluster", err.Error())
		return
	}

	mapClusterToState(cluster, etag, &state)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *ClusterResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan ClusterResourceModel
	var state ClusterResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	patchReq := client.PatchClusterRequest{}
	hasChanges := false

	if !plan.Version.Equal(state.Version) {
		v := plan.Version.ValueString()
		patchReq.Version = &v
		hasChanges = true
	}

	switch {
	case plan.Resources != nil && (state.Resources == nil ||
		!plan.Resources.CPU.Equal(state.Resources.CPU) ||
		!plan.Resources.Memory.Equal(state.Resources.Memory) ||
		!plan.Resources.Storage.Equal(state.Resources.Storage)):
		patchReq.Resources = &client.ClusterResource{
			CPU:     plan.Resources.CPU.ValueString(),
			Memory:  plan.Resources.Memory.ValueString(),
			Storage: plan.Resources.Storage.ValueString(),
		}
		hasChanges = true
	case plan.Resources == nil && state.Resources != nil:
		// User removed the resources block — send empty to clear.
		patchReq.Resources = &client.ClusterResource{}
		hasChanges = true
	}

	if hasChanges {
		cluster, etag, err := r.client.UpdateCluster(ctx, plan.Name.ValueString(), state.ETag.ValueString(), patchReq)
		if err != nil {
			resp.Diagnostics.AddError("failed to update cluster", err.Error())
			return
		}
		mapClusterToState(cluster, etag, &plan)
	} else {
		// No API changes but always refresh computed fields
		cluster, etag, err := r.client.GetCluster(ctx, plan.Name.ValueString())
		if err != nil {
			resp.Diagnostics.AddError("failed to read cluster", err.Error())
			return
		}
		mapClusterToState(cluster, etag, &plan)
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *ClusterResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state ClusterResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	err := r.client.DeleteCluster(ctx, state.Name.ValueString())
	if err != nil && !client.IsNotFound(err) {
		resp.Diagnostics.AddError("failed to delete cluster", err.Error())
	}
}

func (r *ClusterResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("name"), req, resp)
}

func mapClusterToState(c *client.Cluster, etag string, state *ClusterResourceModel) {
	state.Name = types.StringValue(c.Name)
	state.DisplayName = types.StringValue(c.DisplayName)
	state.Type = types.StringValue(c.Type)
	state.Version = types.StringValue(c.Version)
	state.ETag = types.StringValue(etag)
	state.CreatedAt = types.StringValue(c.CreatedAt)

	if c.Status != nil {
		state.Phase = types.StringValue(c.Status.Phase)
		state.Endpoint = types.StringValue(c.Status.Endpoint)
	} else {
		state.Phase = types.StringValue("")
		state.Endpoint = types.StringValue("")
	}

	if c.Resources != nil {
		state.Resources = &ClusterResourcesModel{
			CPU:     types.StringValue(c.Resources.CPU),
			Memory:  types.StringValue(c.Resources.Memory),
			Storage: types.StringValue(c.Resources.Storage),
		}
	} else {
		state.Resources = nil
	}
}
