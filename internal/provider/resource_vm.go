package provider

import (
	"context"
	"fmt"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/setplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/tebibhost/terraform-provider-tebib/internal/client"
)

type vmResource struct{ base }

type vmModel struct {
	projectMeta
	Image             types.String   `tfsdk:"image"`
	InstanceType      types.String   `tfsdk:"instance_type"`
	SSHKeyIDs         types.Set      `tfsdk:"ssh_key_ids"`
	SubnetID          types.String   `tfsdk:"subnet_id"`
	SecurityGroupIDs  types.Set      `tfsdk:"security_group_ids"`
	RootDiskGiB       types.Int64    `tfsdk:"root_disk_gib"`
	UserData          types.String   `tfsdk:"user_data"`
	Hostname          types.String   `tfsdk:"hostname"`
	PowerState        types.String   `tfsdk:"power_state"`
	CurrentPowerState types.String   `tfsdk:"current_power_state"`
	VCPUs             types.Int64    `tfsdk:"vcpus"`
	MemoryMiB         types.Int64    `tfsdk:"memory_mib"`
	IPv4Address       types.String   `tfsdk:"ipv4_address"`
	IPv6Address       types.String   `tfsdk:"ipv6_address"`
	BootVolumeID      types.String   `tfsdk:"boot_volume_id"`
	RestartRequired   types.Bool     `tfsdk:"restart_required"`
	GuestAgent        types.Bool     `tfsdk:"guest_agent"`
	Timeouts          timeouts.Value `tfsdk:"timeouts"`
}

func newVMResource() resource.Resource { return &vmResource{} }

func (r *vmResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_vm"
}

func (r *vmResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	keepUnlessPower := []planmodifier.String{stringKeepUnless("power_state", "instance_type")}
	resp.Schema = schema.Schema{
		MarkdownDescription: "A virtual machine, created from an image.\n\n" +
			"In place: `instance_type` (the VM is stopped, resized, then started again if " +
			"`power_state` is `running`), `security_group_ids`, `power_state`, a larger " +
			"`root_disk_gib`, `description` and `tags`. Any other change replaces the VM.\n\n" +
			"Deleting the VM deletes its boot volume and the volumes marked " +
			"`delete_on_termination`; other volumes are detached and kept, and its public IP " +
			"stays in the project.",
		Attributes: mergeAttrs(metaAttrs("VMs", "VM ID (`vm-...`)."), map[string]schema.Attribute{
			"image": replaceString("Image name, e.g. `ubuntu-24.04` (see the `tebib_images` data source). "+
				"Changing it replaces the VM.", true),
			"instance_type": schema.StringAttribute{
				Required: true,
				MarkdownDescription: "Instance type, e.g. `b1.small` (see the `tebib_instance_types` " +
					"data source). Changing it stops, resizes and restarts the VM.",
			},
			"ssh_key_ids": schema.SetAttribute{
				ElementType: types.StringType, Optional: true, Computed: true,
				MarkdownDescription: "SSH keys to install for the image's default user. They are copied " +
					"at creation: changing them replaces the VM.",
				PlanModifiers: []planmodifier.Set{setplanmodifier.RequiresReplaceIfConfigured(), setplanmodifier.UseStateForUnknown()},
			},
			"subnet_id": optionalComputedReplace("Subnet ID. Default: the project's default subnet. " +
				"Changing it replaces the VM."),
			"security_group_ids": schema.SetAttribute{
				ElementType: types.StringType, Optional: true, Computed: true,
				MarkdownDescription: "Security groups of the VM's network interface (all of them), from " +
					"the subnet's VPC. Default: the VPC's default group. Changes apply in place.",
				PlanModifiers: []planmodifier.Set{setplanmodifier.UseStateForUnknown()},
			},
			"root_disk_gib": schema.Int64Attribute{
				Optional: true, Computed: true,
				MarkdownDescription: "Boot volume size in GiB. Default: the image's minimum. Growing it " +
					"resizes the boot volume in place (grow the filesystem in the guest); a smaller " +
					"value replaces the VM.",
				Validators:    []validator.Int64{int64validator.Between(1, 2048)},
				PlanModifiers: []planmodifier.Int64{int64planmodifier.UseStateForUnknown(), replaceIfShrinks()},
			},
			"user_data": schema.StringAttribute{
				Optional: true,
				MarkdownDescription: "cloud-init user data (`#cloud-config ...`), read on first boot. " +
					"The API doesn't return it, so it is not refreshed. Changing it replaces the VM.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"hostname": optionalComputedReplace("Hostname. Default: the name. Changing it replaces the VM."),
			"power_state": schema.StringAttribute{
				Optional: true, Computed: true, Default: stringdefault.StaticString("running"),
				MarkdownDescription: "Wanted power state: `running` or `stopped`. Default: `running`. " +
					"A shutdown from inside the guest leaves this `running`; apply again to start it.",
				Validators: []validator.String{stringvalidator.OneOf("running", "stopped")},
			},
			"current_power_state": schema.StringAttribute{
				Computed: true, MarkdownDescription: "Power state the platform last saw.",
				PlanModifiers: keepUnlessPower,
			},
			"vcpus": schema.Int64Attribute{
				Computed: true, MarkdownDescription: "vCPUs of the instance type.",
				PlanModifiers: []planmodifier.Int64{int64KeepUnless("instance_type")},
			},
			"memory_mib": schema.Int64Attribute{
				Computed: true, MarkdownDescription: "Memory of the instance type, in MiB.",
				PlanModifiers: []planmodifier.Int64{int64KeepUnless("instance_type")},
			},
			"ipv4_address": schema.StringAttribute{
				Computed: true, MarkdownDescription: "Primary private IPv4 address.",
				PlanModifiers: keepUnlessPower,
			},
			"ipv6_address": schema.StringAttribute{
				Computed: true, MarkdownDescription: "IPv6 address (routed, public).",
				PlanModifiers: keepUnlessPower,
			},
			"boot_volume_id": computedString("Boot volume ID (`vol-...`)."),
			"restart_required": schema.BoolAttribute{
				Computed: true, MarkdownDescription: "Whether a change waits for a restart.",
				PlanModifiers: []planmodifier.Bool{boolKeepUnless("power_state", "instance_type")},
			},
			"guest_agent": schema.BoolAttribute{
				Computed: true, MarkdownDescription: "Whether the guest agent is answering.",
				PlanModifiers: []planmodifier.Bool{boolplanmodifier.UseStateForUnknown()},
			},
		}),
		Blocks: map[string]schema.Block{
			"timeouts": timeouts.Block(ctx, timeouts.Opts{Create: true, Update: true, Delete: true}),
		},
	}
}

func (r *vmResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.configure(req.ProviderData, &resp.Diagnostics)
}

func (r *vmResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	importProjectScoped(ctx, req, resp)
}

func vmPath(m *vmModel) string {
	return client.ProjectPath(m.ProjectID.ValueString(), "vms", m.ID.ValueString())
}

func primary(addrs []client.VMAddress, family string) types.String {
	for _, a := range addrs {
		if a.Family == family && a.Primary {
			return types.StringValue(a.Address)
		}
	}
	for _, a := range addrs {
		if a.Family == family {
			return types.StringValue(a.Address)
		}
	}
	return types.StringNull()
}

func (m *vmModel) fromAPI(vm *client.VM) {
	m.fromAPIMeta(vm.Meta)
	m.Image = types.StringValue(vm.Image)
	m.InstanceType = types.StringValue(vm.InstanceType)
	m.SSHKeyIDs = stringSetValue(vm.SSHKeyIDs)
	m.SubnetID = types.StringValue(vm.SubnetID)
	m.SecurityGroupIDs = stringSetValue(vm.SecurityGroupIDs)
	m.Hostname = types.StringValue(vm.Hostname)
	m.PowerState = types.StringValue(vm.DesiredPowerState)
	m.CurrentPowerState = types.StringValue(vm.PowerState)
	m.VCPUs = types.Int64Value(vm.VCPUs)
	m.MemoryMiB = types.Int64Value(vm.MemoryMiB)
	m.IPv4Address = primary(vm.Addresses, "ipv4")
	m.IPv6Address = primary(vm.Addresses, "ipv6")
	m.BootVolumeID = optString(vm.BootVolumeID)
	m.RestartRequired = types.BoolValue(vm.RestartRequired)
	m.GuestAgent = types.BoolValue(vm.GuestAgent)
}

func (m *vmModel) fromAPIMeta(meta client.Meta) { m.projectMeta.fromAPI(meta) }

// readRootDisk fills root_disk_gib from the boot volume.
func (r *vmResource) readRootDisk(ctx context.Context, m *vmModel, diags *diag.Diagnostics) {
	if m.BootVolumeID.IsNull() {
		if m.RootDiskGiB.IsUnknown() {
			m.RootDiskGiB = types.Int64Null()
		}
		return
	}
	var vol client.Volume
	err := r.client().Get(ctx, client.ProjectPath(m.ProjectID.ValueString(), "volumes", m.BootVolumeID.ValueString()), &vol)
	if err != nil {
		if m.RootDiskGiB.IsUnknown() {
			m.RootDiskGiB = types.Int64Null()
		}
		if !client.IsNotFound(err) {
			diags.AddWarning("Reading the boot volume", err.Error())
		}
		return
	}
	m.RootDiskGiB = types.Int64Value(vol.SizeGiB)
}

func powerIs(want string) func(client.VM) bool {
	return func(vm client.VM) bool { return vm.PowerState == want }
}

func (r *vmResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan vmModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	project := r.projectID(plan.ProjectID, &resp.Diagnostics)
	d, ds := plan.Timeouts.Create(ctx, 15*time.Minute)
	ctx, cancel := withTimeout(ctx, d, ds, &resp.Diagnostics)
	defer cancel()
	body := client.VMIn{
		Name:         plan.Name.ValueString(),
		Image:        plan.Image.ValueString(),
		InstanceType: plan.InstanceType.ValueString(),
		SSHKeyIDs:    stringsFromSet(ctx, plan.SSHKeyIDs, &resp.Diagnostics),
		SubnetID:     strPtr(plan.SubnetID),
		RootDiskGiB:  int64Ptr(plan.RootDiskGiB),
		UserData:     plan.UserData.ValueString(),
		Hostname:     strPtr(plan.Hostname),
		PowerState:   plan.PowerState.ValueString(),
		Description:  plan.Description.ValueString(),
		Tags:         tagsFrom(ctx, plan.Tags, &resp.Diagnostics),
	}
	if !plan.SecurityGroupIDs.IsUnknown() && !plan.SecurityGroupIDs.IsNull() {
		body.SecurityGroupIDs = stringsFromSet(ctx, plan.SecurityGroupIDs, &resp.Diagnostics)
	}
	if resp.Diagnostics.HasError() {
		return
	}
	var vm client.VM
	if err := r.client().Create(ctx, client.ProjectPath(project, "vms"), body, &vm); err != nil {
		apiError(&resp.Diagnostics, "Creating the VM", err)
		return
	}
	userData := plan.UserData
	rootDisk := plan.RootDiskGiB
	save := func(v *client.VM) {
		plan.fromAPI(v)
		plan.UserData = userData
		if !rootDisk.IsUnknown() {
			plan.RootDiskGiB = rootDisk
		}
	}
	save(&vm)
	if plan.RootDiskGiB.IsUnknown() {
		plan.RootDiskGiB = types.Int64Null()
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	settled, err := client.Wait(ctx, r.client(), vmPath(&plan), powerIs(body.PowerState))
	if settled != nil {
		save(settled)
	}
	if err == nil {
		r.readRootDisk(ctx, &plan, &resp.Diagnostics)
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	if err != nil {
		waitError(&resp.Diagnostics, "the VM", err)
	}
}

func (r *vmResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state vmModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	state.ProjectID = types.StringValue(r.projectID(state.ProjectID, &resp.Diagnostics))
	if resp.Diagnostics.HasError() {
		return
	}
	var vm client.VM
	err := r.client().Get(ctx, vmPath(&state), &vm)
	if client.IsNotFound(err) || (err == nil && vm.State == "deleted") {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "Reading the VM", err)
		return
	}
	userData := state.UserData
	state.fromAPI(&vm)
	state.UserData = userData
	r.readRootDisk(ctx, &state, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *vmResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state vmModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	d, ds := plan.Timeouts.Update(ctx, 20*time.Minute)
	ctx, cancel := withTimeout(ctx, d, ds, &resp.Diagnostics)
	defer cancel()
	c := r.client()
	p := vmPath(&state)
	fail := func(what string, err error) {
		apiError(&resp.Diagnostics, what, err)
	}
	waitVM := func(ready func(client.VM) bool) bool {
		if _, err := client.Wait(ctx, c, p, ready); err != nil {
			waitError(&resp.Diagnostics, "the VM", err)
			return false
		}
		return true
	}

	if patch := tagsPatch(ctx, plan.projectMeta, state.projectMeta, &resp.Diagnostics); patch != nil {
		if err := c.Patch(ctx, p, patch, nil); err != nil {
			fail("Updating the VM", err)
			return
		}
		if !waitVM(nil) {
			return
		}
	}

	if !plan.RootDiskGiB.IsUnknown() && !plan.RootDiskGiB.IsNull() && !plan.RootDiskGiB.Equal(state.RootDiskGiB) && !state.BootVolumeID.IsNull() {
		volPath := client.ProjectPath(state.ProjectID.ValueString(), "volumes", state.BootVolumeID.ValueString())
		if err := c.Post(ctx, volPath+"/resize", map[string]int64{"size_gib": plan.RootDiskGiB.ValueInt64()}, nil); err != nil {
			fail("Growing the boot volume", err)
			return
		}
		if _, err := client.Wait[client.Volume](ctx, c, volPath, nil); err != nil {
			waitError(&resp.Diagnostics, "the boot volume", err)
			return
		}
	}

	if !plan.SecurityGroupIDs.IsUnknown() && !plan.SecurityGroupIDs.Equal(state.SecurityGroupIDs) {
		groups := stringsFromSet(ctx, plan.SecurityGroupIDs, &resp.Diagnostics)
		if err := c.Put(ctx, p+"/security-groups", map[string][]string{"security_group_ids": groups}, nil); err != nil {
			fail("Setting the VM's security groups", err)
			return
		}
		if !waitVM(nil) {
			return
		}
	}

	want := plan.PowerState.ValueString()
	stoppedForResize := false
	if !plan.InstanceType.Equal(state.InstanceType) {
		var vm client.VM
		if err := c.Get(ctx, p, &vm); err != nil {
			fail("Reading the VM", err)
			return
		}
		if vm.PowerState != "stopped" {
			if err := c.Post(ctx, p+"/stop", nil, nil); err != nil {
				fail("Stopping the VM to resize it", err)
				return
			}
			if !waitVM(powerIs("stopped")) {
				return
			}
			stoppedForResize = true
		}
		err := c.Post(ctx, p+"/resize", map[string]string{"instance_type": plan.InstanceType.ValueString()}, nil)
		if err != nil {
			fail(fmt.Sprintf("Resizing the VM to %s", plan.InstanceType.ValueString()), err)
			return
		}
		if !waitVM(nil) {
			return
		}
	}

	var vm client.VM
	if err := c.Get(ctx, p, &vm); err != nil {
		fail("Reading the VM", err)
		return
	}
	powerChange := stoppedForResize || !plan.PowerState.Equal(state.PowerState) || vm.DesiredPowerState != want
	if powerChange && vm.PowerState != want {
		action := "start"
		if want == "stopped" {
			action = "stop"
		}
		if err := c.Post(ctx, p+"/"+action, nil, nil); err != nil {
			fail(fmt.Sprintf("Asking the VM to %s", action), err)
			return
		}
		if !waitVM(powerIs(want)) {
			return
		}
	}

	if err := c.Get(ctx, p, &vm); err != nil {
		fail("Reading the VM", err)
		return
	}
	plan.fromAPI(&vm)
	r.readRootDisk(ctx, &plan, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *vmResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state vmModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	d, ds := state.Timeouts.Delete(ctx, 15*time.Minute)
	ctx, cancel := withTimeout(ctx, d, ds, &resp.Diagnostics)
	defer cancel()
	if err := deleteAndWait[client.VM](ctx, r.client(), vmPath(&state)); err != nil {
		apiError(&resp.Diagnostics, "Deleting the VM", err)
	}
}
