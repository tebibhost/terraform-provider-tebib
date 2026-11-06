package provider

import (
	"context"
	"net/url"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/tebibhost/terraform-provider-tebib/internal/client"
)

type projectResource struct{ base }

type projectModel struct {
	ID             types.String   `tfsdk:"id"`
	OrganizationID types.String   `tfsdk:"organization_id"`
	TRN            types.String   `tfsdk:"trn"`
	Name           types.String   `tfsdk:"name"`
	Description    types.String   `tfsdk:"description"`
	Tags           types.Map      `tfsdk:"tags"`
	State          types.String   `tfsdk:"state"`
	StatusMessage  types.String   `tfsdk:"status_message"`
	CreatedAt      types.String   `tfsdk:"created_at"`
	Timeouts       timeouts.Value `tfsdk:"timeouts"`
}

func newProjectResource() resource.Resource { return &projectResource{} }

func (r *projectResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_project"
}

func (r *projectResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "A project: the unit that holds resources, quotas and usage in an " +
			"organization. A new project comes with a default VPC, subnet and security group. " +
			"Deleting a project deletes everything in it.",
		Attributes: map[string]schema.Attribute{
			"id":              idAttr("Project ID (`p-...`)."),
			"organization_id": orgIDAttr(),
			"trn":             computedString("TebibHost resource name (TRN)."),
			"name":            replaceString("Project name, unique in the organization. Changing it replaces the project.", true),
			"description":     descriptionAttr(),
			"tags":            tagsAttr(),
			"state":           computedString("Lifecycle state: `active` once ready."),
			"status_message":  volatileString("What the platform last said about the project."),
			"created_at":      computedString("When it was created (RFC 3339)."),
		},
		Blocks: map[string]schema.Block{
			"timeouts": timeouts.Block(ctx, timeouts.Opts{Create: true, Update: true, Delete: true}),
		},
	}
}

func (r *projectResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.configure(req.ProviderData, &resp.Diagnostics)
}

func (r *projectResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, pathRoot("id"), req.ID)...)
}

func (m *projectModel) fromAPI(p *client.Project) {
	m.ID = types.StringValue(p.ID)
	m.OrganizationID = types.StringValue(p.OrganizationID)
	m.TRN = types.StringValue(p.TRN)
	m.Name = types.StringValue(p.Name)
	m.Description = types.StringValue(p.Description)
	m.Tags = tagsValue(p.Tags)
	m.State = types.StringValue(p.State)
	m.StatusMessage = types.StringValue(p.StatusMessage)
	m.CreatedAt = types.StringValue(p.CreatedAt)
}

func projectPath(id string) string { return "/projects/" + url.PathEscape(id) }

func (r *projectResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan projectModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	org := r.orgID(plan.OrganizationID, &resp.Diagnostics)
	d, ds := plan.Timeouts.Create(ctx, 10*time.Minute)
	ctx, cancel := withTimeout(ctx, d, ds, &resp.Diagnostics)
	defer cancel()
	body := client.ProjectIn{
		Name: plan.Name.ValueString(), Description: plan.Description.ValueString(),
		Tags: tagsFrom(ctx, plan.Tags, &resp.Diagnostics),
	}
	if resp.Diagnostics.HasError() {
		return
	}
	var p client.Project
	if err := r.client().Create(ctx, client.OrgPath(org, "projects"), body, &p); err != nil {
		apiError(&resp.Diagnostics, "Creating the project", err)
		return
	}
	plan.fromAPI(&p)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	settled, err := client.Wait[client.Project](ctx, r.client(), projectPath(p.ID), nil)
	if settled != nil {
		plan.fromAPI(settled)
		resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	}
	if err != nil {
		waitError(&resp.Diagnostics, "the project", err)
	}
}

func (r *projectResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state projectModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var p client.Project
	err := r.client().Get(ctx, projectPath(state.ID.ValueString()), &p)
	if client.IsNotFound(err) || (err == nil && p.State == "deleted") {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "Reading the project", err)
		return
	}
	state.fromAPI(&p)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *projectResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state projectModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	d, ds := plan.Timeouts.Update(ctx, 5*time.Minute)
	ctx, cancel := withTimeout(ctx, d, ds, &resp.Diagnostics)
	defer cancel()
	patch := client.TagsPatch{}
	if !plan.Description.Equal(state.Description) {
		patch.Description = ptr(plan.Description.ValueString())
	}
	if !plan.Tags.Equal(state.Tags) {
		patch.Tags = ptr(tagsFrom(ctx, plan.Tags, &resp.Diagnostics))
	}
	p := projectPath(state.ID.ValueString())
	if patch.Description != nil || patch.Tags != nil {
		if err := r.client().Patch(ctx, p, patch, nil); err != nil {
			apiError(&resp.Diagnostics, "Updating the project", err)
			return
		}
	}
	settled, err := client.Wait[client.Project](ctx, r.client(), p, nil)
	if err != nil {
		waitError(&resp.Diagnostics, "the project", err)
		return
	}
	plan.fromAPI(settled)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *projectResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state projectModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	d, ds := state.Timeouts.Delete(ctx, 30*time.Minute)
	ctx, cancel := withTimeout(ctx, d, ds, &resp.Diagnostics)
	defer cancel()
	if err := deleteAndWait[client.Project](ctx, r.client(), projectPath(state.ID.ValueString())); err != nil {
		apiError(&resp.Diagnostics, "Deleting the project", err)
	}
}
