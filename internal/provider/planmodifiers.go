package provider

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
)

// keepUnless is UseStateForUnknown, except when one of the named attributes changes in the
// plan: then the value is left unknown, because the change can move it (a resize changes
// a VM's vCPUs; starting a stopped VM gives it addresses).
type keepUnless struct {
	watch []string
}

func (m keepUnless) Description(context.Context) string {
	return fmt.Sprintf("Keeps the prior value unless %s changes.", strings.Join(m.watch, " or "))
}

func (m keepUnless) MarkdownDescription(ctx context.Context) string { return m.Description(ctx) }

func (m keepUnless) keep(ctx context.Context, state tfsdk.State, plan tfsdk.Plan, prior attr.Value, planned attr.Value) bool {
	if prior == nil || prior.IsNull() || !planned.IsUnknown() || plan.Raw.IsNull() || state.Raw.IsNull() {
		return false
	}
	for _, name := range m.watch {
		var before, after attr.Value
		if d := state.GetAttribute(ctx, path.Root(name), &before); d.HasError() {
			return false
		}
		if d := plan.GetAttribute(ctx, path.Root(name), &after); d.HasError() {
			return false
		}
		if after.IsUnknown() || !before.Equal(after) {
			return false
		}
	}
	return true
}

type keepStringUnless struct{ keepUnless }

func (m keepStringUnless) PlanModifyString(ctx context.Context, req planmodifier.StringRequest, resp *planmodifier.StringResponse) {
	if m.keep(ctx, req.State, req.Plan, req.StateValue, req.PlanValue) {
		resp.PlanValue = req.StateValue
	}
}

type keepInt64Unless struct{ keepUnless }

func (m keepInt64Unless) PlanModifyInt64(ctx context.Context, req planmodifier.Int64Request, resp *planmodifier.Int64Response) {
	if m.keep(ctx, req.State, req.Plan, req.StateValue, req.PlanValue) {
		resp.PlanValue = req.StateValue
	}
}

type keepBoolUnless struct{ keepUnless }

func (m keepBoolUnless) PlanModifyBool(ctx context.Context, req planmodifier.BoolRequest, resp *planmodifier.BoolResponse) {
	if m.keep(ctx, req.State, req.Plan, req.StateValue, req.PlanValue) {
		resp.PlanValue = req.StateValue
	}
}

func stringKeepUnless(watch ...string) planmodifier.String {
	return keepStringUnless{keepUnless{watch}}
}

func int64KeepUnless(watch ...string) planmodifier.Int64 {
	return keepInt64Unless{keepUnless{watch}}
}

func boolKeepUnless(watch ...string) planmodifier.Bool {
	return keepBoolUnless{keepUnless{watch}}
}

// replaceIfShrinks replaces a resource when a size goes down (sizes only grow in place).
func replaceIfShrinks() planmodifier.Int64 {
	return int64planmodifier.RequiresReplaceIf(
		func(ctx context.Context, req planmodifier.Int64Request, resp *int64planmodifier.RequiresReplaceIfFuncResponse) {
			if req.StateValue.IsNull() || req.PlanValue.IsUnknown() || req.PlanValue.IsNull() {
				return
			}
			resp.RequiresReplace = req.PlanValue.ValueInt64() < req.StateValue.ValueInt64()
		},
		"Disks only grow in place: a smaller size replaces the resource (its data is lost).",
		"Disks only grow in place: a smaller size replaces the resource (its data is lost).",
	)
}
