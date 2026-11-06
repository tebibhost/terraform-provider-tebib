package provider

import (
	"context"
	"fmt"
	"net/url"

	"github.com/hashicorp/terraform-plugin-framework-validators/datasourcevalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/tebibhost/terraform-provider-tebib/internal/client"
)

func dsProjectID() schema.StringAttribute {
	return schema.StringAttribute{
		Optional: true, Computed: true,
		MarkdownDescription: "Project ID. Default: the provider's `project`.",
	}
}

func dsString(desc string) schema.StringAttribute {
	return schema.StringAttribute{Computed: true, MarkdownDescription: desc}
}

func dsTags() schema.MapAttribute {
	return schema.MapAttribute{ElementType: types.StringType, Computed: true, MarkdownDescription: "Tags."}
}

// -- tebib_project --------------------------------------------------------------------------

type projectDataSource struct{ base }

type projectDataModel struct {
	ID             types.String `tfsdk:"id"`
	Name           types.String `tfsdk:"name"`
	OrganizationID types.String `tfsdk:"organization_id"`
	TRN            types.String `tfsdk:"trn"`
	Description    types.String `tfsdk:"description"`
	Tags           types.Map    `tfsdk:"tags"`
	State          types.String `tfsdk:"state"`
	CreatedAt      types.String `tfsdk:"created_at"`
}

func newProjectDataSource() datasource.DataSource { return &projectDataSource{} }

func (d *projectDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_project"
}

func (d *projectDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Looks up a project by ID or by name. With neither, it reads the provider's `project`.",
		Attributes: map[string]schema.Attribute{
			"id":              schema.StringAttribute{Optional: true, Computed: true, MarkdownDescription: "Project ID."},
			"name":            schema.StringAttribute{Optional: true, Computed: true, MarkdownDescription: "Project name (looked up in the organization)."},
			"organization_id": schema.StringAttribute{Optional: true, Computed: true, MarkdownDescription: "Organization to search by name. Default: the provider's."},
			"trn":             dsString("The project's TRN."),
			"description":     dsString("Description."),
			"tags":            dsTags(),
			"state":           dsString("Lifecycle state."),
			"created_at":      dsString("When it was created."),
		},
	}
}

func (d *projectDataSource) ConfigValidators(context.Context) []datasource.ConfigValidator {
	return []datasource.ConfigValidator{
		datasourcevalidator.Conflicting(path.MatchRoot("id"), path.MatchRoot("name")),
	}
}

func (d *projectDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.configure(req.ProviderData, &resp.Diagnostics)
}

func (d *projectDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var m projectDataModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var p client.Project
	if name := m.Name.ValueString(); name != "" {
		org := d.orgID(m.OrganizationID, &resp.Diagnostics)
		if resp.Diagnostics.HasError() {
			return
		}
		found, err := client.Find(ctx, d.client(), client.OrgPath(org, "projects"), nil,
			func(x client.Project) bool { return x.Name == name })
		if err != nil {
			apiError(&resp.Diagnostics, "Listing projects", err)
			return
		}
		if found == nil {
			resp.Diagnostics.AddError("Project not found", fmt.Sprintf("No project named %q in %s.", name, org))
			return
		}
		p = *found
	} else {
		id := m.ID.ValueString()
		if id == "" {
			id = d.data.project
		}
		if id == "" {
			resp.Diagnostics.AddError("No project", "Set id or name, or project in the provider block.")
			return
		}
		if err := d.client().Get(ctx, projectPath(id), &p); err != nil {
			apiError(&resp.Diagnostics, "Reading the project", err)
			return
		}
	}
	m.ID = types.StringValue(p.ID)
	m.Name = types.StringValue(p.Name)
	m.OrganizationID = types.StringValue(p.OrganizationID)
	m.TRN = types.StringValue(p.TRN)
	m.Description = types.StringValue(p.Description)
	m.Tags = tagsValue(p.Tags)
	m.State = types.StringValue(p.State)
	m.CreatedAt = types.StringValue(p.CreatedAt)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

// -- tebib_images ---------------------------------------------------------------------------

type imagesDataSource struct{ base }

type imagesDataModel struct {
	ProjectID types.String `tfsdk:"project_id"`
	OSFamily  types.String `tfsdk:"os_family"`
	Names     types.List   `tfsdk:"names"`
	Images    types.List   `tfsdk:"images"`
}

var imageAttrTypes = map[string]attr.Type{
	"name": types.StringType, "display_name": types.StringType, "os_family": types.StringType,
	"default_user": types.StringType, "min_disk_gib": types.Int64Type,
	"min_memory_mib": types.Int64Type, "min_vcpus": types.Int64Type,
}

func newImagesDataSource() datasource.DataSource { return &imagesDataSource{} }

func (d *imagesDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_images"
}

func (d *imagesDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "The operating system images VMs can be created from.",
		Attributes: map[string]schema.Attribute{
			"project_id": dsProjectID(),
			"os_family":  schema.StringAttribute{Optional: true, MarkdownDescription: "Only images of this family, e.g. `ubuntu`."},
			"names":      schema.ListAttribute{ElementType: types.StringType, Computed: true, MarkdownDescription: "Image names, for `tebib_vm.image`."},
			"images": schema.ListNestedAttribute{
				Computed: true, MarkdownDescription: "The images.",
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"name":           dsString("Name, for `tebib_vm.image`."),
					"display_name":   dsString("Human name, e.g. `Ubuntu 24.04 LTS`."),
					"os_family":      dsString("OS family."),
					"default_user":   dsString("User that gets the VM's SSH keys."),
					"min_disk_gib":   schema.Int64Attribute{Computed: true, MarkdownDescription: "Smallest boot volume."},
					"min_memory_mib": schema.Int64Attribute{Computed: true, MarkdownDescription: "Least memory."},
					"min_vcpus":      schema.Int64Attribute{Computed: true, MarkdownDescription: "Fewest vCPUs."},
				}},
			},
		},
	}
}

func (d *imagesDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.configure(req.ProviderData, &resp.Diagnostics)
}

func (d *imagesDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var m imagesDataModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	project := d.projectID(m.ProjectID, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	images, err := client.GetList[client.Image](ctx, d.client(), client.ProjectPath(project, "images"), nil)
	if err != nil {
		apiError(&resp.Diagnostics, "Listing images", err)
		return
	}
	var names []string
	var objs []attr.Value
	for _, i := range images {
		if f := m.OSFamily.ValueString(); f != "" && i.OSFamily != f {
			continue
		}
		names = append(names, i.Name)
		objs = append(objs, types.ObjectValueMust(imageAttrTypes, map[string]attr.Value{
			"name": types.StringValue(i.Name), "display_name": types.StringValue(i.DisplayName),
			"os_family": types.StringValue(i.OSFamily), "default_user": types.StringValue(i.DefaultUser),
			"min_disk_gib": types.Int64Value(i.MinDiskGiB), "min_memory_mib": types.Int64Value(i.MinMemoryMiB),
			"min_vcpus": types.Int64Value(i.MinVCPUs),
		}))
	}
	m.ProjectID = types.StringValue(project)
	m.Names = stringListValue(names)
	m.Images = types.ListValueMust(types.ObjectType{AttrTypes: imageAttrTypes}, objs)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

// -- tebib_instance_types -------------------------------------------------------------------

type instanceTypesDataSource struct{ base }

type instanceTypesDataModel struct {
	ProjectID     types.String `tfsdk:"project_id"`
	Family        types.String `tfsdk:"family"`
	MinVCPUs      types.Int64  `tfsdk:"min_vcpus"`
	MinMemoryMiB  types.Int64  `tfsdk:"min_memory_mib"`
	Names         types.List   `tfsdk:"names"`
	InstanceTypes types.List   `tfsdk:"instance_types"`
}

var instanceTypeAttrTypes = map[string]attr.Type{
	"name": types.StringType, "description": types.StringType, "family": types.StringType,
	"vcpus": types.Int64Type, "memory_mib": types.Int64Type,
}

func newInstanceTypesDataSource() datasource.DataSource { return &instanceTypesDataSource{} }

func (d *instanceTypesDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_instance_types"
}

func (d *instanceTypesDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "VM sizes (instance types), smallest first as the API lists them. " +
			"See `tebib_prices` for what they cost.",
		Attributes: map[string]schema.Attribute{
			"project_id":     dsProjectID(),
			"family":         schema.StringAttribute{Optional: true, MarkdownDescription: "Only this family."},
			"min_vcpus":      schema.Int64Attribute{Optional: true, MarkdownDescription: "Only types with at least this many vCPUs."},
			"min_memory_mib": schema.Int64Attribute{Optional: true, MarkdownDescription: "Only types with at least this much memory."},
			"names":          schema.ListAttribute{ElementType: types.StringType, Computed: true, MarkdownDescription: "Names, for `tebib_vm.instance_type`."},
			"instance_types": schema.ListNestedAttribute{
				Computed: true, MarkdownDescription: "The instance types.",
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"name":        dsString("Name, for `tebib_vm.instance_type`."),
					"description": dsString("Description."),
					"family":      dsString("Family."),
					"vcpus":       schema.Int64Attribute{Computed: true, MarkdownDescription: "vCPUs."},
					"memory_mib":  schema.Int64Attribute{Computed: true, MarkdownDescription: "Memory in MiB."},
				}},
			},
		},
	}
}

func (d *instanceTypesDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.configure(req.ProviderData, &resp.Diagnostics)
}

func (d *instanceTypesDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var m instanceTypesDataModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	project := d.projectID(m.ProjectID, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	items, err := client.GetList[client.InstanceType](ctx, d.client(), client.ProjectPath(project, "instance-types"), nil)
	if err != nil {
		apiError(&resp.Diagnostics, "Listing instance types", err)
		return
	}
	var names []string
	var objs []attr.Value
	for _, t := range items {
		if f := m.Family.ValueString(); f != "" && t.Family != f {
			continue
		}
		if t.VCPUs < m.MinVCPUs.ValueInt64() || t.MemoryMiB < m.MinMemoryMiB.ValueInt64() {
			continue
		}
		names = append(names, t.Name)
		objs = append(objs, types.ObjectValueMust(instanceTypeAttrTypes, map[string]attr.Value{
			"name": types.StringValue(t.Name), "description": types.StringValue(t.Description),
			"family": types.StringValue(t.Family), "vcpus": types.Int64Value(t.VCPUs),
			"memory_mib": types.Int64Value(t.MemoryMiB),
		}))
	}
	m.ProjectID = types.StringValue(project)
	m.Names = stringListValue(names)
	m.InstanceTypes = types.ListValueMust(types.ObjectType{AttrTypes: instanceTypeAttrTypes}, objs)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

// -- tebib_prices ---------------------------------------------------------------------------

type pricesDataSource struct{ base }

type pricesDataModel struct {
	Region           types.String `tfsdk:"region"`
	Currency         types.String `tfsdk:"currency"`
	At               types.String `tfsdk:"at"`
	HoursPerMonth    types.Int64  `tfsdk:"hours_per_month"`
	Prices           types.List   `tfsdk:"prices"`
	Scheduled        types.List   `tfsdk:"scheduled"`
	EgressAllowances types.List   `tfsdk:"egress_allowances"`
	ByMeter          types.Map    `tfsdk:"monthly_usd_by_meter"`
}

var priceAttrTypes = map[string]attr.Type{
	"meter": types.StringType, "unit": types.StringType, "unit_price_usd": types.StringType,
	"unit_price_micro": types.Int64Type, "monthly_usd": types.StringType, "note": types.StringType,
	"effective_from": types.StringType,
}

var allowanceAttrTypes = map[string]attr.Type{
	"instance_type": types.StringType, "gb_per_month": types.Int64Type, "effective_from": types.StringType,
}

func newPricesDataSource() datasource.DataSource { return &pricesDataSource{} }

func (d *pricesDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_prices"
}

func (d *pricesDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	priceObj := schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
		"meter":            dsString("What is metered, e.g. `vm.b1.small` or `volume.gib`."),
		"unit":             dsString("Billing unit, e.g. `hour`."),
		"unit_price_usd":   dsString("Price per unit in US dollars, as a decimal string."),
		"unit_price_micro": schema.Int64Attribute{Computed: true, MarkdownDescription: "Price per unit in millionths of a dollar."},
		"monthly_usd":      dsString("Price for a month of use, when it has one."),
		"note":             dsString("Note."),
		"effective_from":   dsString("When the price takes effect."),
	}}
	resp.Schema = schema.Schema{
		MarkdownDescription: "The public price list of the endpoint's region, in US dollars.",
		Attributes: map[string]schema.Attribute{
			"region":          dsString("Region the prices are for."),
			"currency":        dsString("Currency (`USD`)."),
			"at":              dsString("Time the list is for."),
			"hours_per_month": schema.Int64Attribute{Computed: true, MarkdownDescription: "Hours in a billing month."},
			"prices":          schema.ListNestedAttribute{Computed: true, NestedObject: priceObj, MarkdownDescription: "Prices in force."},
			"scheduled":       schema.ListNestedAttribute{Computed: true, NestedObject: priceObj, MarkdownDescription: "Announced future prices."},
			"egress_allowances": schema.ListNestedAttribute{
				Computed: true, MarkdownDescription: "Free egress per instance type.",
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"instance_type":  dsString("Instance type."),
					"gb_per_month":   schema.Int64Attribute{Computed: true, MarkdownDescription: "Free GB a month."},
					"effective_from": dsString("When it takes effect."),
				}},
			},
			"monthly_usd_by_meter": schema.MapAttribute{
				ElementType: types.StringType, Computed: true,
				MarkdownDescription: "Monthly price by meter, for meters that have one, e.g. `data.tebib_prices.p.monthly_usd_by_meter[\"vm.b1.small\"]`.",
			},
		},
	}
}

func (d *pricesDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.configure(req.ProviderData, &resp.Diagnostics)
}

func priceValues(prices []client.Price) types.List {
	objs := make([]attr.Value, 0, len(prices))
	for _, p := range prices {
		objs = append(objs, types.ObjectValueMust(priceAttrTypes, map[string]attr.Value{
			"meter": types.StringValue(p.Meter), "unit": types.StringValue(p.Unit),
			"unit_price_usd": types.StringValue(p.UnitPriceUSD), "unit_price_micro": types.Int64Value(p.UnitPriceMicro),
			"monthly_usd": optString(p.MonthlyUSD), "note": types.StringValue(p.Note),
			"effective_from": types.StringValue(p.EffectiveFrom),
		}))
	}
	return types.ListValueMust(types.ObjectType{AttrTypes: priceAttrTypes}, objs)
}

func (d *pricesDataSource) Read(ctx context.Context, _ datasource.ReadRequest, resp *datasource.ReadResponse) {
	var pl client.PriceList
	if err := d.client().Get(ctx, "/prices", &pl); err != nil {
		apiError(&resp.Diagnostics, "Reading prices", err)
		return
	}
	allow := make([]attr.Value, 0, len(pl.EgressAllowances))
	for _, a := range pl.EgressAllowances {
		allow = append(allow, types.ObjectValueMust(allowanceAttrTypes, map[string]attr.Value{
			"instance_type": types.StringValue(a.InstanceType), "gb_per_month": types.Int64Value(a.GBPerMonth),
			"effective_from": types.StringValue(a.EffectiveFrom),
		}))
	}
	byMeter := map[string]attr.Value{}
	for _, p := range pl.Prices {
		if p.MonthlyUSD != nil {
			byMeter[p.Meter] = types.StringValue(*p.MonthlyUSD)
		}
	}
	m := pricesDataModel{
		Region: types.StringValue(pl.Region), Currency: types.StringValue(pl.Currency),
		At: types.StringValue(pl.At), HoursPerMonth: types.Int64Value(pl.HoursPerMonth),
		Prices: priceValues(pl.Prices), Scheduled: priceValues(pl.Scheduled),
		EgressAllowances: types.ListValueMust(types.ObjectType{AttrTypes: allowanceAttrTypes}, allow),
		ByMeter:          types.MapValueMust(types.StringType, byMeter),
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

// -- tebib_vpc, tebib_subnet, tebib_security_group ------------------------------------------

// lookup finds one project resource by id, by name, or the default one.
func lookup[T client.ProjectResource](ctx context.Context, c *client.Client, project, collection string,
	q url.Values, id, name string, wantDefault bool, isDefault func(T) bool, noun string,
) (*T, error) {
	if id != "" {
		var obj T
		if err := c.Get(ctx, client.ProjectPath(project, collection, id), &obj); err != nil {
			return nil, err
		}
		return &obj, nil
	}
	found, err := client.Find(ctx, c, client.ProjectPath(project, collection), q, func(x T) bool {
		if name != "" && x.GetMeta().Name != name {
			return false
		}
		return !wantDefault || isDefault(x)
	})
	if err != nil {
		return nil, err
	}
	if found == nil {
		return nil, fmt.Errorf("no %s matches in project %s", noun, project)
	}
	return found, nil
}

func lookupAttrs(noun string) map[string]schema.Attribute {
	return map[string]schema.Attribute{
		"project_id": dsProjectID(),
		"id":         schema.StringAttribute{Optional: true, Computed: true, MarkdownDescription: noun + " ID."},
		"name":       schema.StringAttribute{Optional: true, Computed: true, MarkdownDescription: "Name to look up."},
		"default": schema.BoolAttribute{Optional: true, Computed: true,
			MarkdownDescription: "Look up the default " + noun + " (every project has one)."},
		"trn":         dsString("TRN."),
		"description": dsString("Description."),
		"tags":        dsTags(),
		"state":       dsString("Lifecycle state."),
	}
}

func lookupValidators() []datasource.ConfigValidator {
	return []datasource.ConfigValidator{
		datasourcevalidator.AtLeastOneOf(path.MatchRoot("id"), path.MatchRoot("name"), path.MatchRoot("default")),
		datasourcevalidator.Conflicting(path.MatchRoot("id"), path.MatchRoot("name")),
	}
}

type lookupMeta struct {
	ProjectID   types.String `tfsdk:"project_id"`
	ID          types.String `tfsdk:"id"`
	Name        types.String `tfsdk:"name"`
	Default     types.Bool   `tfsdk:"default"`
	TRN         types.String `tfsdk:"trn"`
	Description types.String `tfsdk:"description"`
	Tags        types.Map    `tfsdk:"tags"`
	State       types.String `tfsdk:"state"`
}

func (l *lookupMeta) fill(m client.Meta, isDefault bool) {
	l.ProjectID = types.StringValue(m.ProjectID)
	l.ID = types.StringValue(m.ID)
	l.Name = types.StringValue(m.Name)
	l.Default = types.BoolValue(isDefault)
	l.TRN = types.StringValue(m.TRN)
	l.Description = types.StringValue(m.Description)
	l.Tags = tagsValue(m.Tags)
	l.State = types.StringValue(m.State)
}

type vpcDataSource struct{ base }

type vpcDataModel struct {
	lookupMeta
	CIDR     types.String `tfsdk:"cidr"`
	IPv6CIDR types.String `tfsdk:"ipv6_cidr"`
}

func newVpcDataSource() datasource.DataSource { return &vpcDataSource{} }

func (d *vpcDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_vpc"
}

func (d *vpcDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Looks up a VPC by ID, by name, or the project's default VPC (`default = true`).",
		Attributes: mergeDS(lookupAttrs("VPC"), map[string]schema.Attribute{
			"cidr":      dsString("IPv4 range."),
			"ipv6_cidr": dsString("IPv6 range."),
		}),
	}
}

func (d *vpcDataSource) ConfigValidators(context.Context) []datasource.ConfigValidator {
	return lookupValidators()
}

func (d *vpcDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.configure(req.ProviderData, &resp.Diagnostics)
}

func (d *vpcDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var m vpcDataModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	project := d.projectID(m.ProjectID, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	v, err := lookup(ctx, d.client(), project, "vpcs", nil, m.ID.ValueString(), m.Name.ValueString(),
		m.Default.ValueBool(), func(x client.Vpc) bool { return x.IsDefault }, "VPC")
	if err != nil {
		apiError(&resp.Diagnostics, "Looking up the VPC", err)
		return
	}
	m.fill(v.Meta, v.IsDefault)
	m.CIDR = types.StringValue(v.CIDR)
	m.IPv6CIDR = types.StringValue(v.IPv6CIDR)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

type subnetDataSource struct{ base }

type subnetDataModel struct {
	lookupMeta
	VpcID    types.String `tfsdk:"vpc_id"`
	CIDR     types.String `tfsdk:"cidr"`
	IPv6CIDR types.String `tfsdk:"ipv6_cidr"`
	Gateway  types.String `tfsdk:"gateway"`
}

func newSubnetDataSource() datasource.DataSource { return &subnetDataSource{} }

func (d *subnetDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_subnet"
}

func (d *subnetDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Looks up a subnet by ID, by name, or the project's default subnet (`default = true`).",
		Attributes: mergeDS(lookupAttrs("subnet"), map[string]schema.Attribute{
			"vpc_id":    schema.StringAttribute{Optional: true, Computed: true, MarkdownDescription: "Only look in this VPC."},
			"cidr":      dsString("IPv4 range."),
			"ipv6_cidr": dsString("IPv6 range."),
			"gateway":   dsString("IPv4 gateway."),
		}),
	}
}

func (d *subnetDataSource) ConfigValidators(context.Context) []datasource.ConfigValidator {
	return lookupValidators()
}

func (d *subnetDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.configure(req.ProviderData, &resp.Diagnostics)
}

func (d *subnetDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var m subnetDataModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	project := d.projectID(m.ProjectID, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	q := url.Values{}
	if v := m.VpcID.ValueString(); v != "" {
		q.Set("vpc_id", v)
	}
	s, err := lookup(ctx, d.client(), project, "subnets", q, m.ID.ValueString(), m.Name.ValueString(),
		m.Default.ValueBool(), func(x client.Subnet) bool { return x.IsDefault }, "subnet")
	if err != nil {
		apiError(&resp.Diagnostics, "Looking up the subnet", err)
		return
	}
	m.fill(s.Meta, s.IsDefault)
	m.VpcID = types.StringValue(s.VpcID)
	m.CIDR = types.StringValue(s.CIDR)
	m.IPv6CIDR = types.StringValue(s.IPv6CIDR)
	m.Gateway = types.StringValue(s.Gateway)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

type securityGroupDataSource struct{ base }

type securityGroupDataModel struct {
	lookupMeta
	VpcID types.String `tfsdk:"vpc_id"`
}

func newSecurityGroupDataSource() datasource.DataSource { return &securityGroupDataSource{} }

func (d *securityGroupDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_security_group"
}

func (d *securityGroupDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Looks up a security group by ID, by name, or a VPC's default group (`default = true`, with `vpc_id`).",
		Attributes: mergeDS(lookupAttrs("security group"), map[string]schema.Attribute{
			"vpc_id": schema.StringAttribute{Optional: true, Computed: true, MarkdownDescription: "Only look in this VPC."},
		}),
	}
}

func (d *securityGroupDataSource) ConfigValidators(context.Context) []datasource.ConfigValidator {
	return lookupValidators()
}

func (d *securityGroupDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.configure(req.ProviderData, &resp.Diagnostics)
}

func (d *securityGroupDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var m securityGroupDataModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	project := d.projectID(m.ProjectID, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	q := url.Values{}
	if v := m.VpcID.ValueString(); v != "" {
		q.Set("vpc_id", v)
	}
	g, err := lookup(ctx, d.client(), project, "security-groups", q, m.ID.ValueString(), m.Name.ValueString(),
		m.Default.ValueBool(), func(x client.SecurityGroup) bool { return x.IsDefault }, "security group")
	if err != nil {
		apiError(&resp.Diagnostics, "Looking up the security group", err)
		return
	}
	m.fill(g.Meta, g.IsDefault)
	m.VpcID = types.StringValue(g.VpcID)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

func mergeDS(maps ...map[string]schema.Attribute) map[string]schema.Attribute {
	out := map[string]schema.Attribute{}
	for _, m := range maps {
		for k, v := range m {
			out[k] = v
		}
	}
	return out
}
