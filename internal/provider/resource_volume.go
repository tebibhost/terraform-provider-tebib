package provider

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/tebibhost/terraform-provider-tebib/internal/client"
)

// -- tebib_volume ---------------------------------------------------------------------------

type volumeResource struct{ base }

type volumeModel struct {
	projectMeta
	SizeGiB             types.Int64    `tfsdk:"size_gib"`
	DeleteOnTermination types.Bool     `tfsdk:"delete_on_termination"`
	VMID                types.String   `tfsdk:"vm_id"`
	Boot                types.Bool     `tfsdk:"boot"`
	SourceImage         types.String   `tfsdk:"source_image"`
	Timeouts            timeouts.Value `tfsdk:"timeouts"`
}

func newVolumeResource() resource.Resource { return &volumeResource{} }

func (r *volumeResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_volume"
}

func (r *volumeResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "An empty block storage volume (Ceph RBD). Attach it to a VM with " +
			"`tebib_volume_attachment`. It can grow in place; a smaller size replaces it. " +
			"Deleting it destroys its data.",
		Attributes: mergeAttrs(metaAttrs("volumes", "Volume ID (`vol-...`)."), map[string]schema.Attribute{
			"size_gib": schema.Int64Attribute{
				Required:            true,
				MarkdownDescription: "Size in GiB (1-2048). Growing it resizes in place (then grow the filesystem in the guest).",
				Validators:          []validator.Int64{int64validator.Between(1, 2048)},
				PlanModifiers:       []planmodifier.Int64{replaceIfShrinks()},
			},
			"delete_on_termination": schema.BoolAttribute{
				Optional: true, Computed: true, Default: booldefault.StaticBool(false),
				MarkdownDescription: "Delete the volume when the VM it is attached to is deleted. Default: `false`.",
			},
			"vm_id":        volatileString("The VM it is attached to, if any (managed by `tebib_volume_attachment`)."),
			"boot":         stableBool("Whether it is a VM's boot volume."),
			"source_image": computedString("Image a boot volume was made from."),
		}),
		Blocks: map[string]schema.Block{
			"timeouts": timeouts.Block(ctx, timeouts.Opts{Create: true, Update: true, Delete: true}),
		},
	}
}

func (r *volumeResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.configure(req.ProviderData, &resp.Diagnostics)
}

func (r *volumeResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	importProjectScoped(ctx, req, resp)
}

func volumePath(project, id string) string { return client.ProjectPath(project, "volumes", id) }

func (m *volumeModel) fromAPI(v *client.Volume) {
	m.projectMeta.fromAPI(v.Meta)
	m.SizeGiB = types.Int64Value(v.SizeGiB)
	m.DeleteOnTermination = types.BoolValue(v.DeleteOnTermination)
	m.VMID = optString(v.VMID)
	m.Boot = types.BoolValue(v.Boot)
	m.SourceImage = optString(v.SourceImage)
}

func (r *volumeResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan volumeModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	project := r.projectID(plan.ProjectID, &resp.Diagnostics)
	d, ds := plan.Timeouts.Create(ctx, 10*time.Minute)
	ctx, cancel := withTimeout(ctx, d, ds, &resp.Diagnostics)
	defer cancel()
	body := client.VolumeIn{
		Name: plan.Name.ValueString(), SizeGiB: plan.SizeGiB.ValueInt64(),
		DeleteOnTermination: plan.DeleteOnTermination.ValueBool(),
		Description:         plan.Description.ValueString(), Tags: tagsFrom(ctx, plan.Tags, &resp.Diagnostics),
	}
	if resp.Diagnostics.HasError() {
		return
	}
	var vol client.Volume
	if err := r.client().Create(ctx, client.ProjectPath(project, "volumes"), body, &vol); err != nil {
		apiError(&resp.Diagnostics, "Creating the volume", err)
		return
	}
	plan.fromAPI(&vol)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	settled, err := client.Wait[client.Volume](ctx, r.client(), volumePath(project, vol.ID), nil)
	if settled != nil {
		plan.fromAPI(settled)
		resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	}
	if err != nil {
		waitError(&resp.Diagnostics, "the volume", err)
	}
}

func (r *volumeResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state volumeModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	project := r.projectID(state.ProjectID, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	var vol client.Volume
	err := r.client().Get(ctx, volumePath(project, state.ID.ValueString()), &vol)
	if client.IsNotFound(err) || (err == nil && vol.State == "deleted") {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "Reading the volume", err)
		return
	}
	state.fromAPI(&vol)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *volumeResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state volumeModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	d, ds := plan.Timeouts.Update(ctx, 10*time.Minute)
	ctx, cancel := withTimeout(ctx, d, ds, &resp.Diagnostics)
	defer cancel()
	c := r.client()
	p := volumePath(state.ProjectID.ValueString(), state.ID.ValueString())

	var patch client.VolumePatch
	changed := false
	if tp := tagsPatch(ctx, plan.projectMeta, state.projectMeta, &resp.Diagnostics); tp != nil {
		patch.Description, patch.Tags = tp.Description, tp.Tags
		changed = true
	}
	if !plan.DeleteOnTermination.Equal(state.DeleteOnTermination) {
		patch.DeleteOnTermination = ptr(plan.DeleteOnTermination.ValueBool())
		changed = true
	}
	if changed {
		if err := c.Patch(ctx, p, patch, nil); err != nil {
			apiError(&resp.Diagnostics, "Updating the volume", err)
			return
		}
	}
	if plan.SizeGiB.ValueInt64() > state.SizeGiB.ValueInt64() {
		err := c.RetryConflict(ctx, conflictWindow, func() error {
			return c.Post(ctx, p+"/resize", map[string]int64{"size_gib": plan.SizeGiB.ValueInt64()}, nil)
		})
		if err != nil {
			apiError(&resp.Diagnostics, "Growing the volume", err)
			return
		}
	}
	settled, err := client.Wait[client.Volume](ctx, c, p, nil)
	if err != nil {
		waitError(&resp.Diagnostics, "the volume", err)
		return
	}
	plan.fromAPI(settled)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *volumeResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state volumeModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	d, ds := state.Timeouts.Delete(ctx, 10*time.Minute)
	ctx, cancel := withTimeout(ctx, d, ds, &resp.Diagnostics)
	defer cancel()
	if err := deleteAndWait[client.Volume](ctx, r.client(), volumePath(state.ProjectID.ValueString(), state.ID.ValueString())); err != nil {
		apiError(&resp.Diagnostics, "Deleting the volume", err)
	}
}

// -- tebib_volume_attachment ----------------------------------------------------------------

type volumeAttachmentResource struct{ base }

type volumeAttachmentModel struct {
	ID        types.String   `tfsdk:"id"`
	ProjectID types.String   `tfsdk:"project_id"`
	VolumeID  types.String   `tfsdk:"volume_id"`
	VMID      types.String   `tfsdk:"vm_id"`
	Timeouts  timeouts.Value `tfsdk:"timeouts"`
}

func newVolumeAttachmentResource() resource.Resource { return &volumeAttachmentResource{} }

func (r *volumeAttachmentResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_volume_attachment"
}

func (r *volumeAttachmentResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Attaches a volume to a VM. A running VM gets it hot-plugged. " +
			"Destroying the attachment detaches the volume (unmount it in the guest first) and keeps it.",
		Attributes: map[string]schema.Attribute{
			"id":         idAttr("`<volume_id>/<vm_id>`."),
			"project_id": projectIDAttr(),
			"volume_id":  replaceString("Volume ID (`vol-...`).", true),
			"vm_id":      replaceString("VM ID (`vm-...`).", true),
		},
		Blocks: map[string]schema.Block{
			"timeouts": timeouts.Block(ctx, timeouts.Opts{Create: true, Delete: true}),
		},
	}
}

func (r *volumeAttachmentResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.configure(req.ProviderData, &resp.Diagnostics)
}

// attachedTo is true once the VM reports the volume attached.
func (r *volumeAttachmentResource) waitAttached(ctx context.Context, project, volumeID, vmID string) error {
	c := r.client()
	if _, err := client.Wait[client.Volume](ctx, c, volumePath(project, volumeID), nil); err != nil {
		return err
	}
	_, err := client.Wait(ctx, c, client.ProjectPath(project, "vms", vmID), func(vm client.VM) bool {
		for _, a := range vm.Volumes {
			if a.VolumeID == volumeID {
				return a.State == "attached"
			}
		}
		// A stopped VM lists only what its controller has applied; the volume's vm_id is
		// the source of truth then.
		return vm.PowerState != "running"
	})
	return err
}

func (r *volumeAttachmentResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan volumeAttachmentModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	project := r.projectID(plan.ProjectID, &resp.Diagnostics)
	d, ds := plan.Timeouts.Create(ctx, 10*time.Minute)
	ctx, cancel := withTimeout(ctx, d, ds, &resp.Diagnostics)
	defer cancel()
	if resp.Diagnostics.HasError() {
		return
	}
	c := r.client()
	volID, vmID := plan.VolumeID.ValueString(), plan.VMID.ValueString()
	err := c.RetryConflict(ctx, conflictWindow, func() error {
		return c.Post(ctx, volumePath(project, volID)+"/attach", map[string]string{"vm_id": vmID}, nil)
	})
	if err != nil {
		apiError(&resp.Diagnostics, "Attaching the volume", err)
		return
	}
	plan.ProjectID = types.StringValue(project)
	plan.ID = types.StringValue(volID + "/" + vmID)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	if err := r.waitAttached(ctx, project, volID, vmID); err != nil {
		waitError(&resp.Diagnostics, "the volume attachment", err)
	}
}

func (r *volumeAttachmentResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state volumeAttachmentModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	project := r.projectID(state.ProjectID, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	var vol client.Volume
	err := r.client().Get(ctx, volumePath(project, state.VolumeID.ValueString()), &vol)
	if client.IsNotFound(err) || (err == nil && vol.State == "deleted") {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "Reading the volume", err)
		return
	}
	if vol.VMID == nil || *vol.VMID != state.VMID.ValueString() {
		resp.State.RemoveResource(ctx) // detached, or attached elsewhere
		return
	}
	state.ProjectID = types.StringValue(project)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *volumeAttachmentResource) Update(_ context.Context, _ resource.UpdateRequest, resp *resource.UpdateResponse) {
	resp.Diagnostics.AddWarning("Nothing to update", "Attachments change by replacement.")
}

func (r *volumeAttachmentResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state volumeAttachmentModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	d, ds := state.Timeouts.Delete(ctx, 10*time.Minute)
	ctx, cancel := withTimeout(ctx, d, ds, &resp.Diagnostics)
	defer cancel()
	c := r.client()
	p := volumePath(state.ProjectID.ValueString(), state.VolumeID.ValueString())
	var vol client.Volume
	if err := c.Get(ctx, p, &vol); err != nil {
		if !client.IsNotFound(err) {
			apiError(&resp.Diagnostics, "Reading the volume", err)
		}
		return
	}
	if vol.VMID == nil || *vol.VMID != state.VMID.ValueString() {
		return // already detached (e.g. its VM was deleted)
	}
	err := c.RetryConflict(ctx, conflictWindow, func() error { return c.Post(ctx, p+"/detach", nil, nil) })
	if client.IsNotFound(err) {
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "Detaching the volume", err)
		return
	}
	if _, err := client.Wait[client.Volume](ctx, c, p, func(v client.Volume) bool { return v.VMID == nil }); err != nil && !client.IsNotFound(err) {
		waitError(&resp.Diagnostics, "the volume to detach", err)
	}
}

// ImportState accepts <volume_id>/<vm_id> or <project_id>/<volume_id>/<vm_id>.
func (r *volumeAttachmentResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.Split(req.ID, "/")
	if len(parts) == 3 {
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("project_id"), parts[0])...)
		parts = parts[1:]
	}
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		resp.Diagnostics.AddError("Invalid import ID",
			fmt.Sprintf("Expected <volume_id>/<vm_id> or <project_id>/<volume_id>/<vm_id>, got %q.", req.ID))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("volume_id"), parts[0])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("vm_id"), parts[1])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), parts[0]+"/"+parts[1])...)
}
