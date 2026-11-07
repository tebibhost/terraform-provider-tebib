package provider

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64default"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/tebibhost/terraform-provider-tebib/internal/client"
)

// sameDomain is true when two domain spellings name the same domain (case, final dot).
func sameDomain(a, b string) bool {
	norm := func(s string) string { return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(s)), ".") }
	return norm(a) == norm(b)
}

// -- tebib_dns_zone -------------------------------------------------------------------------

type dnsZoneResource struct{ base }

type dnsZoneModel struct {
	projectMeta
	Domain             types.String   `tfsdk:"domain"`
	Status             types.String   `tfsdk:"status"`
	Nameservers        types.List     `tfsdk:"nameservers"`
	Served             types.Bool     `tfsdk:"served"`
	Serial             types.Int64    `tfsdk:"serial"`
	DelegationOK       types.Bool     `tfsdk:"delegation_ok"`
	VerificationState  types.String   `tfsdk:"verification_state"`
	VerificationMethod types.String   `tfsdk:"verification_method"`
	VerificationName   types.String   `tfsdk:"verification_txt_name"`
	VerificationValue  types.String   `tfsdk:"verification_txt_value"`
	Timeouts           timeouts.Value `tfsdk:"timeouts"`
}

func newDNSZoneResource() resource.Resource { return &dnsZoneResource{} }

func (r *dnsZoneResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_dns_zone"
}

func (r *dnsZoneResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	attrs := metaAttrs("DNS zones", "Zone ID (`zone-...`).")
	attrs["name"] = optionalComputedReplace("Name, unique among the project's zones. Default: the domain. " +
		"Changing it replaces the zone.")
	resp.Schema = schema.Schema{
		MarkdownDescription: "A hosted DNS zone for one domain. Our nameservers only answer for " +
			"it once you prove you control the domain: delegate it to `nameservers` at your " +
			"registrar, or publish the TXT record `verification_txt_name` = " +
			"`verification_txt_value` at your current DNS provider. Creating the zone doesn't wait " +
			"for that: `state` stays `creating` and `status` `pending_verification` until then, " +
			"and records can be added meanwhile. Deleting it stops serving the domain and deletes its records.",
		Attributes: mergeAttrs(attrs, map[string]schema.Attribute{
			"domain": schema.StringAttribute{
				Required: true,
				MarkdownDescription: "The domain, e.g. `example.com` (non-ASCII names are stored as " +
					"punycode). Changing it replaces the zone.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"status": volatileString("`pending_verification` (not served yet), `active`, `updating`, " +
				"`error` (another account serves the domain, or publishing keeps failing) or `deleting`."),
			"nameservers": schema.ListAttribute{
				ElementType: types.StringType, Computed: true,
				MarkdownDescription: "Delegate the domain to these at your registrar.",
			},
			"served":                 volatileBool("Whether our nameservers answer for the zone."),
			"serial":                 volatileInt("The zone's SOA serial, once served."),
			"delegation_ok":          volatileBool("Whether the domain's delegation points at our nameservers (null: not checked yet)."),
			"verification_state":     volatileString("`pending`, `verified` or `conflict`."),
			"verification_method":    volatileString("How the domain was proved: `ns`, `txt` or `staff`."),
			"verification_txt_name":  computedString("Name of the TXT record that proves control of the domain."),
			"verification_txt_value": computedString("Value of that TXT record."),
		}),
		Blocks: map[string]schema.Block{
			"timeouts": timeouts.Block(ctx, timeouts.Opts{Create: true, Update: true, Delete: true}),
		},
	}
}

func (r *dnsZoneResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.configure(req.ProviderData, &resp.Diagnostics)
}

func (r *dnsZoneResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	importProjectScoped(ctx, req, resp)
}

func zonePath(project, id string) string { return client.ProjectPath(project, "dns", "zones", id) }

// zoneSettled: the platform is done with the zone for now. A zone waiting for its domain to
// be verified stays "creating" (for days, if nobody delegates the domain), so that counts too.
func zoneSettled(z client.DNSZone) bool {
	return !client.IsTransitional(z.State) || (z.State == "creating" && z.Status == "pending_verification")
}

func (m *dnsZoneModel) fromAPI(z *client.DNSZone) {
	m.projectMeta.fromAPI(z.Meta)
	if m.Domain.IsNull() || m.Domain.IsUnknown() || !sameDomain(m.Domain.ValueString(), z.Domain) {
		m.Domain = types.StringValue(z.Domain)
	}
	m.Status = types.StringValue(z.Status)
	m.Nameservers = stringListValue(z.Nameservers)
	m.Served = types.BoolValue(z.Served)
	m.Serial = optInt(z.Serial)
	m.DelegationOK = types.BoolPointerValue(z.DelegationOK)
	m.VerificationState = types.StringValue(z.Verification.State)
	m.VerificationMethod = optString(z.Verification.Method)
	m.VerificationName = types.StringValue(z.Verification.TXTName)
	m.VerificationValue = types.StringValue(z.Verification.TXTValue)
}

func (r *dnsZoneResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan dnsZoneModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	project := r.projectID(plan.ProjectID, &resp.Diagnostics)
	d, ds := plan.Timeouts.Create(ctx, 5*time.Minute)
	ctx, cancel := withTimeout(ctx, d, ds, &resp.Diagnostics)
	defer cancel()
	body := client.DNSZoneIn{
		Domain: plan.Domain.ValueString(), Name: strPtr(plan.Name),
		Description: plan.Description.ValueString(), Tags: tagsFrom(ctx, plan.Tags, &resp.Diagnostics),
	}
	if resp.Diagnostics.HasError() {
		return
	}
	var z client.DNSZone
	if err := r.client().Create(ctx, client.ProjectPath(project, "dns", "zones"), body, &z); err != nil {
		apiError(&resp.Diagnostics, "Creating the DNS zone", err)
		return
	}
	plan.fromAPI(&z)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	settled, err := client.WaitFor(ctx, r.client(), zonePath(project, z.ID), zoneSettled)
	if settled != nil {
		plan.fromAPI(settled)
		resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	}
	if err != nil {
		waitError(&resp.Diagnostics, "the DNS zone", err)
	}
}

func (r *dnsZoneResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state dnsZoneModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	project := r.projectID(state.ProjectID, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	var z client.DNSZone
	err := r.client().Get(ctx, zonePath(project, state.ID.ValueString()), &z)
	if client.IsNotFound(err) || (err == nil && z.State == "deleted") {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "Reading the DNS zone", err)
		return
	}
	state.fromAPI(&z)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *dnsZoneResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state dnsZoneModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	d, ds := plan.Timeouts.Update(ctx, 5*time.Minute)
	ctx, cancel := withTimeout(ctx, d, ds, &resp.Diagnostics)
	defer cancel()
	p := zonePath(state.ProjectID.ValueString(), state.ID.ValueString())
	if patch := tagsPatch(ctx, plan.projectMeta, state.projectMeta, &resp.Diagnostics); patch != nil {
		if err := r.client().Patch(ctx, p, patch, nil); err != nil {
			apiError(&resp.Diagnostics, "Updating the DNS zone", err)
			return
		}
	}
	settled, err := client.WaitFor(ctx, r.client(), p, zoneSettled)
	if err != nil {
		waitError(&resp.Diagnostics, "the DNS zone", err)
		return
	}
	plan.fromAPI(settled)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *dnsZoneResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state dnsZoneModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	d, ds := state.Timeouts.Delete(ctx, 10*time.Minute)
	ctx, cancel := withTimeout(ctx, d, ds, &resp.Diagnostics)
	defer cancel()
	if err := deleteAndWait[client.DNSZone](ctx, r.client(), zonePath(state.ProjectID.ValueString(), state.ID.ValueString())); err != nil {
		apiError(&resp.Diagnostics, "Deleting the DNS zone", err)
	}
}

// -- tebib_dns_record -----------------------------------------------------------------------

type dnsRecordResource struct{ base }

type dnsRecordModel struct {
	ID              types.String   `tfsdk:"id"`
	ProjectID       types.String   `tfsdk:"project_id"`
	ZoneID          types.String   `tfsdk:"zone_id"`
	Name            types.String   `tfsdk:"name"`
	Type            types.String   `tfsdk:"type"`
	TTL             types.Int64    `tfsdk:"ttl"`
	Values          types.List     `tfsdk:"values"`
	CanonicalValues types.List     `tfsdk:"canonical_values"`
	Description     types.String   `tfsdk:"description"`
	FQDN            types.String   `tfsdk:"fqdn"`
	Timeouts        timeouts.Value `tfsdk:"timeouts"`
}

func newDNSRecordResource() resource.Resource { return &dnsRecordResource{} }

func (r *dnsRecordResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_dns_record"
}

func (r *dnsRecordResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "A record set in a hosted DNS zone: every value of one name and type " +
			"(so all of a name's A records are one `tebib_dns_record`). Values, TTL and " +
			"description change in place; a new name or type replaces it.",
		Attributes: map[string]schema.Attribute{
			"id":         idAttr("Record set ID (`rec-...`)."),
			"project_id": projectIDAttr(),
			"zone_id":    replaceString("Zone ID (`zone-...`). Changing it replaces the record set.", true),
			"name": schema.StringAttribute{
				Optional: true, Computed: true, Default: stringdefault.StaticString("@"),
				MarkdownDescription: "Name relative to the zone (`www`, `_dmarc`, `*`); `@` (default) " +
					"is the apex. Changing it replaces the record set.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"type": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "`A`, `AAAA`, `CNAME`, `MX`, `TXT`, `SRV`, `CAA` or `NS`. Changing it replaces the record set.",
				Validators:          []validator.String{stringvalidator.OneOf("A", "AAAA", "CNAME", "MX", "TXT", "SRV", "CAA", "NS")},
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"ttl": schema.Int64Attribute{
				Optional: true, Computed: true, Default: int64default.StaticInt64(3600),
				MarkdownDescription: "Time to live in seconds (60-86400). Default: 3600.",
				Validators:          []validator.Int64{int64validator.Between(60, 86400)},
			},
			"values": schema.ListAttribute{
				ElementType: types.StringType, Required: true,
				MarkdownDescription: "The values in presentation format, one per record (1-100): " +
					"`192.0.2.1`, `10 mail.example.com.`, `v=spf1 -all`, `0 issue \"letsencrypt.org\"`. " +
					"A CNAME has exactly one.",
				Validators: []validator.List{listvalidator.SizeBetween(1, 100)},
			},
			"canonical_values": schema.ListAttribute{
				ElementType: types.StringType, Computed: true,
				MarkdownDescription: "The values as the nameservers serve them (names fully qualified, TXT quoted).",
			},
			"description": schema.StringAttribute{
				Optional: true, Computed: true, Default: stringdefault.StaticString(""),
				MarkdownDescription: "Description, at most 255 characters. Default: empty.",
			},
			"fqdn": computedString("The record set's full name, with the final dot."),
		},
		Blocks: map[string]schema.Block{
			"timeouts": timeouts.Block(ctx, timeouts.Opts{Create: true, Update: true, Delete: true}),
		},
	}
}

func (r *dnsRecordResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.configure(req.ProviderData, &resp.Diagnostics)
}

// ImportState accepts <zone_id>/<record_id> or <project_id>/<zone_id>/<record_id>.
func (r *dnsRecordResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.Split(req.ID, "/")
	if len(parts) == 3 {
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("project_id"), parts[0])...)
		parts = parts[1:]
	}
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		resp.Diagnostics.AddError("Invalid import ID",
			fmt.Sprintf("Expected <zone_id>/<record_id> or <project_id>/<zone_id>/<record_id>, got %q.", req.ID))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("zone_id"), parts[0])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), parts[1])...)
}

func (m *dnsRecordModel) path() string {
	return client.ProjectPath(m.ProjectID.ValueString(), "dns", "zones", m.ZoneID.ValueString(), "records", m.ID.ValueString())
}

// fromAPI copies an answer. The API canonicalizes names and values (`www.example.com` ->
// `www`, `mail.example.com` -> `mail.example.com.`): the user's spelling is kept while the
// canonical form is unchanged, so only real drift shows in a plan.
func (m *dnsRecordModel) fromAPI(rec *client.DNSRecord) {
	canonical := stringListValue(rec.Values)
	if m.Values.IsNull() || m.Values.IsUnknown() || !m.CanonicalValues.Equal(canonical) {
		m.Values = canonical
	}
	m.CanonicalValues = canonical
	if m.Name.IsNull() || m.Name.IsUnknown() || (!m.FQDN.IsNull() && !m.FQDN.IsUnknown() && m.FQDN.ValueString() != rec.FQDN) {
		m.Name = types.StringValue(rec.Name)
	}
	m.ID = types.StringValue(rec.ID)
	m.ZoneID = types.StringValue(rec.ZoneID)
	m.Type = types.StringValue(rec.Type)
	m.TTL = types.Int64Value(rec.TTL)
	m.Description = types.StringValue(rec.Description)
	m.FQDN = types.StringValue(rec.FQDN)
}

func (r *dnsRecordResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan dnsRecordModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	plan.ProjectID = types.StringValue(r.projectID(plan.ProjectID, &resp.Diagnostics))
	d, ds := plan.Timeouts.Create(ctx, 5*time.Minute)
	ctx, cancel := withTimeout(ctx, d, ds, &resp.Diagnostics)
	defer cancel()
	body := client.DNSRecordIn{
		Name: plan.Name.ValueString(), Type: plan.Type.ValueString(), TTL: plan.TTL.ValueInt64(),
		Values: stringsFromList(ctx, plan.Values, &resp.Diagnostics), Description: plan.Description.ValueString(),
	}
	if resp.Diagnostics.HasError() {
		return
	}
	var rec client.DNSRecord
	p := client.ProjectPath(plan.ProjectID.ValueString(), "dns", "zones", plan.ZoneID.ValueString(), "records")
	if err := r.client().Create(ctx, p, body, &rec); err != nil {
		apiError(&resp.Diagnostics, "Creating the DNS record", err)
		return
	}
	plan.CanonicalValues = types.ListNull(types.StringType)
	plan.FQDN = types.StringNull()
	values := plan.Values
	plan.fromAPI(&rec)
	plan.Values = values // the user's spelling of what was just created
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *dnsRecordResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state dnsRecordModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	state.ProjectID = types.StringValue(r.projectID(state.ProjectID, &resp.Diagnostics))
	if resp.Diagnostics.HasError() {
		return
	}
	var rec client.DNSRecord
	err := r.client().Get(ctx, state.path(), &rec)
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "Reading the DNS record", err)
		return
	}
	state.fromAPI(&rec)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *dnsRecordResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state dnsRecordModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	d, ds := plan.Timeouts.Update(ctx, 5*time.Minute)
	ctx, cancel := withTimeout(ctx, d, ds, &resp.Diagnostics)
	defer cancel()
	var patch client.DNSRecordPatch
	if !plan.TTL.Equal(state.TTL) {
		patch.TTL = ptr(plan.TTL.ValueInt64())
	}
	if !plan.Values.Equal(state.Values) {
		patch.Values = ptr(stringsFromList(ctx, plan.Values, &resp.Diagnostics))
	}
	if !plan.Description.Equal(state.Description) {
		patch.Description = ptr(plan.Description.ValueString())
	}
	if resp.Diagnostics.HasError() {
		return
	}
	var rec client.DNSRecord
	if err := r.client().Patch(ctx, state.path(), patch, &rec); err != nil {
		apiError(&resp.Diagnostics, "Updating the DNS record", err)
		return
	}
	values := plan.Values
	plan.CanonicalValues, plan.FQDN = state.CanonicalValues, state.FQDN
	plan.fromAPI(&rec)
	plan.Values = values
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *dnsRecordResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state dnsRecordModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	d, ds := state.Timeouts.Delete(ctx, 5*time.Minute)
	ctx, cancel := withTimeout(ctx, d, ds, &resp.Diagnostics)
	defer cancel()
	err := r.client().RetryConflict(ctx, conflictWindow, func() error {
		return r.client().Delete(ctx, state.path(), nil)
	})
	if err != nil && !client.IsNotFound(err) {
		apiError(&resp.Diagnostics, "Deleting the DNS record", err)
	}
}
