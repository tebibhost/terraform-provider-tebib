package provider

import (
	"regexp"

	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
)

// Small constructors for resource schema attributes.

func stableBool(desc string) schema.BoolAttribute {
	return schema.BoolAttribute{
		Computed: true, MarkdownDescription: desc,
		PlanModifiers: []planmodifier.Bool{boolplanmodifier.UseStateForUnknown()},
	}
}

func volatileBool(desc string) schema.BoolAttribute {
	return schema.BoolAttribute{Computed: true, MarkdownDescription: desc}
}

func stableInt(desc string) schema.Int64Attribute {
	return schema.Int64Attribute{
		Computed: true, MarkdownDescription: desc,
		PlanModifiers: []planmodifier.Int64{int64planmodifier.UseStateForUnknown()},
	}
}

func volatileInt(desc string) schema.Int64Attribute {
	return schema.Int64Attribute{Computed: true, MarkdownDescription: desc}
}

// optionalComputedReplace is set by the user or by the platform; changing a configured
// value replaces the resource.
func optionalComputedReplace(desc string) schema.StringAttribute {
	return schema.StringAttribute{
		Optional: true, Computed: true, MarkdownDescription: desc,
		PlanModifiers: []planmodifier.String{
			stringplanmodifier.RequiresReplaceIfConfigured(),
			stringplanmodifier.UseStateForUnknown(),
		},
	}
}

func boolRequiresReplace() planmodifier.Bool { return boolplanmodifier.RequiresReplace() }

func regexpMust(pattern string) *regexp.Regexp { return regexp.MustCompile(pattern) }
