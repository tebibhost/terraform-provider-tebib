package provider

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-jsontypes/jsontypes"
	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64default"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/setplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/tebibhost/terraform-provider-tebib/internal/client"
)

// IAM objects live in the organization (not a project) and change synchronously: there is
// no state to wait for. Their creates aren't de-duplicated by Idempotency-Key, so they are
// sent once and not retried on 5xx (names are unique, so a lost answer shows up as a 409).

var iamNamePattern = stringvalidator.RegexMatches(
	regexpMust(`^[A-Za-z0-9+=,.@_-]{1,128}$`),
	"1-128 letters, digits and +=,.@_-",
)

func iamDescription(max int) schema.StringAttribute {
	return schema.StringAttribute{
		Optional: true, Computed: true, Default: stringdefault.StaticString(""),
		MarkdownDescription: fmt.Sprintf("Description, at most %d characters. Default: empty.", max),
		Validators:          []validator.String{stringvalidator.LengthAtMost(max)},
	}
}

// importOrgScoped accepts "<id>" (the provider's organization) or "<organization_id>/<id>".
func importOrgScoped(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.Split(req.ID, "/")
	switch {
	case len(parts) == 1 && parts[0] != "":
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), parts[0])...)
	case len(parts) == 2 && parts[0] != "" && parts[1] != "":
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("organization_id"), parts[0])...)
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), parts[1])...)
	default:
		resp.Diagnostics.AddError("Invalid import ID",
			fmt.Sprintf("Expected <id> or <organization_id>/<id>, got %q.", req.ID))
	}
}

// -- tebib_iam_service_account --------------------------------------------------------------

type serviceAccountResource struct{ base }

type serviceAccountModel struct {
	ID             types.String `tfsdk:"id"`
	OrganizationID types.String `tfsdk:"organization_id"`
	TRN            types.String `tfsdk:"trn"`
	Name           types.String `tfsdk:"name"`
	Description    types.String `tfsdk:"description"`
	Disabled       types.Bool   `tfsdk:"disabled"`
	Tags           types.Map    `tfsdk:"tags"`
	CreatedAt      types.String `tfsdk:"created_at"`
}

func newServiceAccountResource() resource.Resource { return &serviceAccountResource{} }

func (r *serviceAccountResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_iam_service_account"
}

func (r *serviceAccountResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "A service account: an IAM identity for software (CI, scripts, Terraform " +
			"itself), with its own access keys and policies. People join an organization by " +
			"invitation instead; they can't be created here.",
		Attributes: map[string]schema.Attribute{
			"id":              idAttr("Service account ID (`sa-...`)."),
			"organization_id": orgIDAttr(),
			"trn":             computedString("TRN, the `principal` of policy attachments."),
			"name":            replaceString("Name, unique in the organization. Changing it replaces the account.", true),
			"description":     iamDescription(255),
			"disabled": schema.BoolAttribute{
				Optional: true, Computed: true, Default: booldefault.StaticBool(false),
				MarkdownDescription: "A disabled account can't sign in with its keys. Default: `false`.",
			},
			"tags":       tagsAttr(),
			"created_at": computedString("When it was created (RFC 3339)."),
		},
	}
}

func (r *serviceAccountResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.configure(req.ProviderData, &resp.Diagnostics)
}

func (r *serviceAccountResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	importOrgScoped(ctx, req, resp)
}

func (m *serviceAccountModel) fromAPI(org string, a *client.ServiceAccount) {
	m.ID = types.StringValue(a.ID)
	m.OrganizationID = types.StringValue(org)
	m.TRN = types.StringValue(a.TRN)
	m.Name = types.StringValue(a.Name)
	m.Description = types.StringValue(a.Description)
	m.Disabled = types.BoolValue(a.Disabled)
	m.Tags = tagsValue(a.Tags)
	m.CreatedAt = types.StringValue(a.CreatedAt)
}

func (r *serviceAccountResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan serviceAccountModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	org := r.orgID(plan.OrganizationID, &resp.Diagnostics)
	body := client.ServiceAccountIn{
		Name: plan.Name.ValueString(), Description: plan.Description.ValueString(),
		Tags: tagsFrom(ctx, plan.Tags, &resp.Diagnostics),
	}
	if resp.Diagnostics.HasError() {
		return
	}
	var sa client.ServiceAccount
	if err := r.client().Post(ctx, client.OrgPath(org, "service-accounts"), body, &sa); err != nil {
		apiError(&resp.Diagnostics, "Creating the service account", err)
		return
	}
	if plan.Disabled.ValueBool() {
		if err := r.client().Patch(ctx, client.OrgPath(org, "service-accounts", sa.ID),
			client.ServiceAccountPatch{Disabled: ptr(true)}, &sa); err != nil {
			plan.fromAPI(org, &sa)
			resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
			apiError(&resp.Diagnostics, "Disabling the service account", err)
			return
		}
	}
	plan.fromAPI(org, &sa)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *serviceAccountResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state serviceAccountModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	org := r.orgID(state.OrganizationID, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	var sa client.ServiceAccount
	err := r.client().Get(ctx, client.OrgPath(org, "service-accounts", state.ID.ValueString()), &sa)
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "Reading the service account", err)
		return
	}
	state.fromAPI(org, &sa)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *serviceAccountResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state serviceAccountModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	org := state.OrganizationID.ValueString()
	patch := client.ServiceAccountPatch{
		Description: ptr(plan.Description.ValueString()),
		Disabled:    ptr(plan.Disabled.ValueBool()),
		Tags:        ptr(tagsFrom(ctx, plan.Tags, &resp.Diagnostics)),
	}
	var sa client.ServiceAccount
	if err := r.client().Patch(ctx, client.OrgPath(org, "service-accounts", state.ID.ValueString()), patch, &sa); err != nil {
		apiError(&resp.Diagnostics, "Updating the service account", err)
		return
	}
	plan.fromAPI(org, &sa)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *serviceAccountResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state serviceAccountModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	err := r.client().Delete(ctx, client.OrgPath(state.OrganizationID.ValueString(), "service-accounts", state.ID.ValueString()), nil)
	if err != nil && !client.IsNotFound(err) {
		apiError(&resp.Diagnostics, "Deleting the service account", err)
	}
}

// -- tebib_iam_access_key -------------------------------------------------------------------

type accessKeyResource struct{ base }

type accessKeyModel struct {
	ID               types.String `tfsdk:"id"`
	OrganizationID   types.String `tfsdk:"organization_id"`
	ServiceAccountID types.String `tfsdk:"service_account_id"`
	UserID           types.String `tfsdk:"user_id"`
	TRN              types.String `tfsdk:"trn"`
	Owner            types.String `tfsdk:"owner"`
	Description      types.String `tfsdk:"description"`
	Status           types.String `tfsdk:"status"`
	StatusReason     types.String `tfsdk:"status_reason"`
	ExpiresAt        types.String `tfsdk:"expires_at"`
	NeverExpires     types.Bool   `tfsdk:"never_expires"`
	AllowedCIDRs     types.Set    `tfsdk:"allowed_cidrs"`
	CreatedAt        types.String `tfsdk:"created_at"`
	LastUsedAt       types.String `tfsdk:"last_used_at"`
	DisablesAt       types.String `tfsdk:"disables_at"`
	Secret           types.String `tfsdk:"secret"`
	Token            types.String `tfsdk:"token"`
}

func newAccessKeyResource() resource.Resource { return &accessKeyResource{} }

func (r *accessKeyResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_iam_access_key"
}

func (r *accessKeyResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	owner := func(desc string) schema.StringAttribute {
		return schema.StringAttribute{
			Optional: true, MarkdownDescription: desc,
			PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			Validators: []validator.String{
				stringvalidator.ExactlyOneOf(path.MatchRoot("service_account_id"), path.MatchRoot("user_id")),
			},
		}
	}
	sensitive := func(desc string) schema.StringAttribute {
		return schema.StringAttribute{
			Computed: true, Sensitive: true, MarkdownDescription: desc,
			PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
		}
	}
	resp.Schema = schema.Schema{
		MarkdownDescription: "An access key for a service account (or a user). The secret is only " +
			"known when the key is created: it is stored in the Terraform state (marked sensitive), " +
			"so keep the state encrypted, or create keys outside Terraform. An imported key has no secret.",
		Attributes: map[string]schema.Attribute{
			"id":                 idAttr("Access key ID (`TBK...`)."),
			"organization_id":    orgIDAttr(),
			"service_account_id": owner("Service account that owns the key. Give this or `user_id`."),
			"user_id":            owner("User that owns the key. Give this or `service_account_id`."),
			"trn":                computedString("The key's TRN."),
			"owner":              computedString("TRN of the key's owner."),
			"description":        iamDescription(255),
			"status": schema.StringAttribute{
				Optional: true, Computed: true, Default: stringdefault.StaticString("active"),
				MarkdownDescription: "`active` or `inactive`. TebibHost may turn an unused key off; " +
					"Terraform then sees `inactive` and turns it on again. Default: `active`.",
				Validators: []validator.String{stringvalidator.OneOf("active", "inactive")},
			},
			"status_reason": volatileString("Why TebibHost turned the key off, if it did."),
			"expires_at": schema.StringAttribute{
				Optional: true, Computed: true,
				MarkdownDescription: "Expiry (RFC 3339), at most 365 days ahead. Default: 90 days " +
					"after creation. Changing it replaces the key.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplaceIfConfigured(), stringplanmodifier.UseStateForUnknown()},
				Validators:    []validator.String{stringvalidator.ConflictsWith(path.MatchRoot("never_expires"))},
			},
			"never_expires": schema.BoolAttribute{
				Optional: true, Computed: true, Default: booldefault.StaticBool(false),
				MarkdownDescription: "A key that never expires (service accounts only; needs " +
					"`iam:CreateNonExpiringAccessKey`). Changing it replaces the key.",
				PlanModifiers: []planmodifier.Bool{boolRequiresReplace()},
			},
			"allowed_cidrs": schema.SetAttribute{
				ElementType: types.StringType, Optional: true, Computed: true,
				MarkdownDescription: "Addresses the key works from (empty: anywhere). Changing them replaces the key.",
				PlanModifiers:       []planmodifier.Set{setplanmodifier.RequiresReplaceIfConfigured(), setplanmodifier.UseStateForUnknown()},
			},
			"created_at":   computedString("When it was created (RFC 3339)."),
			"last_used_at": volatileString("When it was last used."),
			"disables_at":  volatileString("When TebibHost turns it off if it stays unused."),
			"secret":       sensitive("The secret. Only set by the create; null after an import."),
			"token":        sensitive("`<id>.<secret>`: what goes after `Authorization: Bearer`."),
		},
	}
}

func (r *accessKeyResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.configure(req.ProviderData, &resp.Diagnostics)
}

func (m *accessKeyModel) ownerPath(org string) string {
	if !m.ServiceAccountID.IsNull() && m.ServiceAccountID.ValueString() != "" {
		return client.OrgPath(org, "service-accounts", m.ServiceAccountID.ValueString(), "access-keys")
	}
	return client.OrgPath(org, "users", m.UserID.ValueString(), "access-keys")
}

// sameTime is true when two RFC 3339 times are the same instant.
func sameTime(a, b string) bool {
	ta, errA := time.Parse(time.RFC3339Nano, a)
	tb, errB := time.Parse(time.RFC3339Nano, b)
	if errA != nil || errB != nil {
		return a == b
	}
	return ta.Equal(tb)
}

func (m *accessKeyModel) fromAPI(org string, k *client.AccessKey) {
	m.ID = types.StringValue(k.ID)
	m.OrganizationID = types.StringValue(org)
	m.TRN = types.StringValue(k.TRN)
	m.Owner = types.StringValue(k.Owner)
	m.Description = types.StringValue(k.Description)
	m.Status = types.StringValue(k.Status)
	m.StatusReason = types.StringValue(k.StatusReason)
	if k.ExpiresAt == nil {
		m.ExpiresAt = types.StringNull()
	} else if m.ExpiresAt.IsNull() || m.ExpiresAt.IsUnknown() || !sameTime(m.ExpiresAt.ValueString(), *k.ExpiresAt) {
		m.ExpiresAt = types.StringValue(*k.ExpiresAt)
	}
	m.NeverExpires = types.BoolValue(k.ExpiresAt == nil)
	m.AllowedCIDRs = keepCIDRs(m.AllowedCIDRs, k.AllowedCIDRs)
	m.CreatedAt = types.StringValue(k.CreatedAt)
	m.LastUsedAt = optString(k.LastUsedAt)
	m.DisablesAt = optString(k.DisablesAt)
}

// keepCIDRs keeps the configured spelling of a CIDR set when the API's normalized set means
// the same ranges.
func keepCIDRs(prior types.Set, api []string) types.Set {
	if !prior.IsNull() && !prior.IsUnknown() {
		var mine []string
		for _, v := range prior.Elements() {
			if s, ok := v.(types.String); ok {
				mine = append(mine, normalizeCIDR(s.ValueString()))
			}
		}
		theirs := make([]string, 0, len(api))
		for _, s := range api {
			theirs = append(theirs, normalizeCIDR(s))
		}
		sort.Strings(mine)
		sort.Strings(theirs)
		if strings.Join(mine, ",") == strings.Join(theirs, ",") {
			return prior
		}
	}
	return stringSetValue(api)
}

func (r *accessKeyResource) find(ctx context.Context, m *accessKeyModel, org string) (*client.AccessKey, error) {
	keys, err := client.GetList[client.AccessKey](ctx, r.client(), m.ownerPath(org), nil)
	if err != nil {
		return nil, err
	}
	for i := range keys {
		if keys[i].ID == m.ID.ValueString() {
			return &keys[i], nil
		}
	}
	return nil, nil
}

func (r *accessKeyResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan accessKeyModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	org := r.orgID(plan.OrganizationID, &resp.Diagnostics)
	body := client.AccessKeyIn{
		Description:  plan.Description.ValueString(),
		ExpiresAt:    strPtr(plan.ExpiresAt),
		NeverExpires: plan.NeverExpires.ValueBool(),
		AllowedCIDRs: stringsFromSet(ctx, plan.AllowedCIDRs, &resp.Diagnostics),
	}
	if resp.Diagnostics.HasError() {
		return
	}
	var key client.AccessKey
	// Not retried: a second attempt would make a second key whose secret nobody saw.
	if err := r.client().Post(ctx, plan.ownerPath(org), body, &key); err != nil {
		apiError(&resp.Diagnostics, "Creating the access key", err)
		return
	}
	want := plan.Status.ValueString()
	plan.fromAPI(org, &key)
	plan.Secret = types.StringValue(key.Secret)
	plan.Token = types.StringValue(key.Token)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	if want != key.Status {
		var updated client.AccessKey
		if err := r.client().Patch(ctx, plan.ownerPath(org)+"/"+url.PathEscape(key.ID), client.AccessKeyPatch{Status: &want}, &updated); err != nil {
			apiError(&resp.Diagnostics, "Setting the access key's status", err)
			return
		}
		plan.fromAPI(org, &updated)
		resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	}
}

func (r *accessKeyResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state accessKeyModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	org := r.orgID(state.OrganizationID, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	key, err := r.find(ctx, &state, org)
	if client.IsNotFound(err) || (err == nil && key == nil) {
		resp.State.RemoveResource(ctx) // the key, or its owner, is gone
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "Reading the access key", err)
		return
	}
	state.fromAPI(org, key)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *accessKeyResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state accessKeyModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	org := state.OrganizationID.ValueString()
	patch := client.AccessKeyPatch{
		Description: ptr(plan.Description.ValueString()),
		Status:      ptr(plan.Status.ValueString()),
	}
	var key client.AccessKey
	if err := r.client().Patch(ctx, state.ownerPath(org)+"/"+url.PathEscape(state.ID.ValueString()), patch, &key); err != nil {
		apiError(&resp.Diagnostics, "Updating the access key", err)
		return
	}
	plan.fromAPI(org, &key)
	plan.Secret, plan.Token = state.Secret, state.Token
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *accessKeyResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state accessKeyModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	err := r.client().Delete(ctx, state.ownerPath(state.OrganizationID.ValueString())+"/"+url.PathEscape(state.ID.ValueString()), nil)
	if err != nil && !client.IsNotFound(err) {
		apiError(&resp.Diagnostics, "Deleting the access key", err)
	}
}

// ImportState accepts service-account/<sa_id>/<key_id> or user/<user_id>/<key_id>, with an
// optional <organization_id>/ in front.
func (r *accessKeyResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.Split(req.ID, "/")
	if len(parts) == 4 {
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("organization_id"), parts[0])...)
		parts = parts[1:]
	}
	if len(parts) != 3 || parts[1] == "" || parts[2] == "" || (parts[0] != "service-account" && parts[0] != "user") {
		resp.Diagnostics.AddError("Invalid import ID", fmt.Sprintf(
			"Expected [<organization_id>/]service-account/<service_account_id>/<key_id> "+
				"or [<organization_id>/]user/<user_id>/<key_id>, got %q.", req.ID))
		return
	}
	attr := "service_account_id"
	if parts[0] == "user" {
		attr = "user_id"
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root(attr), parts[1])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), parts[2])...)
}

// -- tebib_iam_role -------------------------------------------------------------------------

type roleResource struct{ base }

type roleModel struct {
	ID                types.String         `tfsdk:"id"`
	OrganizationID    types.String         `tfsdk:"organization_id"`
	TRN               types.String         `tfsdk:"trn"`
	Name              types.String         `tfsdk:"name"`
	Description       types.String         `tfsdk:"description"`
	TrustPolicy       jsontypes.Normalized `tfsdk:"trust_policy"`
	MaxSessionSeconds types.Int64          `tfsdk:"max_session_seconds"`
	Tags              types.Map            `tfsdk:"tags"`
	CreatedAt         types.String         `tfsdk:"created_at"`
}

func newRoleResource() resource.Resource { return &roleResource{} }

func (r *roleResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_iam_role"
}

func (r *roleResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "An IAM role: a set of permissions that trusted principals assume for a " +
			"short session. Give it permissions with `tebib_iam_policy_attachment`.",
		Attributes: map[string]schema.Attribute{
			"id":              idAttr("Role ID (`role-...`)."),
			"organization_id": orgIDAttr(),
			"trn":             computedString("TRN, the `principal` of policy attachments."),
			"name": schema.StringAttribute{
				Required: true, MarkdownDescription: "Name (1-128 of A-Z a-z 0-9 +=,.@_-). Changing it replaces the role.",
				Validators:    []validator.String{iamNamePattern},
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"description": iamDescription(1000),
			"trust_policy": schema.StringAttribute{
				CustomType: jsontypes.NormalizedType{}, Required: true,
				MarkdownDescription: "Who may assume the role: a JSON policy document, e.g. from `jsonencode()`.",
			},
			"max_session_seconds": schema.Int64Attribute{
				Optional: true, Computed: true, Default: int64default.StaticInt64(3600),
				MarkdownDescription: "Longest session, 900-43200 seconds. Default: 3600.",
				Validators:          []validator.Int64{int64validator.Between(900, 43200)},
			},
			"tags":       tagsAttr(),
			"created_at": computedString("When it was created (RFC 3339)."),
		},
	}
}

func (r *roleResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.configure(req.ProviderData, &resp.Diagnostics)
}

func (r *roleResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	importOrgScoped(ctx, req, resp)
}

func (m *roleModel) fromAPI(ctx context.Context, org string, a *client.Role) {
	m.ID = types.StringValue(a.ID)
	m.OrganizationID = types.StringValue(org)
	m.TRN = types.StringValue(a.TRN)
	m.Name = types.StringValue(a.Name)
	m.Description = types.StringValue(a.Description)
	m.TrustPolicy = keepJSONIfEqual(ctx, m.TrustPolicy, a.TrustPolicy)
	m.MaxSessionSeconds = types.Int64Value(a.MaxSessionSeconds)
	m.Tags = tagsValue(a.Tags)
	m.CreatedAt = types.StringValue(a.CreatedAt)
}

func (r *roleResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan roleModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	org := r.orgID(plan.OrganizationID, &resp.Diagnostics)
	body := client.RoleIn{
		Name: plan.Name.ValueString(), Description: plan.Description.ValueString(),
		TrustPolicy:       jsonDoc(plan.TrustPolicy, "trust_policy", &resp.Diagnostics),
		MaxSessionSeconds: plan.MaxSessionSeconds.ValueInt64(),
		Tags:              tagsFrom(ctx, plan.Tags, &resp.Diagnostics),
	}
	if resp.Diagnostics.HasError() {
		return
	}
	var role client.Role
	if err := r.client().Post(ctx, client.OrgPath(org, "roles"), body, &role); err != nil {
		apiError(&resp.Diagnostics, "Creating the role", err)
		return
	}
	plan.fromAPI(ctx, org, &role)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *roleResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state roleModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	org := r.orgID(state.OrganizationID, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	var role client.Role
	err := r.client().Get(ctx, client.OrgPath(org, "roles", state.ID.ValueString()), &role)
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "Reading the role", err)
		return
	}
	state.fromAPI(ctx, org, &role)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *roleResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state roleModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	org := state.OrganizationID.ValueString()
	patch := client.RolePatch{
		Description:       ptr(plan.Description.ValueString()),
		TrustPolicy:       jsonDoc(plan.TrustPolicy, "trust_policy", &resp.Diagnostics),
		MaxSessionSeconds: ptr(plan.MaxSessionSeconds.ValueInt64()),
		Tags:              ptr(tagsFrom(ctx, plan.Tags, &resp.Diagnostics)),
	}
	if resp.Diagnostics.HasError() {
		return
	}
	var role client.Role
	if err := r.client().Patch(ctx, client.OrgPath(org, "roles", state.ID.ValueString()), patch, &role); err != nil {
		apiError(&resp.Diagnostics, "Updating the role", err)
		return
	}
	plan.fromAPI(ctx, org, &role)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *roleResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state roleModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	err := r.client().Delete(ctx, client.OrgPath(state.OrganizationID.ValueString(), "roles", state.ID.ValueString()), nil)
	if err != nil && !client.IsNotFound(err) {
		apiError(&resp.Diagnostics, "Deleting the role", err)
	}
}

// -- tebib_iam_policy -----------------------------------------------------------------------

type policyResource struct{ base }

type policyModel struct {
	ID              types.String         `tfsdk:"id"`
	OrganizationID  types.String         `tfsdk:"organization_id"`
	TRN             types.String         `tfsdk:"trn"`
	Name            types.String         `tfsdk:"name"`
	Description     types.String         `tfsdk:"description"`
	Document        jsontypes.Normalized `tfsdk:"document"`
	DefaultVersion  types.Int64          `tfsdk:"default_version"`
	AttachmentCount types.Int64          `tfsdk:"attachment_count"`
	CreatedAt       types.String         `tfsdk:"created_at"`
}

// maxPolicyVersions is MAX_POLICY_VERSIONS in the control plane (tebib/iam/models.py).
const maxPolicyVersions = 5

func newPolicyResource() resource.Resource { return &policyResource{} }

func (r *policyResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_iam_policy"
}

func (r *policyResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "A customer-managed IAM policy. Changing `document` adds a new policy " +
			"version and makes it the default; when the policy already keeps " +
			fmt.Sprint(maxPolicyVersions) + " versions, the oldest non-default one is deleted first.",
		Attributes: map[string]schema.Attribute{
			"id":              idAttr("Policy ID (`pol-...`)."),
			"organization_id": orgIDAttr(),
			"trn":             computedString("The policy's TRN."),
			"name": schema.StringAttribute{
				Required: true, MarkdownDescription: "Name (1-128 of A-Z a-z 0-9 +=,.@_-). Changing it replaces the policy.",
				Validators:    []validator.String{iamNamePattern},
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"description": iamDescription(1000),
			"document": schema.StringAttribute{
				CustomType: jsontypes.NormalizedType{}, Required: true,
				MarkdownDescription: "The policy document (JSON), e.g. from `jsonencode()`.",
			},
			"default_version": schema.Int64Attribute{
				Computed: true, MarkdownDescription: "The version in force.",
				PlanModifiers: []planmodifier.Int64{int64KeepUnless("document")},
			},
			"attachment_count": volatileInt("How many principals it is attached to."),
			"created_at":       computedString("When it was created (RFC 3339)."),
		},
	}
}

func (r *policyResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.configure(req.ProviderData, &resp.Diagnostics)
}

func (r *policyResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	importOrgScoped(ctx, req, resp)
}

func (m *policyModel) fromAPI(ctx context.Context, org string, p *client.Policy) {
	m.ID = types.StringValue(p.ID)
	m.OrganizationID = types.StringValue(org)
	m.TRN = types.StringValue(p.TRN)
	m.Name = types.StringValue(p.Name)
	m.Description = types.StringValue(p.Description)
	m.Document = keepJSONIfEqual(ctx, m.Document, p.Document)
	m.DefaultVersion = types.Int64Value(p.DefaultVersion)
	m.AttachmentCount = types.Int64Value(p.AttachmentCount)
	m.CreatedAt = types.StringValue(p.CreatedAt)
}

func (r *policyResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan policyModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	org := r.orgID(plan.OrganizationID, &resp.Diagnostics)
	body := client.PolicyIn{
		Name: plan.Name.ValueString(), Description: plan.Description.ValueString(),
		Document: jsonDoc(plan.Document, "document", &resp.Diagnostics),
	}
	if resp.Diagnostics.HasError() {
		return
	}
	var p client.Policy
	if err := r.client().Post(ctx, client.OrgPath(org, "policies"), body, &p); err != nil {
		apiError(&resp.Diagnostics, "Creating the policy", err)
		return
	}
	plan.fromAPI(ctx, org, &p)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *policyResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state policyModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	org := r.orgID(state.OrganizationID, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	var p client.Policy
	err := r.client().Get(ctx, client.OrgPath(org, "policies", state.ID.ValueString()), &p)
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "Reading the policy", err)
		return
	}
	state.fromAPI(ctx, org, &p)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *policyResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state policyModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	org := state.OrganizationID.ValueString()
	c := r.client()
	p := client.OrgPath(org, "policies", state.ID.ValueString())
	if !plan.Description.Equal(state.Description) {
		if err := c.Patch(ctx, p, map[string]string{"description": plan.Description.ValueString()}, nil); err != nil {
			apiError(&resp.Diagnostics, "Updating the policy", err)
			return
		}
	}
	equal, diags := plan.Document.StringSemanticEquals(ctx, state.Document)
	resp.Diagnostics.Append(diags...)
	if !equal {
		doc := jsonDoc(plan.Document, "document", &resp.Diagnostics)
		if resp.Diagnostics.HasError() {
			return
		}
		versions, err := client.GetList[client.PolicyVersion](ctx, c, p+"/versions", nil)
		if err != nil {
			apiError(&resp.Diagnostics, "Listing the policy's versions", err)
			return
		}
		if len(versions) >= maxPolicyVersions {
			sort.Slice(versions, func(i, j int) bool { return versions[i].Version < versions[j].Version })
			for _, v := range versions {
				if !v.IsDefault {
					err := c.Delete(ctx, fmt.Sprintf("%s/versions/%d", p, v.Version), nil)
					if err != nil && !client.IsNotFound(err) {
						apiError(&resp.Diagnostics, fmt.Sprintf("Deleting policy version %d to make room", v.Version), err)
						return
					}
					break
				}
			}
		}
		if err := c.Post(ctx, p+"/versions", client.VersionIn{Document: doc, SetAsDefault: true}, nil); err != nil {
			apiError(&resp.Diagnostics, "Adding a policy version", err)
			return
		}
	}
	var got client.Policy
	if err := c.Get(ctx, p, &got); err != nil {
		apiError(&resp.Diagnostics, "Reading the policy", err)
		return
	}
	plan.fromAPI(ctx, org, &got)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *policyResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state policyModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	err := r.client().Delete(ctx, client.OrgPath(state.OrganizationID.ValueString(), "policies", state.ID.ValueString()), nil)
	if err != nil && !client.IsNotFound(err) {
		apiError(&resp.Diagnostics, "Deleting the policy", err)
	}
}

// -- tebib_iam_policy_attachment ------------------------------------------------------------

type policyAttachmentResource struct{ base }

type policyAttachmentModel struct {
	ID             types.String `tfsdk:"id"`
	OrganizationID types.String `tfsdk:"organization_id"`
	Policy         types.String `tfsdk:"policy"`
	Principal      types.String `tfsdk:"principal"`
	ProjectID      types.String `tfsdk:"project_id"`
	PolicyID       types.String `tfsdk:"policy_id"`
	PolicyTRN      types.String `tfsdk:"policy_trn"`
	PrincipalType  types.String `tfsdk:"principal_type"`
	CreatedAt      types.String `tfsdk:"created_at"`
}

func newPolicyAttachmentResource() resource.Resource { return &policyAttachmentResource{} }

func (r *policyAttachmentResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_iam_policy_attachment"
}

func (r *policyAttachmentResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Attaches a policy (customer or TebibHost-managed) to a user, group, " +
			"service account or role, for the whole organization or for one project.",
		Attributes: map[string]schema.Attribute{
			"id":              idAttr("Attachment ID."),
			"organization_id": orgIDAttr(),
			"policy": replaceString("Policy ID, TRN or name, e.g. `tebib_iam_policy.ci.id` or a "+
				"managed policy name like `ComputeFullAccess`. Changing it replaces the attachment.", true),
			"principal": replaceString("TRN of the user, group, service account or role. "+
				"Changing it replaces the attachment.", true),
			"project_id": replaceString("Limit the policy to this project. Default: the whole "+
				"organization. Changing it replaces the attachment.", false),
			"policy_id":      computedString("ID of the attached policy."),
			"policy_trn":     computedString("TRN of the attached policy."),
			"principal_type": computedString("`user`, `group`, `service-account` or `role`."),
			"created_at":     computedString("When it was created (RFC 3339)."),
		},
	}
}

func (r *policyAttachmentResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.configure(req.ProviderData, &resp.Diagnostics)
}

func (r *policyAttachmentResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	importOrgScoped(ctx, req, resp)
}

func (m *policyAttachmentModel) fromAPI(org string, a *client.PolicyAttachment) {
	m.ID = types.StringValue(a.ID)
	m.OrganizationID = types.StringValue(org)
	if m.Policy.IsNull() || m.Policy.IsUnknown() {
		m.Policy = types.StringValue(a.PolicyID)
	}
	m.Principal = types.StringValue(a.Principal)
	m.ProjectID = optString(a.ProjectID)
	m.PolicyID = types.StringValue(a.PolicyID)
	m.PolicyTRN = types.StringValue(a.PolicyTRN)
	m.PrincipalType = types.StringValue(a.PrincipalType)
	m.CreatedAt = types.StringValue(a.CreatedAt)
}

func (r *policyAttachmentResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan policyAttachmentModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	org := r.orgID(plan.OrganizationID, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	body := client.PolicyAttachmentIn{
		Policy: plan.Policy.ValueString(), Principal: plan.Principal.ValueString(), ProjectID: strPtr(plan.ProjectID),
	}
	var a client.PolicyAttachment
	if err := r.client().Post(ctx, client.OrgPath(org, "policy-attachments"), body, &a); err != nil {
		apiError(&resp.Diagnostics, "Attaching the policy", err)
		return
	}
	plan.fromAPI(org, &a)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *policyAttachmentResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state policyAttachmentModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	org := r.orgID(state.OrganizationID, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	// There is no GET for one attachment: list (by policy when known) and pick it out.
	q := url.Values{}
	if !state.PolicyID.IsNull() && state.PolicyID.ValueString() != "" {
		q.Set("policy", state.PolicyID.ValueString())
	}
	id := state.ID.ValueString()
	found, err := client.Find(ctx, r.client(), client.OrgPath(org, "policy-attachments"), q,
		func(a client.PolicyAttachment) bool { return a.ID == id })
	if client.IsNotFound(err) || (err == nil && found == nil) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "Reading the policy attachment", err)
		return
	}
	state.fromAPI(org, found)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *policyAttachmentResource) Update(_ context.Context, _ resource.UpdateRequest, resp *resource.UpdateResponse) {
	resp.Diagnostics.AddWarning("Nothing to update", "Policy attachments change by replacement.")
}

func (r *policyAttachmentResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state policyAttachmentModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	err := r.client().Delete(ctx, client.OrgPath(state.OrganizationID.ValueString(), "policy-attachments", state.ID.ValueString()), nil)
	if err != nil && !client.IsNotFound(err) {
		apiError(&resp.Diagnostics, "Detaching the policy", err)
	}
}

// -- tebib_iam_group ------------------------------------------------------------------------

type groupResource struct{ base }

type groupModel struct {
	ID             types.String `tfsdk:"id"`
	OrganizationID types.String `tfsdk:"organization_id"`
	TRN            types.String `tfsdk:"trn"`
	Name           types.String `tfsdk:"name"`
	Description    types.String `tfsdk:"description"`
	MemberCount    types.Int64  `tfsdk:"member_count"`
	CreatedAt      types.String `tfsdk:"created_at"`
}

func newGroupResource() resource.Resource { return &groupResource{} }

func (r *groupResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_iam_group"
}

func (r *groupResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "An IAM group of users. Policies attached to the group apply to its members. " +
			"Add members with `tebib_iam_group_membership`.",
		Attributes: map[string]schema.Attribute{
			"id":              idAttr("Group ID (`grp-...`)."),
			"organization_id": orgIDAttr(),
			"trn":             computedString("TRN, the `principal` of policy attachments."),
			"name": schema.StringAttribute{
				Required: true, MarkdownDescription: "Name (1-128 of A-Z a-z 0-9 +=,.@_-). Changing it replaces the group.",
				Validators:    []validator.String{iamNamePattern},
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"description":  iamDescription(255),
			"member_count": volatileInt("How many users are in the group."),
			"created_at":   computedString("When it was created (RFC 3339)."),
		},
	}
}

func (r *groupResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.configure(req.ProviderData, &resp.Diagnostics)
}

func (r *groupResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	importOrgScoped(ctx, req, resp)
}

func (m *groupModel) fromAPI(org string, g *client.Group) {
	m.ID = types.StringValue(g.ID)
	m.OrganizationID = types.StringValue(org)
	m.TRN = types.StringValue(g.TRN)
	m.Name = types.StringValue(g.Name)
	m.Description = types.StringValue(g.Description)
	m.MemberCount = types.Int64Value(g.MemberCount)
	m.CreatedAt = types.StringValue(g.CreatedAt)
}

func (r *groupResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan groupModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	org := r.orgID(plan.OrganizationID, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	var g client.Group
	body := client.GroupIn{Name: plan.Name.ValueString(), Description: plan.Description.ValueString()}
	if err := r.client().Post(ctx, client.OrgPath(org, "groups"), body, &g); err != nil {
		apiError(&resp.Diagnostics, "Creating the group", err)
		return
	}
	plan.fromAPI(org, &g)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *groupResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state groupModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	org := r.orgID(state.OrganizationID, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	var g client.Group
	err := r.client().Get(ctx, client.OrgPath(org, "groups", state.ID.ValueString()), &g)
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "Reading the group", err)
		return
	}
	state.fromAPI(org, &g)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *groupResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state groupModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	org := state.OrganizationID.ValueString()
	var g client.Group
	err := r.client().Patch(ctx, client.OrgPath(org, "groups", state.ID.ValueString()),
		map[string]string{"description": plan.Description.ValueString()}, &g)
	if err != nil {
		apiError(&resp.Diagnostics, "Updating the group", err)
		return
	}
	plan.fromAPI(org, &g)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *groupResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state groupModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	err := r.client().Delete(ctx, client.OrgPath(state.OrganizationID.ValueString(), "groups", state.ID.ValueString()), nil)
	if err != nil && !client.IsNotFound(err) {
		apiError(&resp.Diagnostics, "Deleting the group", err)
	}
}

// -- tebib_iam_group_membership -------------------------------------------------------------

type groupMembershipResource struct{ base }

type groupMembershipModel struct {
	ID             types.String `tfsdk:"id"`
	OrganizationID types.String `tfsdk:"organization_id"`
	GroupID        types.String `tfsdk:"group_id"`
	UserID         types.String `tfsdk:"user_id"`
	Email          types.String `tfsdk:"email"`
}

func newGroupMembershipResource() resource.Resource { return &groupMembershipResource{} }

func (r *groupMembershipResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_iam_group_membership"
}

func (r *groupMembershipResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Puts one user of the organization in an IAM group.",
		Attributes: map[string]schema.Attribute{
			"id":              idAttr("`<group_id>/<user_id>`."),
			"organization_id": orgIDAttr(),
			"group_id":        replaceString("Group ID.", true),
			"user_id":         replaceString("User ID (see `tebib iam user list`).", true),
			"email":           computedString("The user's email address."),
		},
	}
}

func (r *groupMembershipResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.configure(req.ProviderData, &resp.Diagnostics)
}

func (m *groupMembershipModel) memberPath() string {
	return client.OrgPath(m.OrganizationID.ValueString(), "groups", m.GroupID.ValueString(), "members", m.UserID.ValueString())
}

func (r *groupMembershipResource) find(ctx context.Context, m *groupMembershipModel) (*client.GroupMember, error) {
	members, err := client.GetList[client.GroupMember](ctx, r.client(),
		client.OrgPath(m.OrganizationID.ValueString(), "groups", m.GroupID.ValueString(), "members"), nil)
	if err != nil {
		return nil, err
	}
	for i := range members {
		if members[i].UserID == m.UserID.ValueString() {
			return &members[i], nil
		}
	}
	return nil, nil
}

func (r *groupMembershipResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan groupMembershipModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	plan.OrganizationID = types.StringValue(r.orgID(plan.OrganizationID, &resp.Diagnostics))
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client().Put(ctx, plan.memberPath(), nil, nil); err != nil {
		apiError(&resp.Diagnostics, "Adding the user to the group", err)
		return
	}
	plan.ID = types.StringValue(plan.GroupID.ValueString() + "/" + plan.UserID.ValueString())
	plan.Email = types.StringNull()
	if m, err := r.find(ctx, &plan); err == nil && m != nil {
		plan.Email = types.StringValue(m.Email)
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *groupMembershipResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state groupMembershipModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	state.OrganizationID = types.StringValue(r.orgID(state.OrganizationID, &resp.Diagnostics))
	if resp.Diagnostics.HasError() {
		return
	}
	m, err := r.find(ctx, &state)
	if client.IsNotFound(err) || (err == nil && m == nil) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "Reading the group's members", err)
		return
	}
	state.Email = types.StringValue(m.Email)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *groupMembershipResource) Update(_ context.Context, _ resource.UpdateRequest, resp *resource.UpdateResponse) {
	resp.Diagnostics.AddWarning("Nothing to update", "Group memberships change by replacement.")
}

func (r *groupMembershipResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state groupMembershipModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client().Delete(ctx, state.memberPath(), nil); err != nil && !client.IsNotFound(err) {
		apiError(&resp.Diagnostics, "Removing the user from the group", err)
	}
}

// ImportState accepts <group_id>/<user_id> or <organization_id>/<group_id>/<user_id>.
func (r *groupMembershipResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.Split(req.ID, "/")
	if len(parts) == 3 {
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("organization_id"), parts[0])...)
		parts = parts[1:]
	}
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		resp.Diagnostics.AddError("Invalid import ID",
			fmt.Sprintf("Expected <group_id>/<user_id> or <organization_id>/<group_id>/<user_id>, got %q.", req.ID))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("group_id"), parts[0])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("user_id"), parts[1])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), parts[0]+"/"+parts[1])...)
}
