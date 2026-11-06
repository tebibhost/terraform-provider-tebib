package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-jsontypes/jsontypes"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/mapdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/tebibhost/terraform-provider-tebib/internal/client"
)

func pathRoot(name string) path.Path { return path.Root(name) }

// conflictWindow is how long a call that races a controller (attach while detaching,
// delete right after a dependant went away) is retried on 409 Conflict.
const conflictWindow = 3 * time.Minute

// base is embedded in every resource and data source.
type base struct {
	data *providerData
}

func (b *base) configure(raw any, diags *diag.Diagnostics) {
	if raw == nil {
		return // the provider isn't configured yet (validation)
	}
	data, ok := raw.(*providerData)
	if !ok {
		diags.AddError("Unexpected provider data", fmt.Sprintf("got %T", raw))
		return
	}
	b.data = data
}

func (b *base) client() *client.Client { return b.data.client }

// projectID is the resource's project: its own project_id, else the provider's project.
func (b *base) projectID(v types.String, diags *diag.Diagnostics) string {
	if !v.IsNull() && !v.IsUnknown() && v.ValueString() != "" {
		return v.ValueString()
	}
	if b.data.project == "" {
		diags.AddAttributeError(path.Root("project_id"), "No project",
			"Set project_id here, project in the provider block, or TEBIB_PROJECT.")
	}
	return b.data.project
}

// orgID is the resource's organization: its own organization_id, else the provider's.
func (b *base) orgID(v types.String, diags *diag.Diagnostics) string {
	if !v.IsNull() && !v.IsUnknown() && v.ValueString() != "" {
		return v.ValueString()
	}
	if b.data.organization == "" {
		diags.AddAttributeError(path.Root("organization_id"), "No organization",
			"Set organization_id here, organization in the provider block, or TEBIB_ORGANIZATION.")
	}
	return b.data.organization
}

func apiError(diags *diag.Diagnostics, summary string, err error) {
	diags.AddError(summary, err.Error())
}

// withTimeout returns ctx bounded by a timeouts-block value.
func withTimeout(ctx context.Context, d time.Duration, diags diag.Diagnostics, into *diag.Diagnostics) (context.Context, context.CancelFunc) {
	into.Append(diags...)
	return context.WithTimeout(ctx, d)
}

// deleteAndWait deletes a project resource (404 counts as done) and waits until it is gone.
func deleteAndWait[T client.Stateful](ctx context.Context, c *client.Client, p string) error {
	err := c.RetryConflict(ctx, conflictWindow, func() error { return c.Delete(ctx, p, nil) })
	if client.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	return client.WaitGone[T](ctx, c, p)
}

// -- values ---------------------------------------------------------------------------------

func tagsFrom(ctx context.Context, m types.Map, diags *diag.Diagnostics) map[string]string {
	out := map[string]string{}
	if m.IsNull() || m.IsUnknown() {
		return out
	}
	diags.Append(m.ElementsAs(ctx, &out, false)...)
	return out
}

func tagsValue(m map[string]string) types.Map {
	elems := make(map[string]attr.Value, len(m))
	for k, v := range m {
		elems[k] = types.StringValue(v)
	}
	return types.MapValueMust(types.StringType, elems)
}

func stringsFromSet(ctx context.Context, s types.Set, diags *diag.Diagnostics) []string {
	out := []string{}
	if s.IsNull() || s.IsUnknown() {
		return out
	}
	diags.Append(s.ElementsAs(ctx, &out, false)...)
	return out
}

func stringsFromList(ctx context.Context, s types.List, diags *diag.Diagnostics) []string {
	out := []string{}
	if s.IsNull() || s.IsUnknown() {
		return out
	}
	diags.Append(s.ElementsAs(ctx, &out, false)...)
	return out
}

func stringSetValue(items []string) types.Set {
	elems := make([]attr.Value, 0, len(items))
	for _, v := range items {
		elems = append(elems, types.StringValue(v))
	}
	return types.SetValueMust(types.StringType, elems)
}

func stringListValue(items []string) types.List {
	elems := make([]attr.Value, 0, len(items))
	for _, v := range items {
		elems = append(elems, types.StringValue(v))
	}
	return types.ListValueMust(types.StringType, elems)
}

func optString(p *string) types.String { return types.StringPointerValue(p) }

func optInt(p *int64) types.Int64 { return types.Int64PointerValue(p) }

func ptr[T any](v T) *T { return &v }

// strPtr is nil for a null, unknown or empty string.
func strPtr(v types.String) *string {
	if v.IsNull() || v.IsUnknown() || v.ValueString() == "" {
		return nil
	}
	s := v.ValueString()
	return &s
}

func int64Ptr(v types.Int64) *int64 {
	if v.IsNull() || v.IsUnknown() {
		return nil
	}
	n := v.ValueInt64()
	return &n
}

// jsonDoc decodes a JSON attribute into an object.
func jsonDoc(v jsontypes.Normalized, attrName string, diags *diag.Diagnostics) map[string]any {
	var out map[string]any
	if err := json.Unmarshal([]byte(v.ValueString()), &out); err != nil || out == nil {
		diags.AddAttributeError(path.Root(attrName), "Invalid JSON document",
			"It must be a JSON object, e.g. jsonencode({ Version = \"2026-01-01\", Statement = [...] }).")
	}
	return out
}

// jsonValue encodes an API document. Normalized compares semantically, so key order and
// spacing differences between the config and the API are not drift.
func jsonValue(doc map[string]any) jsontypes.Normalized {
	b, _ := json.Marshal(doc)
	return jsontypes.NewNormalizedValue(string(b))
}

// keepJSONIfEqual keeps the user's spelling of a JSON document when the API's is
// semantically the same, so a refresh doesn't rewrite it.
func keepJSONIfEqual(ctx context.Context, prior jsontypes.Normalized, api map[string]any) jsontypes.Normalized {
	fresh := jsonValue(api)
	if prior.IsNull() || prior.IsUnknown() {
		return fresh
	}
	equal, diags := prior.StringSemanticEquals(ctx, fresh)
	if !diags.HasError() && equal {
		return prior
	}
	return fresh
}

// splitID splits an import ID "a/b/c" into exactly n parts, or reports an error.
func splitID(id string, n int, format string, diags *diag.Diagnostics) []string {
	parts := strings.Split(id, "/")
	if len(parts) != n {
		diags.AddError("Invalid import ID", fmt.Sprintf("Expected %s, got %q.", format, id))
		return nil
	}
	for _, p := range parts {
		if p == "" {
			diags.AddError("Invalid import ID", fmt.Sprintf("Expected %s, got %q.", format, id))
			return nil
		}
	}
	return parts
}

// importProjectScoped accepts "<id>" (the provider's project) or "<project_id>/<id>".
func importProjectScoped(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.Split(req.ID, "/")
	switch {
	case len(parts) == 1 && parts[0] != "":
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), parts[0])...)
	case len(parts) == 2 && parts[0] != "" && parts[1] != "":
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("project_id"), parts[0])...)
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), parts[1])...)
	default:
		resp.Diagnostics.AddError("Invalid import ID",
			fmt.Sprintf("Expected <id> or <project_id>/<id>, got %q.", req.ID))
	}
}

// failedState reports a resource that settled in "error" (or timed out) during create:
// the state is already saved, so Terraform marks it tainted and replaces it next time.
func waitError(diags *diag.Diagnostics, what string, err error) {
	var failed *client.ResourceFailedError
	if errors.As(err, &failed) {
		diags.AddError(what+" failed", err.Error()+
			". The resource is kept in the state as tainted: the next apply replaces it.")
		return
	}
	diags.AddError("Waiting for "+what, err.Error())
}

// -- common schema attributes ---------------------------------------------------------------

func idAttr(desc string) schema.StringAttribute {
	return schema.StringAttribute{
		Computed:            true,
		MarkdownDescription: desc,
		PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
	}
}

func computedString(desc string) schema.StringAttribute {
	return schema.StringAttribute{
		Computed:            true,
		MarkdownDescription: desc,
		PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
	}
}

// volatileString is computed and may change on any update (status messages).
func volatileString(desc string) schema.StringAttribute {
	return schema.StringAttribute{Computed: true, MarkdownDescription: desc}
}

func projectIDAttr() schema.StringAttribute {
	return schema.StringAttribute{
		Optional: true,
		Computed: true,
		MarkdownDescription: "Project ID (`p-...`). Default: the provider's `project`. " +
			"Changing it replaces the resource.",
		PlanModifiers: []planmodifier.String{
			stringplanmodifier.RequiresReplaceIfConfigured(),
			stringplanmodifier.UseStateForUnknown(),
		},
	}
}

func orgIDAttr() schema.StringAttribute {
	return schema.StringAttribute{
		Optional: true,
		Computed: true,
		MarkdownDescription: "Organization ID (`o-...`). Default: the provider's `organization`. " +
			"Changing it replaces the resource.",
		PlanModifiers: []planmodifier.String{
			stringplanmodifier.RequiresReplaceIfConfigured(),
			stringplanmodifier.UseStateForUnknown(),
		},
	}
}

func replaceString(desc string, required bool) schema.StringAttribute {
	return schema.StringAttribute{
		Required:            required,
		Optional:            !required,
		MarkdownDescription: desc,
		PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
	}
}

func nameAttr(noun string) schema.StringAttribute {
	return replaceString(fmt.Sprintf("Name, unique among the project's %s. Changing it replaces the resource.", noun), true)
}

func descriptionAttr() schema.StringAttribute {
	return schema.StringAttribute{
		Optional:            true,
		Computed:            true,
		Default:             stringdefault.StaticString(""),
		MarkdownDescription: "Description, at most 255 characters. Default: empty.",
	}
}

func tagsAttr() schema.MapAttribute {
	return schema.MapAttribute{
		ElementType:         types.StringType,
		Optional:            true,
		Computed:            true,
		Default:             mapdefault.StaticValue(types.MapValueMust(types.StringType, map[string]attr.Value{})),
		MarkdownDescription: "Tags (key-value pairs). IAM policies can match them. Default: none.",
	}
}

// metaAttrs are the attributes every project resource has.
func metaAttrs(noun string, idDesc string) map[string]schema.Attribute {
	return map[string]schema.Attribute{
		"id":             idAttr(idDesc),
		"project_id":     projectIDAttr(),
		"trn":            computedString("TebibHost resource name (TRN), for IAM policies."),
		"name":           nameAttr(noun),
		"description":    descriptionAttr(),
		"tags":           tagsAttr(),
		"state":          computedString("Lifecycle state: `active` once ready (or `error`)."),
		"status_message": volatileString("What the platform last said about the resource."),
		"created_at":     computedString("When it was created (RFC 3339)."),
	}
}

func mergeAttrs(maps ...map[string]schema.Attribute) map[string]schema.Attribute {
	out := map[string]schema.Attribute{}
	for _, m := range maps {
		for k, v := range m {
			out[k] = v
		}
	}
	return out
}

// projectMeta is embedded in the model of every project resource.
type projectMeta struct {
	ID            types.String `tfsdk:"id"`
	ProjectID     types.String `tfsdk:"project_id"`
	TRN           types.String `tfsdk:"trn"`
	Name          types.String `tfsdk:"name"`
	Description   types.String `tfsdk:"description"`
	Tags          types.Map    `tfsdk:"tags"`
	State         types.String `tfsdk:"state"`
	StatusMessage types.String `tfsdk:"status_message"`
	CreatedAt     types.String `tfsdk:"created_at"`
}

func (m *projectMeta) fromAPI(a client.Meta) {
	m.ID = types.StringValue(a.ID)
	m.ProjectID = types.StringValue(a.ProjectID)
	m.TRN = types.StringValue(a.TRN)
	m.Name = types.StringValue(a.Name)
	m.Description = types.StringValue(a.Description)
	m.Tags = tagsValue(a.Tags)
	m.State = types.StringValue(a.State)
	m.StatusMessage = types.StringValue(a.StatusMessage)
	m.CreatedAt = types.StringValue(a.CreatedAt)
}

// tagsPatch is the PATCH body for description and tag changes, or nil if neither changed.
func tagsPatch(ctx context.Context, plan, state projectMeta, diags *diag.Diagnostics) *client.TagsPatch {
	var patch client.TagsPatch
	changed := false
	if !plan.Description.Equal(state.Description) {
		patch.Description = ptr(plan.Description.ValueString())
		changed = true
	}
	if !plan.Tags.Equal(state.Tags) {
		patch.Tags = ptr(tagsFrom(ctx, plan.Tags, diags))
		changed = true
	}
	if !changed {
		return nil
	}
	return &patch
}
