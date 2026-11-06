package provider

import (
	"context"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/tebibhost/terraform-provider-tebib/internal/client"
)

type publicIPAssociationResource struct{ base }

type publicIPAssociationModel struct {
	ID           types.String   `tfsdk:"id"`
	ProjectID    types.String   `tfsdk:"project_id"`
	PublicIPID   types.String   `tfsdk:"public_ip_id"`
	VMID         types.String   `tfsdk:"vm_id"`
	NatGatewayID types.String   `tfsdk:"nat_gateway_id"`
	Address      types.String   `tfsdk:"address"`
	Timeouts     timeouts.Value `tfsdk:"timeouts"`
}

func newPublicIPAssociationResource() resource.Resource { return &publicIPAssociationResource{} }

func (r *publicIPAssociationResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_public_ip_association"
}

func (r *publicIPAssociationResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	target := func(desc string) schema.StringAttribute {
		return schema.StringAttribute{
			Optional: true, MarkdownDescription: desc,
			PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			Validators: []validator.String{
				stringvalidator.ExactlyOneOf(path.MatchRoot("vm_id"), path.MatchRoot("nat_gateway_id")),
			},
		}
	}
	resp.Schema = schema.Schema{
		MarkdownDescription: "Points a public IP at a VM (1:1) or at a NAT gateway (outbound SNAT). " +
			"A VM's public IP reaches it through its VPC's NAT gateway, so the VPC needs one first. " +
			"Destroying the association keeps the address in the project.",
		Attributes: map[string]schema.Attribute{
			"id":             idAttr("Same as `public_ip_id`."),
			"project_id":     projectIDAttr(),
			"public_ip_id":   replaceString("Public IP ID (`eip-...`).", true),
			"vm_id":          target("VM to point the address at. Give this or `nat_gateway_id`."),
			"nat_gateway_id": target("NAT gateway to point the address at. Give this or `vm_id`."),
			"address":        computedString("The IPv4 address."),
		},
		Blocks: map[string]schema.Block{
			"timeouts": timeouts.Block(ctx, timeouts.Opts{Create: true, Delete: true}),
		},
	}
}

func (r *publicIPAssociationResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.configure(req.ProviderData, &resp.Diagnostics)
}

func (r *publicIPAssociationResource) ipPath(m *publicIPAssociationModel) string {
	return client.ProjectPath(m.ProjectID.ValueString(), "public-ips", m.PublicIPID.ValueString())
}

func (r *publicIPAssociationResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan publicIPAssociationModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	plan.ProjectID = types.StringValue(r.projectID(plan.ProjectID, &resp.Diagnostics))
	d, ds := plan.Timeouts.Create(ctx, 5*time.Minute)
	ctx, cancel := withTimeout(ctx, d, ds, &resp.Diagnostics)
	defer cancel()
	if resp.Diagnostics.HasError() {
		return
	}
	body := client.AssociateIn{VMID: strPtr(plan.VMID), NatGatewayID: strPtr(plan.NatGatewayID)}
	var ip client.PublicIP
	// A VM's NAT gateway may still be on its way: retry the 409 for a while.
	err := r.client().RetryConflict(ctx, conflictWindow, func() error {
		return r.client().Post(ctx, r.ipPath(&plan)+"/associate", body, &ip)
	})
	if err != nil {
		apiError(&resp.Diagnostics, "Associating the public IP", err)
		return
	}
	plan.ID = plan.PublicIPID
	plan.Address = types.StringValue(ip.Address)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	if _, err := client.Wait[client.PublicIP](ctx, r.client(), r.ipPath(&plan), nil); err != nil {
		waitError(&resp.Diagnostics, "the public IP", err)
	}
}

func (r *publicIPAssociationResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state publicIPAssociationModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	state.ProjectID = types.StringValue(r.projectID(state.ProjectID, &resp.Diagnostics))
	if state.PublicIPID.IsNull() {
		state.PublicIPID = state.ID
	}
	if resp.Diagnostics.HasError() {
		return
	}
	var ip client.PublicIP
	err := r.client().Get(ctx, r.ipPath(&state), &ip)
	if client.IsNotFound(err) || (err == nil && ip.State == "deleted") {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "Reading the public IP", err)
		return
	}
	state.ID = types.StringValue(ip.ID)
	state.Address = types.StringValue(ip.Address)
	switch ip.Association {
	case "vm":
		state.VMID = optString(ip.VMID)
		state.NatGatewayID = types.StringNull()
	case "nat-gateway":
		state.NatGatewayID = optString(ip.NatGatewayID)
		state.VMID = types.StringNull()
	default:
		// Not associated any more (someone disassociated it, or its VM went away).
		resp.State.RemoveResource(ctx)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *publicIPAssociationResource) Update(_ context.Context, _ resource.UpdateRequest, resp *resource.UpdateResponse) {
	resp.Diagnostics.AddWarning("Nothing to update", "Associations change by replacement.")
}

func (r *publicIPAssociationResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state publicIPAssociationModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	d, ds := state.Timeouts.Delete(ctx, 5*time.Minute)
	ctx, cancel := withTimeout(ctx, d, ds, &resp.Diagnostics)
	defer cancel()
	// Only disassociate if it still points where this resource put it: the address may have
	// moved to another target since.
	var ip client.PublicIP
	err := r.client().Get(ctx, r.ipPath(&state), &ip)
	if client.IsNotFound(err) {
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "Reading the public IP", err)
		return
	}
	if !stillOurs(&state, &ip) {
		return
	}
	err = r.client().RetryConflict(ctx, conflictWindow, func() error {
		return r.client().Post(ctx, r.ipPath(&state)+"/disassociate", nil, nil)
	})
	if client.IsNotFound(err) {
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "Disassociating the public IP", err)
		return
	}
	if _, err := client.Wait[client.PublicIP](ctx, r.client(), r.ipPath(&state), nil); err != nil && !client.IsNotFound(err) {
		waitError(&resp.Diagnostics, "the public IP", err)
	}
}

func stillOurs(m *publicIPAssociationModel, ip *client.PublicIP) bool {
	if v := strPtr(m.VMID); v != nil {
		return ip.Association == "vm" && ip.VMID != nil && *ip.VMID == *v
	}
	if n := strPtr(m.NatGatewayID); n != nil {
		return ip.Association == "nat-gateway" && ip.NatGatewayID != nil && *ip.NatGatewayID == *n
	}
	return false
}

// ImportState accepts <public_ip_id> or <project_id>/<public_ip_id>.
func (r *publicIPAssociationResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	importProjectScoped(ctx, req, resp)
}
