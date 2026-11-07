package provider

import (
	"context"
	"net/http"
	"net/url"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	dschema "github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/tebibhost/terraform-provider-tebib/internal/client"
)

// -- tebib_bucket ---------------------------------------------------------------------------

type bucketResource struct{ base }

type bucketModel struct {
	projectMeta
	Visibility   types.String   `tfsdk:"visibility"`
	MaxSizeGiB   types.Int64    `tfsdk:"max_size_gib"`
	ForceDestroy types.Bool     `tfsdk:"force_destroy"`
	Endpoint     types.String   `tfsdk:"endpoint"`
	URL          types.String   `tfsdk:"url"`
	StoredBytes  types.Int64    `tfsdk:"stored_bytes"`
	ObjectCount  types.Int64    `tfsdk:"object_count"`
	Timeouts     timeouts.Value `tfsdk:"timeouts"`
}

func newBucketResource() resource.Resource { return &bucketResource{} }

func (r *bucketResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_bucket"
}

func (r *bucketResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	attrs := metaAttrs("buckets", "Bucket ID (`bkt-...`).")
	attrs["name"] = schema.StringAttribute{
		Required: true,
		MarkdownDescription: "The S3 bucket name: 3-63 lowercase letters, digits and `-`, unique in " +
			"the region (across all customers). Changing it replaces the bucket.",
		PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
		Validators:    []validator.String{stringvalidator.RegexMatches(regexpMust(`^[a-z0-9][a-z0-9-]{1,61}[a-z0-9]$`), "3-63 lowercase letters, digits and -")},
	}
	resp.Schema = schema.Schema{
		MarkdownDescription: "An S3-compatible object storage bucket. Use it with any S3 client " +
			"(path-style URLs, the region from `tebib_storage`) and a `tebib_storage_key`. " +
			"A bucket that holds objects is only deleted with `force_destroy`.",
		Attributes: mergeAttrs(attrs, map[string]schema.Attribute{
			"visibility": schema.StringAttribute{
				Optional: true, Computed: true, Default: stringdefault.StaticString("private"),
				MarkdownDescription: "`private` (default) or `public-read`: anyone can download objects " +
					"without a key. Public buckets need a verified account.",
				Validators: []validator.String{stringvalidator.OneOf("private", "public-read")},
			},
			"max_size_gib": schema.Int64Attribute{
				Optional: true,
				MarkdownDescription: "The bucket's own size cap in GiB (1-100000). Default: none, only " +
					"the project's storage quota applies.",
				Validators: []validator.Int64{int64validator.Between(1, 100000)},
			},
			"force_destroy": schema.BoolAttribute{
				Optional: true, Computed: true, Default: booldefault.StaticBool(false),
				MarkdownDescription: "Delete the bucket even if it holds objects (they are deleted " +
					"with it). Default: `false`, deleting a bucket that holds objects fails.",
			},
			"endpoint":     computedString("S3 endpoint URL."),
			"url":          computedString("Path-style URL of the bucket (`<endpoint>/<name>`)."),
			"stored_bytes": volatileInt("Bytes stored, as last observed (about a minute behind)."),
			"object_count": volatileInt("Objects stored, as last observed."),
		}),
		Blocks: map[string]schema.Block{
			"timeouts": timeouts.Block(ctx, timeouts.Opts{Create: true, Update: true, Delete: true}),
		},
	}
}

func (r *bucketResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.configure(req.ProviderData, &resp.Diagnostics)
}

func (r *bucketResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	importProjectScoped(ctx, req, resp)
}

func (m *bucketModel) fromAPI(b *client.Bucket) {
	m.projectMeta.fromAPI(b.Meta)
	m.Visibility = types.StringValue(b.Visibility)
	m.MaxSizeGiB = optInt(b.MaxSizeGiB)
	m.Endpoint = types.StringValue(b.Endpoint)
	m.URL = types.StringValue(b.URL)
	m.StoredBytes = types.Int64Value(b.StoredBytes)
	m.ObjectCount = types.Int64Value(b.ObjectCount)
	if m.ForceDestroy.IsNull() || m.ForceDestroy.IsUnknown() {
		m.ForceDestroy = types.BoolValue(false) // after an import
	}
}

func bucketPath(project, id string) string { return client.ProjectPath(project, "buckets", id) }

func (r *bucketResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan bucketModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	project := r.projectID(plan.ProjectID, &resp.Diagnostics)
	d, ds := plan.Timeouts.Create(ctx, 10*time.Minute)
	ctx, cancel := withTimeout(ctx, d, ds, &resp.Diagnostics)
	defer cancel()
	body := client.BucketIn{
		Name: plan.Name.ValueString(), Visibility: plan.Visibility.ValueString(),
		MaxSizeGiB:  int64Ptr(plan.MaxSizeGiB),
		Description: plan.Description.ValueString(), Tags: tagsFrom(ctx, plan.Tags, &resp.Diagnostics),
	}
	if resp.Diagnostics.HasError() {
		return
	}
	var b client.Bucket
	if err := r.client().Create(ctx, client.ProjectPath(project, "buckets"), body, &b); err != nil {
		apiError(&resp.Diagnostics, "Creating the bucket", err)
		return
	}
	plan.fromAPI(&b)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	settled, err := client.Wait[client.Bucket](ctx, r.client(), bucketPath(project, b.ID), nil)
	if settled != nil {
		plan.fromAPI(settled)
		resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	}
	if err != nil {
		waitError(&resp.Diagnostics, "the bucket", err)
	}
}

func (r *bucketResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state bucketModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	project := r.projectID(state.ProjectID, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	var b client.Bucket
	err := r.client().Get(ctx, bucketPath(project, state.ID.ValueString()), &b)
	if client.IsNotFound(err) || (err == nil && b.State == "deleted") {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "Reading the bucket", err)
		return
	}
	state.fromAPI(&b)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *bucketResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state bucketModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	d, ds := plan.Timeouts.Update(ctx, 10*time.Minute)
	ctx, cancel := withTimeout(ctx, d, ds, &resp.Diagnostics)
	defer cancel()
	p := bucketPath(state.ProjectID.ValueString(), state.ID.ValueString())
	var patch client.BucketPatch
	changed := false
	if tp := tagsPatch(ctx, plan.projectMeta, state.projectMeta, &resp.Diagnostics); tp != nil {
		patch.Description, patch.Tags = tp.Description, tp.Tags
		changed = true
	}
	if !plan.Visibility.Equal(state.Visibility) {
		patch.Visibility = ptr(plan.Visibility.ValueString())
		changed = true
	}
	if !plan.MaxSizeGiB.Equal(state.MaxSizeGiB) {
		patch.MaxSizeGiB = ptr(plan.MaxSizeGiB.ValueInt64()) // null -> 0: no own cap
		changed = true
	}
	if changed {
		if err := r.client().Patch(ctx, p, patch, nil); err != nil {
			apiError(&resp.Diagnostics, "Updating the bucket", err)
			return
		}
	}
	settled, err := client.Wait[client.Bucket](ctx, r.client(), p, nil)
	if err != nil {
		waitError(&resp.Diagnostics, "the bucket", err)
		return
	}
	force := plan.ForceDestroy
	plan.fromAPI(settled)
	plan.ForceDestroy = force
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *bucketResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state bucketModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	d, ds := state.Timeouts.Delete(ctx, 30*time.Minute)
	ctx, cancel := withTimeout(ctx, d, ds, &resp.Diagnostics)
	defer cancel()
	c := r.client()
	p := bucketPath(state.ProjectID.ValueString(), state.ID.ValueString())
	var q url.Values
	if state.ForceDestroy.ValueBool() {
		q = url.Values{"force": {"true"}}
	}
	// Not retried on 409: "BucketNotEmpty" won't go away by waiting.
	err := c.Do(ctx, client.Request{Method: http.MethodDelete, Path: p, Query: q}, nil)
	if client.IsNotFound(err) {
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "Deleting the bucket", err)
		return
	}
	if err := client.WaitGone[client.Bucket](ctx, c, p); err != nil {
		apiError(&resp.Diagnostics, "Deleting the bucket", err)
	}
}

// -- tebib_storage_key ----------------------------------------------------------------------

type storageKeyResource struct{ base }

type storageKeyModel struct {
	projectMeta
	ServiceAccountID types.String   `tfsdk:"service_account_id"`
	Status           types.String   `tfsdk:"status"`
	AccessKeyID      types.String   `tfsdk:"access_key_id"`
	SecretAccessKey  types.String   `tfsdk:"secret_access_key"`
	Holder           types.String   `tfsdk:"holder"`
	EffectiveStatus  types.String   `tfsdk:"effective_status"`
	Endpoint         types.String   `tfsdk:"endpoint"`
	Region           types.String   `tfsdk:"region"`
	Timeouts         timeouts.Value `tfsdk:"timeouts"`
}

func newStorageKeyResource() resource.Resource { return &storageKeyResource{} }

func (r *storageKeyResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_storage_key"
}

func (r *storageKeyResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "An S3 access key for the project's buckets. Its holder's IAM " +
			"permissions decide what it can do. The secret is only known when the key is created: " +
			"it is stored in the Terraform state (marked sensitive), so keep the state encrypted. " +
			"An imported key has no secret.",
		Attributes: mergeAttrs(metaAttrs("storage keys", "Storage key ID (`s3key-...`)."), map[string]schema.Attribute{
			"service_account_id": replaceString("Give the key to this service account (`sa-...`). "+
				"Default: the caller (the user or service account whose credentials the provider "+
				"uses). Changing it replaces the key.", false),
			"status": schema.StringAttribute{
				Optional: true, Computed: true, Default: stringdefault.StaticString("active"),
				MarkdownDescription: "`active` (default) or `inactive`: S3 refuses an inactive key.",
				Validators:          []validator.String{stringvalidator.OneOf("active", "inactive")},
			},
			"access_key_id": computedString("The S3 access key ID."),
			"secret_access_key": schema.StringAttribute{
				Computed: true, Sensitive: true,
				MarkdownDescription: "The S3 secret. Only set by the create; null after an import.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"holder":           computedString("TRN of the user or service account holding the key."),
			"effective_status": volatileString("What S3 sees now: `pending`, `active`, `inactive`, `holder-inactive` or `lost`."),
			"endpoint":         computedString("S3 endpoint URL."),
			"region":           computedString("The region name S3 clients sign requests with."),
		}),
		Blocks: map[string]schema.Block{
			"timeouts": timeouts.Block(ctx, timeouts.Opts{Create: true, Update: true, Delete: true}),
		},
	}
}

func (r *storageKeyResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.configure(req.ProviderData, &resp.Diagnostics)
}

func (r *storageKeyResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	importProjectScoped(ctx, req, resp)
}

func storageKeyPath(project, id string) string {
	return client.ProjectPath(project, "storage-keys", id)
}

func (m *storageKeyModel) fromAPI(k *client.StorageKey) {
	m.projectMeta.fromAPI(k.Meta)
	m.Status = types.StringValue(k.Status)
	m.AccessKeyID = types.StringValue(k.AccessKeyID)
	m.Holder = types.StringValue(k.Holder)
	m.EffectiveStatus = types.StringValue(k.EffectiveStatus)
	m.Endpoint = types.StringValue(k.Endpoint)
	if k.Region != "" {
		m.Region = types.StringValue(k.Region)
	}
	if m.SecretAccessKey.IsUnknown() {
		m.SecretAccessKey = types.StringNull()
	}
	if m.ServiceAccountID.IsUnknown() {
		m.ServiceAccountID = types.StringNull()
	}
}

// region fills in the region when the key's answer doesn't carry it (reads, imports).
func (r *storageKeyResource) region(ctx context.Context, project string, m *storageKeyModel) {
	if !m.Region.IsNull() && !m.Region.IsUnknown() && m.Region.ValueString() != "" {
		return
	}
	var s client.Storage
	if err := r.client().Get(ctx, client.ProjectPath(project, "storage"), &s); err == nil {
		m.Region = types.StringValue(s.Region)
	} else {
		m.Region = types.StringNull()
	}
}

func (r *storageKeyResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan storageKeyModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	project := r.projectID(plan.ProjectID, &resp.Diagnostics)
	d, ds := plan.Timeouts.Create(ctx, 10*time.Minute)
	ctx, cancel := withTimeout(ctx, d, ds, &resp.Diagnostics)
	defer cancel()
	body := client.StorageKeyIn{
		Name: plan.Name.ValueString(), ServiceAccountID: strPtr(plan.ServiceAccountID),
		Description: plan.Description.ValueString(), Tags: tagsFrom(ctx, plan.Tags, &resp.Diagnostics),
	}
	if resp.Diagnostics.HasError() {
		return
	}
	want := plan.Status.ValueString()
	var k client.StorageKey
	// Not retried: a replayed create answers without the secret.
	if err := r.client().Post(ctx, client.ProjectPath(project, "storage-keys"), body, &k); err != nil {
		apiError(&resp.Diagnostics, "Creating the storage key", err)
		return
	}
	plan.fromAPI(&k)
	plan.SecretAccessKey = optString(k.SecretAccessKey)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	c := r.client()
	p := storageKeyPath(project, k.ID)
	settled, err := client.Wait[client.StorageKey](ctx, c, p, nil)
	if err != nil {
		waitError(&resp.Diagnostics, "the storage key", err)
		return
	}
	if want != settled.Status {
		if err := c.Patch(ctx, p, client.StorageKeyPatch{Status: &want}, nil); err != nil {
			apiError(&resp.Diagnostics, "Setting the storage key's status", err)
			return
		}
		if settled, err = client.Wait[client.StorageKey](ctx, c, p, nil); err != nil {
			waitError(&resp.Diagnostics, "the storage key", err)
			return
		}
	}
	plan.fromAPI(settled)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *storageKeyResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state storageKeyModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	project := r.projectID(state.ProjectID, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	var k client.StorageKey
	err := r.client().Get(ctx, storageKeyPath(project, state.ID.ValueString()), &k)
	if client.IsNotFound(err) || (err == nil && k.State == "deleted") {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "Reading the storage key", err)
		return
	}
	state.fromAPI(&k)
	r.region(ctx, project, &state)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *storageKeyResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state storageKeyModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	d, ds := plan.Timeouts.Update(ctx, 5*time.Minute)
	ctx, cancel := withTimeout(ctx, d, ds, &resp.Diagnostics)
	defer cancel()
	project := state.ProjectID.ValueString()
	p := storageKeyPath(project, state.ID.ValueString())
	var patch client.StorageKeyPatch
	changed := false
	if tp := tagsPatch(ctx, plan.projectMeta, state.projectMeta, &resp.Diagnostics); tp != nil {
		patch.Description, patch.Tags = tp.Description, tp.Tags
		changed = true
	}
	if !plan.Status.Equal(state.Status) {
		patch.Status = ptr(plan.Status.ValueString())
		changed = true
	}
	if changed {
		if err := r.client().Patch(ctx, p, patch, nil); err != nil {
			apiError(&resp.Diagnostics, "Updating the storage key", err)
			return
		}
	}
	settled, err := client.Wait[client.StorageKey](ctx, r.client(), p, nil)
	if err != nil {
		waitError(&resp.Diagnostics, "the storage key", err)
		return
	}
	plan.fromAPI(settled)
	plan.SecretAccessKey, plan.Region, plan.ServiceAccountID = state.SecretAccessKey, state.Region, state.ServiceAccountID
	r.region(ctx, project, &plan)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *storageKeyResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state storageKeyModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	d, ds := state.Timeouts.Delete(ctx, 10*time.Minute)
	ctx, cancel := withTimeout(ctx, d, ds, &resp.Diagnostics)
	defer cancel()
	if err := deleteAndWait[client.StorageKey](ctx, r.client(), storageKeyPath(state.ProjectID.ValueString(), state.ID.ValueString())); err != nil {
		apiError(&resp.Diagnostics, "Deleting the storage key", err)
	}
}

// -- data source tebib_storage --------------------------------------------------------------

type storageDataSource struct{ base }

type storageDataModel struct {
	ProjectID   types.String `tfsdk:"project_id"`
	Endpoint    types.String `tfsdk:"endpoint"`
	Region      types.String `tfsdk:"region"`
	Buckets     types.Int64  `tfsdk:"buckets"`
	AccessKeys  types.Int64  `tfsdk:"access_keys"`
	StoredBytes types.Int64  `tfsdk:"stored_bytes"`
	QuotaGiB    types.Int64  `tfsdk:"quota_gib"`
	State       types.String `tfsdk:"state"`
}

func newStorageDataSource() datasource.DataSource { return &storageDataSource{} }

func (d *storageDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_storage"
}

func (d *storageDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	n := func(desc string) dschema.Int64Attribute {
		return dschema.Int64Attribute{Computed: true, MarkdownDescription: desc}
	}
	resp.Schema = dschema.Schema{
		MarkdownDescription: "A project's object storage: the S3 endpoint and region to configure " +
			"S3 clients with, and its usage.",
		Attributes: map[string]dschema.Attribute{
			"project_id":   dsProjectID(),
			"endpoint":     dsString("S3 endpoint, path-style (`<endpoint>/<bucket>/<key>`)."),
			"region":       dsString("The region name S3 clients sign requests with."),
			"buckets":      n("Number of buckets."),
			"access_keys":  n("Number of storage keys."),
			"stored_bytes": n("Bytes stored in all buckets."),
			"quota_gib":    n("The project's storage quota in GiB."),
			"state":        dsString("The project's storage account: `none` until the first bucket or key, then `creating`, `active`..."),
		},
	}
}

func (d *storageDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.configure(req.ProviderData, &resp.Diagnostics)
}

func (d *storageDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var m storageDataModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	project := d.projectID(m.ProjectID, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	var s client.Storage
	if err := d.client().Get(ctx, client.ProjectPath(project, "storage"), &s); err != nil {
		apiError(&resp.Diagnostics, "Reading the project's storage", err)
		return
	}
	m.ProjectID = types.StringValue(project)
	m.Endpoint = types.StringValue(s.Endpoint)
	m.Region = types.StringValue(s.Region)
	m.Buckets = types.Int64Value(s.Buckets)
	m.AccessKeys = types.Int64Value(s.AccessKeys)
	m.StoredBytes = types.Int64Value(s.StoredBytes)
	m.QuotaGiB = types.Int64Value(s.QuotaGiB)
	m.State = types.StringValue(s.State)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
