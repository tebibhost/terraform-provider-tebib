package provider

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64default"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/objectdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/setplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"

	"github.com/tebibhost/terraform-provider-tebib/internal/client"
)

// Load balancers (ADR 0040). Target groups, listeners and targets live inside the load
// balancer in the API; each is its own resource here (like security group rules), so one
// can change without touching the others. Every change answers with the whole load
// balancer, which then settles while its nodes pick the change up.

func lbPath(project, id string) string { return client.ProjectPath(project, "load-balancers", id) }

func lbProtocol(desc string) schema.StringAttribute {
	return schema.StringAttribute{
		Optional: true, Computed: true, Default: stringdefault.StaticString("tcp"),
		MarkdownDescription: desc,
		Validators:          []validator.String{stringvalidator.OneOf("tcp", "http")},
		PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
	}
}

func portAttr(desc string, replace bool) schema.Int64Attribute {
	a := schema.Int64Attribute{
		Required: true, MarkdownDescription: desc,
		Validators: []validator.Int64{int64validator.Between(1, 65535)},
	}
	if replace {
		a.PlanModifiers = []planmodifier.Int64{int64planmodifier.RequiresReplace()}
	}
	return a
}

// waitLB waits for the load balancer to settle after a change to it or to one of its parts.
func waitLB(ctx context.Context, c *client.Client, p string, diags *diag.Diagnostics) *client.LoadBalancer {
	lb, err := client.Wait[client.LoadBalancer](ctx, c, p, nil)
	if err != nil {
		waitError(diags, "the load balancer", err)
		return nil
	}
	return lb
}

// getLB reads a load balancer; nil (and no error) when it is gone.
func getLB(ctx context.Context, c *client.Client, p string, diags *diag.Diagnostics) (*client.LoadBalancer, bool) {
	var lb client.LoadBalancer
	err := c.Get(ctx, p, &lb)
	if client.IsNotFound(err) || (err == nil && lb.State == "deleted") {
		return nil, true
	}
	if err != nil {
		apiError(diags, "Reading the load balancer", err)
		return nil, false
	}
	return &lb, true
}

// splitLBImport parses "[<project_id>/]<load_balancer_id>/<id>[/<id>]" into the project (or
// "") and n IDs.
func splitLBImport(id string, n int, format string, diags *diag.Diagnostics) (string, []string) {
	parts := strings.Split(id, "/")
	project := ""
	if len(parts) == n+1 {
		project, parts = parts[0], parts[1:]
	}
	if len(parts) != n {
		diags.AddError("Invalid import ID", fmt.Sprintf("Expected %s or <project_id>/%s, got %q.", format, format, id))
		return "", nil
	}
	for _, p := range parts {
		if p == "" {
			diags.AddError("Invalid import ID", fmt.Sprintf("Expected %s or <project_id>/%s, got %q.", format, format, id))
			return "", nil
		}
	}
	return project, parts
}

// -- tebib_load_balancer --------------------------------------------------------------------

type loadBalancerResource struct{ base }

type loadBalancerModel struct {
	projectMeta
	Scheme           types.String   `tfsdk:"scheme"`
	SubnetID         types.String   `tfsdk:"subnet_id"`
	PublicIPID       types.String   `tfsdk:"public_ip_id"`
	SecurityGroupIDs types.Set      `tfsdk:"security_group_ids"`
	VpcID            types.String   `tfsdk:"vpc_id"`
	Address          types.String   `tfsdk:"address"`
	PublicAddress    types.String   `tfsdk:"public_address"`
	NodesDesired     types.Int64    `tfsdk:"nodes_desired"`
	NodesReady       types.Int64    `tfsdk:"nodes_ready"`
	Timeouts         timeouts.Value `tfsdk:"timeouts"`
}

func newLoadBalancerResource() resource.Resource { return &loadBalancerResource{} }

func (r *loadBalancerResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_load_balancer"
}

func (r *loadBalancerResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "A layer 4 (TCP) / layer 7 (HTTP) load balancer in a VPC. Add " +
			"`tebib_load_balancer_target_group`, `tebib_load_balancer_listener` and " +
			"`tebib_load_balancer_target` resources to it. A public load balancer has a public " +
			"IPv4 address; an internal one only its VPC address.",
		Attributes: mergeAttrs(metaAttrs("load balancers", "Load balancer ID (`lb-...`)."), map[string]schema.Attribute{
			"scheme": schema.StringAttribute{
				Optional: true, Computed: true, Default: stringdefault.StaticString("public"),
				MarkdownDescription: "`public` (default): a public IPv4 address; `internal`: the VPC " +
					"address only. Changing it replaces the load balancer.",
				Validators:    []validator.String{stringvalidator.OneOf("public", "internal")},
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"subnet_id": optionalComputedReplace("Subnet to place it in (`subnet-...`). Default: the " +
				"default VPC's default subnet. Changing it replaces the load balancer."),
			"public_ip_id": optionalComputedReplace("Public only: an allocated, unassociated " +
				"`tebib_public_ip` to use. Default: one is allocated and released with the load " +
				"balancer. Changing it replaces the load balancer."),
			"security_group_ids": schema.SetAttribute{
				ElementType: types.StringType, Optional: true, Computed: true,
				MarkdownDescription: "Security groups (1-5) that filter traffic to the listeners. " +
					"Default: the VPC's default security group. Changes in place.",
				Validators:    []validator.Set{setvalidator.SizeBetween(1, 5)},
				PlanModifiers: []planmodifier.Set{setplanmodifier.UseStateForUnknown()},
			},
			"vpc_id":         computedString("The VPC it is in."),
			"address":        computedString("Its address inside the VPC (from 100.68.0.0/16)."),
			"public_address": computedString("Its public IPv4 address (public load balancers)."),
			"nodes_desired":  volatileInt("Load balancer nodes it should run."),
			"nodes_ready":    volatileInt("Load balancer nodes running and ready."),
		}),
		Blocks: map[string]schema.Block{
			"timeouts": timeouts.Block(ctx, timeouts.Opts{Create: true, Update: true, Delete: true}),
		},
	}
}

func (r *loadBalancerResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.configure(req.ProviderData, &resp.Diagnostics)
}

func (r *loadBalancerResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	importProjectScoped(ctx, req, resp)
}

func (m *loadBalancerModel) fromAPI(lb *client.LoadBalancer) {
	m.projectMeta.fromAPI(lb.Meta)
	m.Scheme = types.StringValue(lb.Scheme)
	m.SubnetID = types.StringValue(lb.SubnetID)
	m.PublicIPID = optString(lb.PublicIPID)
	m.SecurityGroupIDs = stringSetValue(lb.SecurityGroupIDs)
	m.VpcID = types.StringValue(lb.VpcID)
	m.Address = optString(lb.Address)
	m.PublicAddress = optString(lb.PublicAddress)
	m.NodesDesired = types.Int64Value(lb.Nodes.Desired)
	m.NodesReady = types.Int64Value(lb.Nodes.Ready)
}

func (r *loadBalancerResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan loadBalancerModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	project := r.projectID(plan.ProjectID, &resp.Diagnostics)
	d, ds := plan.Timeouts.Create(ctx, 20*time.Minute)
	ctx, cancel := withTimeout(ctx, d, ds, &resp.Diagnostics)
	defer cancel()
	body := client.LoadBalancerIn{
		Name: plan.Name.ValueString(), Scheme: plan.Scheme.ValueString(),
		SubnetID: strPtr(plan.SubnetID), PublicIPID: strPtr(plan.PublicIPID),
		Description: plan.Description.ValueString(), Tags: tagsFrom(ctx, plan.Tags, &resp.Diagnostics),
	}
	if !plan.SecurityGroupIDs.IsNull() && !plan.SecurityGroupIDs.IsUnknown() {
		body.SecurityGroupIDs = stringsFromSet(ctx, plan.SecurityGroupIDs, &resp.Diagnostics)
	}
	if resp.Diagnostics.HasError() {
		return
	}
	var lb client.LoadBalancer
	if err := r.client().Create(ctx, client.ProjectPath(project, "load-balancers"), body, &lb); err != nil {
		apiError(&resp.Diagnostics, "Creating the load balancer", err)
		return
	}
	plan.fromAPI(&lb)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	settled, err := client.Wait[client.LoadBalancer](ctx, r.client(), lbPath(project, lb.ID), nil)
	if settled != nil {
		plan.fromAPI(settled)
		resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	}
	if err != nil {
		waitError(&resp.Diagnostics, "the load balancer", err)
	}
}

func (r *loadBalancerResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state loadBalancerModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	project := r.projectID(state.ProjectID, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	lb, ok := getLB(ctx, r.client(), lbPath(project, state.ID.ValueString()), &resp.Diagnostics)
	if !ok {
		return
	}
	if lb == nil {
		resp.State.RemoveResource(ctx)
		return
	}
	state.fromAPI(lb)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *loadBalancerResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state loadBalancerModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	d, ds := plan.Timeouts.Update(ctx, 10*time.Minute)
	ctx, cancel := withTimeout(ctx, d, ds, &resp.Diagnostics)
	defer cancel()
	c := r.client()
	p := lbPath(state.ProjectID.ValueString(), state.ID.ValueString())
	if patch := tagsPatch(ctx, plan.projectMeta, state.projectMeta, &resp.Diagnostics); patch != nil {
		if err := c.Patch(ctx, p, patch, nil); err != nil {
			apiError(&resp.Diagnostics, "Updating the load balancer", err)
			return
		}
	}
	if !plan.SecurityGroupIDs.IsUnknown() && !plan.SecurityGroupIDs.Equal(state.SecurityGroupIDs) {
		ids := stringsFromSet(ctx, plan.SecurityGroupIDs, &resp.Diagnostics)
		err := c.RetryConflict(ctx, conflictWindow, func() error {
			return c.Put(ctx, p+"/security-groups", map[string][]string{"security_group_ids": ids}, nil)
		})
		if err != nil {
			apiError(&resp.Diagnostics, "Changing the load balancer's security groups", err)
			return
		}
	}
	lb := waitLB(ctx, c, p, &resp.Diagnostics)
	if lb == nil {
		return
	}
	plan.fromAPI(lb)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *loadBalancerResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state loadBalancerModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	d, ds := state.Timeouts.Delete(ctx, 20*time.Minute)
	ctx, cancel := withTimeout(ctx, d, ds, &resp.Diagnostics)
	defer cancel()
	if err := deleteAndWait[client.LoadBalancer](ctx, r.client(), lbPath(state.ProjectID.ValueString(), state.ID.ValueString())); err != nil {
		apiError(&resp.Diagnostics, "Deleting the load balancer", err)
	}
}

// -- tebib_load_balancer_target_group -------------------------------------------------------

type targetGroupResource struct{ base }

type targetGroupModel struct {
	ID             types.String   `tfsdk:"id"`
	ProjectID      types.String   `tfsdk:"project_id"`
	LoadBalancerID types.String   `tfsdk:"load_balancer_id"`
	Name           types.String   `tfsdk:"name"`
	Protocol       types.String   `tfsdk:"protocol"`
	Port           types.Int64    `tfsdk:"port"`
	Algorithm      types.String   `tfsdk:"algorithm"`
	ProxyProtocol  types.Bool     `tfsdk:"proxy_protocol"`
	HealthCheck    types.Object   `tfsdk:"health_check"`
	Timeouts       timeouts.Value `tfsdk:"timeouts"`
}

type healthCheckModel struct {
	Protocol           types.String `tfsdk:"protocol"`
	Port               types.Int64  `tfsdk:"port"`
	Path               types.String `tfsdk:"path"`
	Matcher            types.String `tfsdk:"matcher"`
	IntervalSeconds    types.Int64  `tfsdk:"interval_seconds"`
	TimeoutSeconds     types.Int64  `tfsdk:"timeout_seconds"`
	HealthyThreshold   types.Int64  `tfsdk:"healthy_threshold"`
	UnhealthyThreshold types.Int64  `tfsdk:"unhealthy_threshold"`
}

var healthCheckTypes = map[string]attr.Type{
	"protocol": types.StringType, "port": types.Int64Type, "path": types.StringType,
	"matcher": types.StringType, "interval_seconds": types.Int64Type, "timeout_seconds": types.Int64Type,
	"healthy_threshold": types.Int64Type, "unhealthy_threshold": types.Int64Type,
}

func healthCheckValue(h client.HealthCheck) types.Object {
	return types.ObjectValueMust(healthCheckTypes, map[string]attr.Value{
		"protocol": types.StringValue(h.Protocol), "port": optInt(h.Port),
		"path": types.StringValue(h.Path), "matcher": types.StringValue(h.Matcher),
		"interval_seconds": types.Int64Value(h.IntervalSeconds), "timeout_seconds": types.Int64Value(h.TimeoutSeconds),
		"healthy_threshold": types.Int64Value(h.HealthyThreshold), "unhealthy_threshold": types.Int64Value(h.UnhealthyThreshold),
	})
}

// defaultHealthCheck is the API's default: a TCP connect to the traffic port.
var defaultHealthCheck = healthCheckValue(client.HealthCheck{
	Protocol: "tcp", Path: "/", Matcher: "200-399", IntervalSeconds: 10, TimeoutSeconds: 5,
	HealthyThreshold: 3, UnhealthyThreshold: 3,
})

func healthCheckIn(ctx context.Context, o types.Object, diags *diag.Diagnostics) *client.HealthCheckIn {
	if o.IsNull() || o.IsUnknown() {
		return nil
	}
	var h healthCheckModel
	diags.Append(o.As(ctx, &h, basetypes.ObjectAsOptions{})...)
	return &client.HealthCheckIn{
		Protocol: h.Protocol.ValueString(), Port: int64Ptr(h.Port), Path: h.Path.ValueString(),
		Matcher: h.Matcher.ValueString(), IntervalSeconds: h.IntervalSeconds.ValueInt64(),
		TimeoutSeconds: h.TimeoutSeconds.ValueInt64(), HealthyThreshold: h.HealthyThreshold.ValueInt64(),
		UnhealthyThreshold: h.UnhealthyThreshold.ValueInt64(),
	}
}

func newTargetGroupResource() resource.Resource { return &targetGroupResource{} }

func (r *targetGroupResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_load_balancer_target_group"
}

func (r *targetGroupResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	threshold := func(desc string) schema.Int64Attribute {
		return schema.Int64Attribute{
			Optional: true, Computed: true, Default: int64default.StaticInt64(3), MarkdownDescription: desc,
			Validators: []validator.Int64{int64validator.Between(2, 10)},
		}
	}
	resp.Schema = schema.Schema{
		MarkdownDescription: "A target group of a load balancer: the VMs (`tebib_load_balancer_target`) " +
			"that listeners forward to, how traffic is spread over them and how their health is checked.",
		Attributes: map[string]schema.Attribute{
			"id":               idAttr("Target group ID (`lbtg-...`)."),
			"project_id":       projectIDAttr(),
			"load_balancer_id": replaceString("Load balancer ID (`lb-...`). Changing it replaces the target group.", true),
			"name":             replaceString("Name, unique in the load balancer. Changing it replaces the target group.", true),
			"protocol":         lbProtocol("`tcp` (default) or `http`. Changing it replaces the target group."),
			"port":             portAttr("Port on the targets (1-65535), unless a target sets its own. Changes in place.", false),
			"algorithm": schema.StringAttribute{
				Optional: true, Computed: true, Default: stringdefault.StaticString("round_robin"),
				MarkdownDescription: "`round_robin` (default), `least_connections` or `source_ip`.",
				Validators:          []validator.String{stringvalidator.OneOf("round_robin", "least_connections", "source_ip")},
			},
			"proxy_protocol": schema.BoolAttribute{
				Optional: true, Computed: true, Default: booldefault.StaticBool(false),
				MarkdownDescription: "TCP only: send a PROXY protocol v2 header to the targets. Default: `false`.",
			},
			"health_check": schema.SingleNestedAttribute{
				Optional: true, Computed: true, Default: objectdefault.StaticValue(defaultHealthCheck),
				MarkdownDescription: "How target health is checked. Default: a TCP connect to the traffic port every 10 seconds.",
				Attributes: map[string]schema.Attribute{
					"protocol": schema.StringAttribute{
						Optional: true, Computed: true, Default: stringdefault.StaticString("tcp"),
						MarkdownDescription: "`tcp` (default: a connect) or `http` (a GET of `path`).",
						Validators:          []validator.String{stringvalidator.OneOf("tcp", "http")},
					},
					"port": schema.Int64Attribute{
						Optional: true, MarkdownDescription: "Port to check. Default: the traffic port.",
						Validators: []validator.Int64{int64validator.Between(1, 65535)},
					},
					"path": schema.StringAttribute{
						Optional: true, Computed: true, Default: stringdefault.StaticString("/"),
						MarkdownDescription: "HTTP checks: the path to GET. Default: `/`.",
					},
					"matcher": schema.StringAttribute{
						Optional: true, Computed: true, Default: stringdefault.StaticString("200-399"),
						MarkdownDescription: "HTTP checks: expected status codes, e.g. `200` or `200-399` (default).",
					},
					"interval_seconds": schema.Int64Attribute{
						Optional: true, Computed: true, Default: int64default.StaticInt64(10),
						MarkdownDescription: "Seconds between checks (5-300). Default: 10.",
						Validators:          []validator.Int64{int64validator.Between(5, 300)},
					},
					"timeout_seconds": schema.Int64Attribute{
						Optional: true, Computed: true, Default: int64default.StaticInt64(5),
						MarkdownDescription: "Seconds a check may take (2-120). Default: 5.",
						Validators:          []validator.Int64{int64validator.Between(2, 120)},
					},
					"healthy_threshold":   threshold("Checks in a row that make a target healthy (2-10). Default: 3."),
					"unhealthy_threshold": threshold("Checks in a row that make a target unhealthy (2-10). Default: 3."),
				},
			},
		},
		Blocks: map[string]schema.Block{
			"timeouts": timeouts.Block(ctx, timeouts.Opts{Create: true, Update: true, Delete: true}),
		},
	}
}

func (r *targetGroupResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.configure(req.ProviderData, &resp.Diagnostics)
}

// ImportState accepts [<project_id>/]<load_balancer_id>/<target_group_id>.
func (r *targetGroupResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	project, parts := splitLBImport(req.ID, 2, "<load_balancer_id>/<target_group_id>", &resp.Diagnostics)
	if parts == nil {
		return
	}
	if project != "" {
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("project_id"), project)...)
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("load_balancer_id"), parts[0])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), parts[1])...)
}

func (m *targetGroupModel) lbPath() string {
	return lbPath(m.ProjectID.ValueString(), m.LoadBalancerID.ValueString())
}

func (m *targetGroupModel) fromAPI(g *client.TargetGroup) {
	m.ID = types.StringValue(g.ID)
	m.Name = types.StringValue(g.Name)
	m.Protocol = types.StringValue(g.Protocol)
	m.Port = types.Int64Value(g.Port)
	m.Algorithm = types.StringValue(g.Algorithm)
	m.ProxyProtocol = types.BoolValue(g.ProxyProtocol)
	m.HealthCheck = healthCheckValue(g.HealthCheck)
}

func findGroup(lb *client.LoadBalancer, match func(*client.TargetGroup) bool) *client.TargetGroup {
	for i := range lb.TargetGroups {
		if match(&lb.TargetGroups[i]) {
			return &lb.TargetGroups[i]
		}
	}
	return nil
}

func (r *targetGroupResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan targetGroupModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	plan.ProjectID = types.StringValue(r.projectID(plan.ProjectID, &resp.Diagnostics))
	d, ds := plan.Timeouts.Create(ctx, 10*time.Minute)
	ctx, cancel := withTimeout(ctx, d, ds, &resp.Diagnostics)
	defer cancel()
	body := client.TargetGroupIn{
		Name: plan.Name.ValueString(), Protocol: plan.Protocol.ValueString(), Port: plan.Port.ValueInt64(),
		Algorithm: plan.Algorithm.ValueString(), ProxyProtocol: plan.ProxyProtocol.ValueBool(),
		HealthCheck: healthCheckIn(ctx, plan.HealthCheck, &resp.Diagnostics),
	}
	if resp.Diagnostics.HasError() {
		return
	}
	c := r.client()
	var lb client.LoadBalancer
	if err := c.Post(ctx, plan.lbPath()+"/target-groups", body, &lb); err != nil {
		apiError(&resp.Diagnostics, "Creating the target group", err)
		return
	}
	g := findGroup(&lb, func(g *client.TargetGroup) bool { return g.Name == plan.Name.ValueString() })
	if g == nil {
		resp.Diagnostics.AddError("Creating the target group",
			"The API accepted the target group but its answer doesn't list it. Import it with its ID.")
		return
	}
	plan.fromAPI(g)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	waitLB(ctx, c, plan.lbPath(), &resp.Diagnostics)
}

func (r *targetGroupResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state targetGroupModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	state.ProjectID = types.StringValue(r.projectID(state.ProjectID, &resp.Diagnostics))
	if resp.Diagnostics.HasError() {
		return
	}
	lb, ok := getLB(ctx, r.client(), state.lbPath(), &resp.Diagnostics)
	if !ok {
		return
	}
	var g *client.TargetGroup
	if lb != nil {
		g = findGroup(lb, func(g *client.TargetGroup) bool { return g.ID == state.ID.ValueString() })
	}
	if g == nil {
		resp.State.RemoveResource(ctx)
		return
	}
	state.fromAPI(g)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *targetGroupResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state targetGroupModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	d, ds := plan.Timeouts.Update(ctx, 10*time.Minute)
	ctx, cancel := withTimeout(ctx, d, ds, &resp.Diagnostics)
	defer cancel()
	var patch client.TargetGroupPatch
	if !plan.Port.Equal(state.Port) {
		patch.Port = ptr(plan.Port.ValueInt64())
	}
	if !plan.Algorithm.Equal(state.Algorithm) {
		patch.Algorithm = ptr(plan.Algorithm.ValueString())
	}
	if !plan.ProxyProtocol.Equal(state.ProxyProtocol) {
		patch.ProxyProtocol = ptr(plan.ProxyProtocol.ValueBool())
	}
	if !plan.HealthCheck.Equal(state.HealthCheck) {
		patch.HealthCheck = healthCheckIn(ctx, plan.HealthCheck, &resp.Diagnostics)
	}
	if resp.Diagnostics.HasError() {
		return
	}
	c := r.client()
	var lb client.LoadBalancer
	if err := c.Patch(ctx, state.lbPath()+"/target-groups/"+state.ID.ValueString(), patch, &lb); err != nil {
		apiError(&resp.Diagnostics, "Updating the target group", err)
		return
	}
	if g := findGroup(&lb, func(g *client.TargetGroup) bool { return g.ID == state.ID.ValueString() }); g != nil {
		plan.fromAPI(g)
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	waitLB(ctx, c, state.lbPath(), &resp.Diagnostics)
}

func (r *targetGroupResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state targetGroupModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	d, ds := state.Timeouts.Delete(ctx, 10*time.Minute)
	ctx, cancel := withTimeout(ctx, d, ds, &resp.Diagnostics)
	defer cancel()
	deleteLBPart(ctx, r.client(), state.lbPath(), "/target-groups/"+state.ID.ValueString(), "the target group", &resp.Diagnostics)
}

// deleteLBPart deletes a part of a load balancer. 409s are retried for a while: a listener
// that forwards to a target group may be being deleted in the same apply.
func deleteLBPart(ctx context.Context, c *client.Client, lbp, rest, what string, diags *diag.Diagnostics) {
	err := c.RetryConflict(ctx, conflictWindow, func() error { return c.Delete(ctx, lbp+rest, nil) })
	if client.IsNotFound(err) {
		return // it, or the whole load balancer, is gone
	}
	if err != nil {
		apiError(diags, "Deleting "+what, err)
		return
	}
	if _, err := client.Wait[client.LoadBalancer](ctx, c, lbp, nil); err != nil && !client.IsNotFound(err) {
		waitError(diags, "the load balancer", err)
	}
}

// -- tebib_load_balancer_listener -----------------------------------------------------------

type listenerResource struct{ base }

type listenerModel struct {
	ID             types.String   `tfsdk:"id"`
	ProjectID      types.String   `tfsdk:"project_id"`
	LoadBalancerID types.String   `tfsdk:"load_balancer_id"`
	Port           types.Int64    `tfsdk:"port"`
	Protocol       types.String   `tfsdk:"protocol"`
	TargetGroupID  types.String   `tfsdk:"target_group_id"`
	Timeouts       timeouts.Value `tfsdk:"timeouts"`
}

func newListenerResource() resource.Resource { return &listenerResource{} }

func (r *listenerResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_load_balancer_listener"
}

func (r *listenerResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "A port a load balancer accepts traffic on, and the target group it forwards to.",
		Attributes: map[string]schema.Attribute{
			"id":               idAttr("Listener ID (`lbl-...`)."),
			"project_id":       projectIDAttr(),
			"load_balancer_id": replaceString("Load balancer ID (`lb-...`). Changing it replaces the listener.", true),
			"port":             portAttr("Port (1-65535), one listener per port. Changing it replaces the listener.", true),
			"protocol": lbProtocol("`tcp` (default) or `http`. Changing it replaces the listener. " +
				"Its target group must use the same protocol."),
			"target_group_id": schema.StringAttribute{
				Required: true, MarkdownDescription: "Target group to forward to (`lbtg-...`). Changes in place.",
			},
		},
		Blocks: map[string]schema.Block{
			"timeouts": timeouts.Block(ctx, timeouts.Opts{Create: true, Update: true, Delete: true}),
		},
	}
}

func (r *listenerResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.configure(req.ProviderData, &resp.Diagnostics)
}

// ImportState accepts [<project_id>/]<load_balancer_id>/<listener_id>.
func (r *listenerResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	project, parts := splitLBImport(req.ID, 2, "<load_balancer_id>/<listener_id>", &resp.Diagnostics)
	if parts == nil {
		return
	}
	if project != "" {
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("project_id"), project)...)
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("load_balancer_id"), parts[0])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), parts[1])...)
}

func (m *listenerModel) lbPath() string {
	return lbPath(m.ProjectID.ValueString(), m.LoadBalancerID.ValueString())
}

func (m *listenerModel) fromAPI(l *client.Listener) {
	m.ID = types.StringValue(l.ID)
	m.Port = types.Int64Value(l.Port)
	m.Protocol = types.StringValue(l.Protocol)
	m.TargetGroupID = types.StringValue(l.TargetGroupID)
}

func findListener(lb *client.LoadBalancer, match func(*client.Listener) bool) *client.Listener {
	for i := range lb.Listeners {
		if match(&lb.Listeners[i]) {
			return &lb.Listeners[i]
		}
	}
	return nil
}

func (r *listenerResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan listenerModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	plan.ProjectID = types.StringValue(r.projectID(plan.ProjectID, &resp.Diagnostics))
	d, ds := plan.Timeouts.Create(ctx, 10*time.Minute)
	ctx, cancel := withTimeout(ctx, d, ds, &resp.Diagnostics)
	defer cancel()
	if resp.Diagnostics.HasError() {
		return
	}
	c := r.client()
	body := client.ListenerIn{Port: plan.Port.ValueInt64(), Protocol: plan.Protocol.ValueString(), TargetGroupID: plan.TargetGroupID.ValueString()}
	var lb client.LoadBalancer
	if err := c.Post(ctx, plan.lbPath()+"/listeners", body, &lb); err != nil {
		apiError(&resp.Diagnostics, "Creating the listener", err)
		return
	}
	l := findListener(&lb, func(l *client.Listener) bool { return l.Port == plan.Port.ValueInt64() })
	if l == nil {
		resp.Diagnostics.AddError("Creating the listener",
			"The API accepted the listener but its answer doesn't list it. Import it with its ID.")
		return
	}
	plan.fromAPI(l)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	waitLB(ctx, c, plan.lbPath(), &resp.Diagnostics)
}

func (r *listenerResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state listenerModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	state.ProjectID = types.StringValue(r.projectID(state.ProjectID, &resp.Diagnostics))
	if resp.Diagnostics.HasError() {
		return
	}
	lb, ok := getLB(ctx, r.client(), state.lbPath(), &resp.Diagnostics)
	if !ok {
		return
	}
	var l *client.Listener
	if lb != nil {
		l = findListener(lb, func(l *client.Listener) bool { return l.ID == state.ID.ValueString() })
	}
	if l == nil {
		resp.State.RemoveResource(ctx)
		return
	}
	state.fromAPI(l)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *listenerResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state listenerModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	d, ds := plan.Timeouts.Update(ctx, 10*time.Minute)
	ctx, cancel := withTimeout(ctx, d, ds, &resp.Diagnostics)
	defer cancel()
	c := r.client()
	plan.ID, plan.ProjectID = state.ID, state.ProjectID
	if !plan.TargetGroupID.Equal(state.TargetGroupID) {
		var lb client.LoadBalancer
		body := map[string]string{"target_group_id": plan.TargetGroupID.ValueString()}
		if err := c.Patch(ctx, state.lbPath()+"/listeners/"+state.ID.ValueString(), body, &lb); err != nil {
			apiError(&resp.Diagnostics, "Updating the listener", err)
			return
		}
		if l := findListener(&lb, func(l *client.Listener) bool { return l.ID == state.ID.ValueString() }); l != nil {
			plan.fromAPI(l)
		}
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	waitLB(ctx, c, state.lbPath(), &resp.Diagnostics)
}

func (r *listenerResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state listenerModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	d, ds := state.Timeouts.Delete(ctx, 10*time.Minute)
	ctx, cancel := withTimeout(ctx, d, ds, &resp.Diagnostics)
	defer cancel()
	deleteLBPart(ctx, r.client(), state.lbPath(), "/listeners/"+state.ID.ValueString(), "the listener", &resp.Diagnostics)
}

// -- tebib_load_balancer_target -------------------------------------------------------------

type lbTargetResource struct{ base }

type lbTargetModel struct {
	ID             types.String   `tfsdk:"id"`
	ProjectID      types.String   `tfsdk:"project_id"`
	LoadBalancerID types.String   `tfsdk:"load_balancer_id"`
	TargetGroupID  types.String   `tfsdk:"target_group_id"`
	VMID           types.String   `tfsdk:"vm_id"`
	Port           types.Int64    `tfsdk:"port"`
	EffectivePort  types.Int64    `tfsdk:"effective_port"`
	Health         types.String   `tfsdk:"health"`
	HealthReason   types.String   `tfsdk:"health_reason"`
	Timeouts       timeouts.Value `tfsdk:"timeouts"`
}

func newLBTargetResource() resource.Resource { return &lbTargetResource{} }

func (r *lbTargetResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_load_balancer_target"
}

func (r *lbTargetResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "A VM in a load balancer's target group. The VM must be in the load " +
			"balancer's VPC, and its security groups must let the load balancer reach the port. " +
			"Any change replaces the target.",
		Attributes: map[string]schema.Attribute{
			"id":               idAttr("Target ID (`lbt-...`)."),
			"project_id":       projectIDAttr(),
			"load_balancer_id": replaceString("Load balancer ID (`lb-...`).", true),
			"target_group_id":  replaceString("Target group ID (`lbtg-...`).", true),
			"vm_id":            replaceString("VM ID (`vm-...`).", true),
			"port": schema.Int64Attribute{
				Optional: true, MarkdownDescription: "Port on the VM (1-65535). Default: the target group's port.",
				Validators:    []validator.Int64{int64validator.Between(1, 65535)},
				PlanModifiers: []planmodifier.Int64{int64planmodifier.RequiresReplace()},
			},
			"effective_port": volatileInt("The port traffic goes to."),
			"health":         volatileString("`healthy`, `unhealthy`, `initial`, `unused` (no listener forwards to the group) or `unavailable`."),
			"health_reason":  volatileString("Why it isn't healthy."),
		},
		Blocks: map[string]schema.Block{
			"timeouts": timeouts.Block(ctx, timeouts.Opts{Create: true, Delete: true}),
		},
	}
}

func (r *lbTargetResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.configure(req.ProviderData, &resp.Diagnostics)
}

// ImportState accepts [<project_id>/]<load_balancer_id>/<target_group_id>/<target_id>.
func (r *lbTargetResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	project, parts := splitLBImport(req.ID, 3, "<load_balancer_id>/<target_group_id>/<target_id>", &resp.Diagnostics)
	if parts == nil {
		return
	}
	if project != "" {
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("project_id"), project)...)
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("load_balancer_id"), parts[0])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("target_group_id"), parts[1])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), parts[2])...)
}

func (m *lbTargetModel) lbPath() string {
	return lbPath(m.ProjectID.ValueString(), m.LoadBalancerID.ValueString())
}

func (m *lbTargetModel) groupPath() string {
	return m.lbPath() + "/target-groups/" + m.TargetGroupID.ValueString()
}

func (m *lbTargetModel) fromAPI(t *client.LBTarget) {
	m.ID = types.StringValue(t.ID)
	m.VMID = types.StringValue(t.VMID)
	m.Port = optInt(t.Port)
	m.EffectivePort = types.Int64Value(t.EffectivePort)
	m.Health = types.StringValue(t.Health)
	m.HealthReason = types.StringValue(t.HealthReason)
}

func findTarget(lb *client.LoadBalancer, groupID string, match func(*client.LBTarget) bool) *client.LBTarget {
	g := findGroup(lb, func(g *client.TargetGroup) bool { return g.ID == groupID })
	if g == nil {
		return nil
	}
	for i := range g.Targets {
		if match(&g.Targets[i]) {
			return &g.Targets[i]
		}
	}
	return nil
}

func (r *lbTargetResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan lbTargetModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	plan.ProjectID = types.StringValue(r.projectID(plan.ProjectID, &resp.Diagnostics))
	d, ds := plan.Timeouts.Create(ctx, 10*time.Minute)
	ctx, cancel := withTimeout(ctx, d, ds, &resp.Diagnostics)
	defer cancel()
	if resp.Diagnostics.HasError() {
		return
	}
	c := r.client()
	var lb client.LoadBalancer
	body := client.TargetIn{VMID: plan.VMID.ValueString(), Port: int64Ptr(plan.Port)}
	if err := c.Post(ctx, plan.groupPath()+"/targets", body, &lb); err != nil {
		apiError(&resp.Diagnostics, "Adding the target", err)
		return
	}
	match := func(t *client.LBTarget) bool {
		return t.VMID == plan.VMID.ValueString() && ((t.Port == nil && plan.Port.IsNull()) ||
			(t.Port != nil && !plan.Port.IsNull() && *t.Port == plan.Port.ValueInt64()))
	}
	t := findTarget(&lb, plan.TargetGroupID.ValueString(), match)
	if t == nil {
		resp.Diagnostics.AddError("Adding the target",
			"The API accepted the target but its answer doesn't list it. Import it with its ID.")
		return
	}
	plan.fromAPI(t)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	if settled := waitLB(ctx, c, plan.lbPath(), &resp.Diagnostics); settled != nil {
		if t := findTarget(settled, plan.TargetGroupID.ValueString(), func(t *client.LBTarget) bool { return t.ID == plan.ID.ValueString() }); t != nil {
			plan.fromAPI(t)
			resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
		}
	}
}

func (r *lbTargetResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state lbTargetModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	state.ProjectID = types.StringValue(r.projectID(state.ProjectID, &resp.Diagnostics))
	if resp.Diagnostics.HasError() {
		return
	}
	lb, ok := getLB(ctx, r.client(), state.lbPath(), &resp.Diagnostics)
	if !ok {
		return
	}
	var t *client.LBTarget
	if lb != nil {
		t = findTarget(lb, state.TargetGroupID.ValueString(), func(t *client.LBTarget) bool { return t.ID == state.ID.ValueString() })
	}
	if t == nil {
		resp.State.RemoveResource(ctx)
		return
	}
	state.fromAPI(t)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *lbTargetResource) Update(_ context.Context, _ resource.UpdateRequest, resp *resource.UpdateResponse) {
	resp.Diagnostics.AddWarning("Nothing to update", "Load balancer targets change by replacement.")
}

func (r *lbTargetResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state lbTargetModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	d, ds := state.Timeouts.Delete(ctx, 10*time.Minute)
	ctx, cancel := withTimeout(ctx, d, ds, &resp.Diagnostics)
	defer cancel()
	deleteLBPart(ctx, r.client(), state.lbPath(), "/target-groups/"+state.TargetGroupID.ValueString()+"/targets/"+state.ID.ValueString(), "the target", &resp.Diagnostics)
}
