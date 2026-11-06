package provider

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/tebibhost/terraform-provider-tebib/internal/client"
)

type sgRuleResource struct{ base }

type sgRuleModel struct {
	ID              types.String   `tfsdk:"id"`
	ProjectID       types.String   `tfsdk:"project_id"`
	SecurityGroupID types.String   `tfsdk:"security_group_id"`
	Direction       types.String   `tfsdk:"direction"`
	Protocol        types.String   `tfsdk:"protocol"`
	PortFrom        types.Int64    `tfsdk:"port_from"`
	PortTo          types.Int64    `tfsdk:"port_to"`
	RemoteCIDR      types.String   `tfsdk:"remote_cidr"`
	RemoteGroupID   types.String   `tfsdk:"remote_group_id"`
	IPVersion       types.String   `tfsdk:"ip_version"`
	Description     types.String   `tfsdk:"description"`
	Timeouts        timeouts.Value `tfsdk:"timeouts"`
}

func newSecurityGroupRuleResource() resource.Resource { return &sgRuleResource{} }

func (r *sgRuleResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_security_group_rule"
}

func (r *sgRuleResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	replaceInt := []planmodifier.Int64{int64planmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{
		MarkdownDescription: "One rule of a security group. Rules allow traffic; everything inbound " +
			"that no rule allows is denied. Rules can't be changed in place: any change replaces the rule.",
		Attributes: map[string]schema.Attribute{
			"id":                idAttr("Rule ID (`sgr-...`)."),
			"project_id":        projectIDAttr(),
			"security_group_id": replaceString("Security group ID (`sg-...`).", true),
			"direction": schema.StringAttribute{
				Optional: true, Computed: true, Default: stringdefault.StaticString("ingress"),
				MarkdownDescription: "`ingress` (inbound) or `egress` (outbound). Default: `ingress`.",
				Validators:          []validator.String{stringvalidator.OneOf("ingress", "egress")},
				PlanModifiers:       replace,
			},
			"protocol": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "`tcp`, `udp`, `icmp` or `all`.",
				Validators:          []validator.String{stringvalidator.OneOf("tcp", "udp", "icmp", "all")},
				PlanModifiers:       replace,
			},
			"port_from": schema.Int64Attribute{
				Optional:            true,
				MarkdownDescription: "First port (tcp and udp only). Leave out for all ports.",
				Validators:          []validator.Int64{int64validator.Between(1, 65535)},
				PlanModifiers:       replaceInt,
			},
			"port_to": schema.Int64Attribute{
				Optional: true, Computed: true,
				MarkdownDescription: "Last port of a range. Default: `port_from`.",
				Validators: []validator.Int64{
					int64validator.Between(1, 65535),
					int64validator.AlsoRequires(path.MatchRoot("port_from")),
				},
				PlanModifiers: []planmodifier.Int64{int64planmodifier.RequiresReplaceIfConfigured(), int64planmodifier.UseStateForUnknown()},
			},
			"remote_cidr": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Remote range, e.g. `0.0.0.0/0` or `2001:db8::/32`. Give this or `remote_group_id`.",
				Validators: []validator.String{
					stringvalidator.ExactlyOneOf(path.MatchRoot("remote_cidr"), path.MatchRoot("remote_group_id")),
				},
				PlanModifiers: replace,
			},
			"remote_group_id": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Remote security group (same VPC) instead of a range. Needs `ip_version`.",
				Validators:          []validator.String{stringvalidator.AlsoRequires(path.MatchRoot("ip_version"))},
				PlanModifiers:       replace,
			},
			"ip_version": schema.StringAttribute{
				Optional: true, Computed: true,
				MarkdownDescription: "`ipv4` or `ipv6`. Taken from `remote_cidr` when not set.",
				Validators:          []validator.String{stringvalidator.OneOf("ipv4", "ipv6")},
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplaceIfConfigured(), stringplanmodifier.UseStateForUnknown()},
			},
			"description": schema.StringAttribute{
				Optional: true, Computed: true, Default: stringdefault.StaticString(""),
				MarkdownDescription: "Why the rule exists.",
				PlanModifiers:       replace,
			},
		},
		Blocks: map[string]schema.Block{
			"timeouts": timeouts.Block(ctx, timeouts.Opts{Create: true, Delete: true}),
		},
	}
}

func (r *sgRuleResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var m sgRuleModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || m.Protocol.IsUnknown() {
		return
	}
	p := m.Protocol.ValueString()
	if !m.PortFrom.IsNull() && p != "tcp" && p != "udp" {
		resp.Diagnostics.AddAttributeError(path.Root("port_from"), "Ports need tcp or udp",
			fmt.Sprintf("Protocol %q has no ports; leave port_from and port_to out.", p))
	}
}

func (r *sgRuleResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.configure(req.ProviderData, &resp.Diagnostics)
}

// normalizeCIDR spells a range the way the API does: the network address and a prefix.
func normalizeCIDR(s string) string {
	if !strings.Contains(s, "/") {
		if ip := net.ParseIP(s); ip != nil {
			if ip.To4() != nil {
				s += "/32"
			} else {
				s += "/128"
			}
		}
	}
	_, n, err := net.ParseCIDR(s)
	if err != nil {
		return s
	}
	return n.String()
}

func ipVersionOf(cidr string) string {
	if strings.Contains(cidr, ":") {
		return "ipv6"
	}
	return "ipv4"
}

func (m *sgRuleModel) matches(rule client.Rule) bool {
	if rule.Direction != m.Direction.ValueString() || rule.Protocol != m.Protocol.ValueString() {
		return false
	}
	from := int64Ptr(m.PortFrom)
	to := int64Ptr(m.PortTo)
	if from != nil && to == nil {
		to = from
	}
	if !eqInt(rule.PortFrom, from) || !eqInt(rule.PortTo, to) {
		return false
	}
	if g := strPtr(m.RemoteGroupID); g != nil {
		return rule.RemoteGroupID != nil && *rule.RemoteGroupID == *g && rule.IPVersion == m.IPVersion.ValueString()
	}
	cidr := normalizeCIDR(m.RemoteCIDR.ValueString())
	return rule.RemoteCIDR != nil && normalizeCIDR(*rule.RemoteCIDR) == cidr
}

func eqInt(a, b *int64) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func (m *sgRuleModel) fromAPI(rule client.Rule) {
	m.ID = types.StringValue(rule.ID)
	m.Direction = types.StringValue(rule.Direction)
	m.Protocol = types.StringValue(rule.Protocol)
	m.PortFrom = optInt(rule.PortFrom)
	m.PortTo = optInt(rule.PortTo)
	if rule.RemoteGroupID != nil && *rule.RemoteGroupID != "" {
		m.RemoteGroupID = types.StringValue(*rule.RemoteGroupID)
		m.RemoteCIDR = types.StringNull()
	} else {
		m.RemoteGroupID = types.StringNull()
		// Keep the user's spelling (10.0.0.1/24) when it means the same range.
		if rule.RemoteCIDR != nil && (m.RemoteCIDR.IsNull() || normalizeCIDR(m.RemoteCIDR.ValueString()) != normalizeCIDR(*rule.RemoteCIDR)) {
			m.RemoteCIDR = types.StringValue(*rule.RemoteCIDR)
		}
	}
	m.IPVersion = types.StringValue(rule.IPVersion)
	m.Description = types.StringValue(rule.Description)
}

func (r *sgRuleResource) groupPath(m *sgRuleModel) string {
	return client.ProjectPath(m.ProjectID.ValueString(), "security-groups", m.SecurityGroupID.ValueString())
}

func (r *sgRuleResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan sgRuleModel
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
	body := client.RuleIn{
		Direction:     plan.Direction.ValueString(),
		Protocol:      plan.Protocol.ValueString(),
		PortFrom:      int64Ptr(plan.PortFrom),
		PortTo:        int64Ptr(plan.PortTo),
		RemoteCIDR:    strPtr(plan.RemoteCIDR),
		RemoteGroupID: strPtr(plan.RemoteGroupID),
		IPVersion:     strPtr(plan.IPVersion),
		Description:   plan.Description.ValueString(),
	}
	if plan.IPVersion.IsUnknown() || plan.IPVersion.IsNull() {
		if body.RemoteCIDR != nil {
			plan.IPVersion = types.StringValue(ipVersionOf(*body.RemoteCIDR))
		}
	}
	var group client.SecurityGroup
	if err := r.client().Post(ctx, r.groupPath(&plan)+"/rules", body, &group); err != nil {
		apiError(&resp.Diagnostics, "Adding the security group rule", err)
		return
	}
	var found *client.Rule
	for i := range group.Rules {
		if plan.matches(group.Rules[i]) {
			found = &group.Rules[i]
			break
		}
	}
	if found == nil {
		resp.Diagnostics.AddError("Adding the security group rule",
			"The API accepted the rule but its answer doesn't list it. Run terraform refresh, or import the rule.")
		return
	}
	plan.fromAPI(*found)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	if _, err := client.Wait[client.SecurityGroup](ctx, r.client(), r.groupPath(&plan), nil); err != nil {
		waitError(&resp.Diagnostics, "the security group", err)
	}
}

func (r *sgRuleResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state sgRuleModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	state.ProjectID = types.StringValue(r.projectID(state.ProjectID, &resp.Diagnostics))
	if resp.Diagnostics.HasError() {
		return
	}
	var group client.SecurityGroup
	err := r.client().Get(ctx, r.groupPath(&state), &group)
	if client.IsNotFound(err) || (err == nil && group.State == "deleted") {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "Reading the security group", err)
		return
	}
	for _, rule := range group.Rules {
		if rule.ID == state.ID.ValueString() {
			state.fromAPI(rule)
			resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
			return
		}
	}
	resp.State.RemoveResource(ctx)
}

func (r *sgRuleResource) Update(_ context.Context, _ resource.UpdateRequest, resp *resource.UpdateResponse) {
	// Every argument forces replacement; only timeouts can change in place.
	resp.Diagnostics.AddWarning("Nothing to update", "Security group rules change by replacement.")
}

func (r *sgRuleResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state sgRuleModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	d, ds := state.Timeouts.Delete(ctx, 5*time.Minute)
	ctx, cancel := withTimeout(ctx, d, ds, &resp.Diagnostics)
	defer cancel()
	err := r.client().RetryConflict(ctx, conflictWindow, func() error {
		return r.client().Delete(ctx, r.groupPath(&state)+"/rules/"+state.ID.ValueString(), nil)
	})
	if client.IsNotFound(err) {
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "Removing the security group rule", err)
		return
	}
	if _, err := client.Wait[client.SecurityGroup](ctx, r.client(), r.groupPath(&state), nil); err != nil && !client.IsNotFound(err) {
		waitError(&resp.Diagnostics, "the security group", err)
	}
}

// ImportState accepts <security_group_id>/<rule_id> or <project_id>/<security_group_id>/<rule_id>.
func (r *sgRuleResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.Split(req.ID, "/")
	if len(parts) == 3 {
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("project_id"), parts[0])...)
		parts = parts[1:]
	}
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		resp.Diagnostics.AddError("Invalid import ID",
			fmt.Sprintf("Expected <security_group_id>/<rule_id> or <project_id>/<security_group_id>/<rule_id>, got %q.", req.ID))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("security_group_id"), parts[0])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), parts[1])...)
}
