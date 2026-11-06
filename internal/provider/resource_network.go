package provider

import (
	"context"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/tebibhost/terraform-provider-tebib/internal/client"
)

// -- tebib_vpc ------------------------------------------------------------------------------

type vpcModel struct {
	projectMeta
	CIDR          types.String   `tfsdk:"cidr"`
	IPv6CIDR      types.String   `tfsdk:"ipv6_cidr"`
	DNSServer     types.String   `tfsdk:"dns_server"`
	IsDefault     types.Bool     `tfsdk:"is_default"`
	HasNatGateway types.Bool     `tfsdk:"has_nat_gateway"`
	Timeouts      timeouts.Value `tfsdk:"timeouts"`
}

func (m *vpcModel) meta() *projectMeta            { return &m.projectMeta }
func (m *vpcModel) timeoutsValue() timeouts.Value { return m.Timeouts }

func newVpcResource() resource.Resource {
	return &simpleResource[client.Vpc, vpcModel, *vpcModel]{spec: simpleSpec[client.Vpc, vpcModel]{
		typeName: "vpc", noun: "VPC", plural: "VPCs", collection: "vpcs",
		summary: "A VPC: a private network, isolated from other projects. It gets a routed IPv6 " +
			"range and its own DNS server. Every project also has a default VPC (see the " +
			"`tebib_vpc` data source).",
		idDesc:   "VPC ID (`vpc-...`).",
		timeouts: [3]time.Duration{10 * time.Minute, 10 * time.Minute, 10 * time.Minute},
		attrs: map[string]schema.Attribute{
			"cidr":            replaceString("Private IPv4 range, e.g. `10.1.0.0/16`. Changing it replaces the VPC.", true),
			"ipv6_cidr":       computedString("The VPC's IPv6 range."),
			"dns_server":      computedString("The VPC's DNS server address."),
			"is_default":      stableBool("Whether this is the project's default VPC."),
			"has_nat_gateway": volatileBool("Whether the VPC has a NAT gateway."),
		},
		body: func(ctx context.Context, m *vpcModel, diags *diag.Diagnostics) any {
			return client.VpcIn{
				Name: m.Name.ValueString(), CIDR: m.CIDR.ValueString(),
				Description: m.Description.ValueString(), Tags: tagsFrom(ctx, m.Tags, diags),
			}
		},
		read: func(m *vpcModel, a *client.Vpc) {
			m.CIDR = types.StringValue(a.CIDR)
			m.IPv6CIDR = types.StringValue(a.IPv6CIDR)
			m.DNSServer = types.StringValue(a.DNSServer)
			m.IsDefault = types.BoolValue(a.IsDefault)
			m.HasNatGateway = types.BoolValue(a.HasNatGateway)
		},
	}}
}

// -- tebib_subnet ---------------------------------------------------------------------------

type subnetModel struct {
	projectMeta
	VpcID         types.String   `tfsdk:"vpc_id"`
	CIDR          types.String   `tfsdk:"cidr"`
	Gateway       types.String   `tfsdk:"gateway"`
	IPv6CIDR      types.String   `tfsdk:"ipv6_cidr"`
	IPv6Gateway   types.String   `tfsdk:"ipv6_gateway"`
	AvailableIPv4 types.Int64    `tfsdk:"available_ipv4"`
	IsDefault     types.Bool     `tfsdk:"is_default"`
	Timeouts      timeouts.Value `tfsdk:"timeouts"`
}

func (m *subnetModel) meta() *projectMeta            { return &m.projectMeta }
func (m *subnetModel) timeoutsValue() timeouts.Value { return m.Timeouts }

func newSubnetResource() resource.Resource {
	return &simpleResource[client.Subnet, subnetModel, *subnetModel]{spec: simpleSpec[client.Subnet, subnetModel]{
		typeName: "subnet", noun: "subnet", plural: "subnets", collection: "subnets",
		summary:  "A dual-stack subnet in a VPC. VMs attach to a subnet.",
		idDesc:   "Subnet ID (`subnet-...`).",
		timeouts: [3]time.Duration{10 * time.Minute, 10 * time.Minute, 10 * time.Minute},
		attrs: map[string]schema.Attribute{
			"vpc_id":         replaceString("VPC ID. Changing it replaces the subnet.", true),
			"cidr":           replaceString("IPv4 range inside the VPC's, e.g. `10.1.1.0/24`. Changing it replaces the subnet.", true),
			"gateway":        computedString("IPv4 gateway address."),
			"ipv6_cidr":      computedString("The subnet's IPv6 range."),
			"ipv6_gateway":   computedString("IPv6 gateway address."),
			"available_ipv4": volatileInt("Free IPv4 addresses."),
			"is_default":     stableBool("Whether this is the project's default subnet."),
		},
		body: func(ctx context.Context, m *subnetModel, diags *diag.Diagnostics) any {
			return client.SubnetIn{
				Name: m.Name.ValueString(), VpcID: m.VpcID.ValueString(), CIDR: m.CIDR.ValueString(),
				Description: m.Description.ValueString(), Tags: tagsFrom(ctx, m.Tags, diags),
			}
		},
		read: func(m *subnetModel, a *client.Subnet) {
			m.VpcID = types.StringValue(a.VpcID)
			m.CIDR = types.StringValue(a.CIDR)
			m.Gateway = types.StringValue(a.Gateway)
			m.IPv6CIDR = types.StringValue(a.IPv6CIDR)
			m.IPv6Gateway = types.StringValue(a.IPv6Gateway)
			m.AvailableIPv4 = optInt(a.AvailableIPv4)
			m.IsDefault = types.BoolValue(a.IsDefault)
		},
	}}
}

// -- tebib_security_group -------------------------------------------------------------------

type securityGroupModel struct {
	projectMeta
	VpcID          types.String   `tfsdk:"vpc_id"`
	IsDefault      types.Bool     `tfsdk:"is_default"`
	AllowSameGroup types.Bool     `tfsdk:"allow_same_group"`
	Timeouts       timeouts.Value `tfsdk:"timeouts"`
}

func (m *securityGroupModel) meta() *projectMeta            { return &m.projectMeta }
func (m *securityGroupModel) timeoutsValue() timeouts.Value { return m.Timeouts }

func newSecurityGroupResource() resource.Resource {
	return &simpleResource[client.SecurityGroup, securityGroupModel, *securityGroupModel]{spec: simpleSpec[client.SecurityGroup, securityGroupModel]{
		typeName: "security_group", noun: "security group", plural: "security groups", collection: "security-groups",
		summary: "A security group: a stateful firewall on VM network interfaces. Inbound traffic " +
			"is denied unless a rule allows it. Add rules with `tebib_security_group_rule`.",
		idDesc:   "Security group ID (`sg-...`).",
		timeouts: [3]time.Duration{10 * time.Minute, 10 * time.Minute, 10 * time.Minute},
		attrs: map[string]schema.Attribute{
			"vpc_id":           replaceString("VPC ID. Changing it replaces the group.", true),
			"is_default":       stableBool("Whether this is the VPC's default group."),
			"allow_same_group": stableBool("Whether members of the group can reach each other freely."),
		},
		body: func(ctx context.Context, m *securityGroupModel, diags *diag.Diagnostics) any {
			return client.SecurityGroupIn{
				Name: m.Name.ValueString(), VpcID: m.VpcID.ValueString(),
				Description: m.Description.ValueString(), Tags: tagsFrom(ctx, m.Tags, diags),
			}
		},
		read: func(m *securityGroupModel, a *client.SecurityGroup) {
			m.VpcID = types.StringValue(a.VpcID)
			m.IsDefault = types.BoolValue(a.IsDefault)
			m.AllowSameGroup = types.BoolValue(a.AllowSameGroup)
		},
	}}
}

// -- tebib_nat_gateway ----------------------------------------------------------------------

type natGatewayModel struct {
	projectMeta
	SubnetID      types.String   `tfsdk:"subnet_id"`
	PublicIPID    types.String   `tfsdk:"public_ip_id"`
	VpcID         types.String   `tfsdk:"vpc_id"`
	LanIP         types.String   `tfsdk:"lan_ip"`
	PublicAddress types.String   `tfsdk:"public_address"`
	Timeouts      timeouts.Value `tfsdk:"timeouts"`
}

func (m *natGatewayModel) meta() *projectMeta            { return &m.projectMeta }
func (m *natGatewayModel) timeoutsValue() timeouts.Value { return m.Timeouts }

func newNatGatewayResource() resource.Resource {
	return &simpleResource[client.NatGateway, natGatewayModel, *natGatewayModel]{spec: simpleSpec[client.NatGateway, natGatewayModel]{
		typeName: "nat_gateway", noun: "NAT gateway", plural: "NAT gateways", collection: "nat-gateways",
		summary: "A NAT gateway: outbound internet for VMs in a VPC that have no public IP. " +
			"Give it a public IP with `public_ip_id` or a `tebib_public_ip_association`.",
		idDesc:   "NAT gateway ID (`nat-...`).",
		timeouts: [3]time.Duration{15 * time.Minute, 10 * time.Minute, 15 * time.Minute},
		attrs: map[string]schema.Attribute{
			"subnet_id": replaceString("Subnet the gateway sits in. Changing it replaces the gateway.", true),
			"public_ip_id": optionalComputedReplace("Public IP for outbound traffic, set at creation. " +
				"Leave it out to associate one later with `tebib_public_ip_association`."),
			"vpc_id":         computedString("The gateway's VPC."),
			"lan_ip":         computedString("The gateway's address in the subnet."),
			"public_address": volatileString("Its public IPv4 address, once it has one."),
		},
		body: func(ctx context.Context, m *natGatewayModel, diags *diag.Diagnostics) any {
			return client.NatGatewayIn{
				Name: m.Name.ValueString(), SubnetID: m.SubnetID.ValueString(), PublicIPID: strPtr(m.PublicIPID),
				Description: m.Description.ValueString(), Tags: tagsFrom(ctx, m.Tags, diags),
			}
		},
		read: func(m *natGatewayModel, a *client.NatGateway) {
			m.SubnetID = types.StringValue(a.SubnetID)
			m.PublicIPID = optString(a.PublicIPID)
			m.VpcID = types.StringValue(a.VpcID)
			m.LanIP = types.StringValue(a.LanIP)
			m.PublicAddress = optString(a.PublicAddress)
		},
	}}
}

// -- tebib_public_ip ------------------------------------------------------------------------

type publicIPModel struct {
	projectMeta
	Address      types.String   `tfsdk:"address"`
	Association  types.String   `tfsdk:"association"`
	VMID         types.String   `tfsdk:"vm_id"`
	NatGatewayID types.String   `tfsdk:"nat_gateway_id"`
	Timeouts     timeouts.Value `tfsdk:"timeouts"`
}

func (m *publicIPModel) meta() *projectMeta            { return &m.projectMeta }
func (m *publicIPModel) timeoutsValue() timeouts.Value { return m.Timeouts }

func newPublicIPResource() resource.Resource {
	return &simpleResource[client.PublicIP, publicIPModel, *publicIPModel]{spec: simpleSpec[client.PublicIP, publicIPModel]{
		typeName: "public_ip", noun: "public IP", plural: "public IPs", collection: "public-ips",
		summary: "A public IPv4 address allocated to the project. Point it at a VM or NAT gateway " +
			"with `tebib_public_ip_association`. Deleting it releases the address: you may not get " +
			"the same one again.",
		idDesc:   "Public IP ID (`eip-...`).",
		timeouts: [3]time.Duration{5 * time.Minute, 5 * time.Minute, 5 * time.Minute},
		attrs: map[string]schema.Attribute{
			"address":        computedString("The IPv4 address."),
			"association":    volatileString("What the address points at: `none`, `vm` or `nat-gateway`."),
			"vm_id":          volatileString("The VM it points at, if any."),
			"nat_gateway_id": volatileString("The NAT gateway that carries its traffic, if any (also set for a VM's address)."),
		},
		body: func(ctx context.Context, m *publicIPModel, diags *diag.Diagnostics) any {
			return client.PublicIPIn{
				Name: m.Name.ValueString(), Description: m.Description.ValueString(),
				Tags: tagsFrom(ctx, m.Tags, diags),
			}
		},
		read: func(m *publicIPModel, a *client.PublicIP) {
			m.Address = types.StringValue(a.Address)
			m.Association = types.StringValue(a.Association)
			m.VMID = optString(a.VMID)
			m.NatGatewayID = optString(a.NatGatewayID)
		},
	}}
}
