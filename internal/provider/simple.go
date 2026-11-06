package provider

import (
	"context"
	"fmt"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"

	"github.com/tebibhost/terraform-provider-tebib/internal/client"
)

// simpleResource implements a project resource whose only in-place changes are its
// description and tags (PATCH); every other argument replaces it. VPCs, subnets, security
// groups, NAT gateways, public IPs, SSH keys and snapshots work this way.
//
// A is the API type, M the Terraform model (a struct embedding projectMeta), PM is *M.
type simpleResource[A client.ProjectResource, M any, PM interface {
	*M
	simpleModel
}] struct {
	base
	spec simpleSpec[A, M]
}

type simpleModel interface {
	meta() *projectMeta
	timeoutsValue() timeouts.Value
}

type simpleSpec[A client.ProjectResource, M any] struct {
	typeName   string // tebib_<typeName>
	noun       string // "VPC"
	plural     string // "VPCs", for the name's description
	collection string // API path segment: "vpcs"
	summary    string // resource description (markdown)
	idDesc     string
	attrs      map[string]schema.Attribute
	timeouts   [3]time.Duration // create, update, delete
	// body is the create request.
	body func(ctx context.Context, m *M, diags *diag.Diagnostics) any
	// read copies the resource-specific fields (meta is copied for you).
	read func(m *M, a *A)
}

func (r *simpleResource[A, M, PM]) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_" + r.spec.typeName
}

func (r *simpleResource[A, M, PM]) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: r.spec.summary,
		Attributes:          mergeAttrs(metaAttrs(r.spec.plural, r.spec.idDesc), r.spec.attrs),
		Blocks: map[string]schema.Block{
			"timeouts": timeouts.Block(ctx, timeouts.Opts{Create: true, Update: true, Delete: true}),
		},
	}
}

func (r *simpleResource[A, M, PM]) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.configure(req.ProviderData, &resp.Diagnostics)
}

func (r *simpleResource[A, M, PM]) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	importProjectScoped(ctx, req, resp)
}

func (r *simpleResource[A, M, PM]) fill(m PM, a *A) {
	m.meta().fromAPI((*a).GetMeta())
	r.spec.read((*M)(m), a)
}

func (r *simpleResource[A, M, PM]) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	plan := PM(new(M))
	resp.Diagnostics.Append(req.Plan.Get(ctx, plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	project := r.projectID(plan.meta().ProjectID, &resp.Diagnostics)
	body := r.spec.body(ctx, (*M)(plan), &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := r.deadline(ctx, plan, 0, &resp.Diagnostics)
	defer cancel()

	var created A
	if err := r.client().Create(ctx, client.ProjectPath(project, r.spec.collection), body, &created); err != nil {
		apiError(&resp.Diagnostics, fmt.Sprintf("Creating the %s", r.spec.noun), err)
		return
	}
	r.fill(plan, &created)
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
	settled, err := client.Wait[A](ctx, r.client(), client.ProjectPath(project, r.spec.collection, created.GetMeta().ID), nil)
	if settled != nil {
		r.fill(plan, settled)
		resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
	}
	if err != nil {
		waitError(&resp.Diagnostics, "the "+r.spec.noun, err)
	}
}

func (r *simpleResource[A, M, PM]) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	state := PM(new(M))
	resp.Diagnostics.Append(req.State.Get(ctx, state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	project := r.projectID(state.meta().ProjectID, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	var got A
	err := r.client().Get(ctx, client.ProjectPath(project, r.spec.collection, state.meta().ID.ValueString()), &got)
	if client.IsNotFound(err) || (err == nil && got.Life().State == "deleted") {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, fmt.Sprintf("Reading the %s", r.spec.noun), err)
		return
	}
	r.fill(state, &got)
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *simpleResource[A, M, PM]) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	plan, state := PM(new(M)), PM(new(M))
	resp.Diagnostics.Append(req.Plan.Get(ctx, plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := r.deadline(ctx, plan, 1, &resp.Diagnostics)
	defer cancel()
	p := client.ProjectPath(state.meta().ProjectID.ValueString(), r.spec.collection, state.meta().ID.ValueString())
	if patch := tagsPatch(ctx, *plan.meta(), *state.meta(), &resp.Diagnostics); patch != nil {
		if resp.Diagnostics.HasError() {
			return
		}
		if err := r.client().Patch(ctx, p, patch, nil); err != nil {
			apiError(&resp.Diagnostics, fmt.Sprintf("Updating the %s", r.spec.noun), err)
			return
		}
	}
	settled, err := client.Wait[A](ctx, r.client(), p, nil)
	if err != nil {
		waitError(&resp.Diagnostics, "the "+r.spec.noun, err)
		return
	}
	r.fill(plan, settled)
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *simpleResource[A, M, PM]) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	state := PM(new(M))
	resp.Diagnostics.Append(req.State.Get(ctx, state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := r.deadline(ctx, state, 2, &resp.Diagnostics)
	defer cancel()
	p := client.ProjectPath(state.meta().ProjectID.ValueString(), r.spec.collection, state.meta().ID.ValueString())
	if err := deleteAndWait[A](ctx, r.client(), p); err != nil {
		apiError(&resp.Diagnostics, fmt.Sprintf("Deleting the %s", r.spec.noun), err)
	}
}

// deadline bounds ctx by the create (0), update (1) or delete (2) timeout.
func (r *simpleResource[A, M, PM]) deadline(ctx context.Context, m PM, which int, diags *diag.Diagnostics) (context.Context, context.CancelFunc) {
	def := r.spec.timeouts[which]
	if def == 0 {
		def = 10 * time.Minute
	}
	t := m.timeoutsValue()
	var d time.Duration
	var ds diag.Diagnostics
	switch which {
	case 0:
		d, ds = t.Create(ctx, def)
	case 1:
		d, ds = t.Update(ctx, def)
	default:
		d, ds = t.Delete(ctx, def)
	}
	return withTimeout(ctx, d, ds, diags)
}
