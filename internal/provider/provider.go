// Package provider is the TebibHost Terraform/OpenTofu provider.
package provider

import (
	"context"
	"fmt"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/tebibhost/terraform-provider-tebib/internal/client"
)

var _ provider.Provider = (*tebibProvider)(nil)

type tebibProvider struct {
	version string
}

// New returns a provider factory for the plugin server.
func New(version string) func() provider.Provider {
	return func() provider.Provider { return &tebibProvider{version: version} }
}

type providerModel struct {
	Endpoint        types.String `tfsdk:"endpoint"`
	AccessKeyID     types.String `tfsdk:"access_key_id"`
	SecretAccessKey types.String `tfsdk:"secret_access_key"`
	Profile         types.String `tfsdk:"profile"`
	Organization    types.String `tfsdk:"organization"`
	Project         types.String `tfsdk:"project"`
	PollInterval    types.String `tfsdk:"poll_interval"`
	MaxRetries      types.Int64  `tfsdk:"max_retries"`
}

// providerData is what resources and data sources get from Configure.
type providerData struct {
	client       *client.Client
	organization string
	project      string
}

func (p *tebibProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "tebib"
	resp.Version = p.version
}

func (p *tebibProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "The TebibHost provider manages resources on the TebibHost cloud " +
			"(projects, VPCs, VMs, volumes, public IPs, IAM) through its public API.\n\n" +
			"It reads the same settings as the `tebib` CLI. Each setting comes from the first of: " +
			"the provider block, an environment variable, the CLI profile files " +
			"(`~/.config/tebib/config.toml` and `credentials.toml`), the default.",
		Attributes: map[string]schema.Attribute{
			"endpoint": schema.StringAttribute{
				Optional: true,
				MarkdownDescription: "API endpoint. Default: `https://cloud.tebibhost.com`. " +
					"Environment: `TEBIB_ENDPOINT`. Each region has its own endpoint.",
			},
			"access_key_id": schema.StringAttribute{
				Optional: true,
				MarkdownDescription: "Access key ID (`TBK...`), or a role session ID (`TBS...`). " +
					"Environment: `TEBIB_ACCESS_KEY_ID`.",
			},
			"secret_access_key": schema.StringAttribute{
				Optional:            true,
				Sensitive:           true,
				MarkdownDescription: "Secret of the access key. Environment: `TEBIB_SECRET_ACCESS_KEY`.",
			},
			"profile": schema.StringAttribute{
				Optional: true,
				MarkdownDescription: "CLI profile to read settings from (see `tebib configure`). " +
					"Environment: `TEBIB_PROFILE`. Default: the CLI's default profile.",
			},
			"organization": schema.StringAttribute{
				Optional: true,
				MarkdownDescription: "Default organization ID (`o-...`) for projects and IAM resources. " +
					"Environment: `TEBIB_ORGANIZATION`.",
			},
			"project": schema.StringAttribute{
				Optional: true,
				MarkdownDescription: "Default project ID (`p-...`) for project resources " +
					"(VPCs, VMs, volumes...). Environment: `TEBIB_PROJECT`.",
			},
			"poll_interval": schema.StringAttribute{
				Optional: true,
				MarkdownDescription: "How often to check on asynchronous changes, as a duration " +
					"like `2s`. Default: `2s`.",
			},
			"max_retries": schema.Int64Attribute{
				Optional: true,
				MarkdownDescription: "Attempts for a call that failed with a network error, 429, " +
					"502, 503 or 504 (only calls that are safe to repeat). Default: 4.",
			},
		},
	}
}

func (p *tebibProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var cfg providerModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	for name, v := range map[string]types.String{
		"endpoint": cfg.Endpoint, "access_key_id": cfg.AccessKeyID,
		"secret_access_key": cfg.SecretAccessKey, "profile": cfg.Profile,
		"organization": cfg.Organization, "project": cfg.Project,
	} {
		if v.IsUnknown() {
			resp.Diagnostics.AddError("Unknown provider setting",
				fmt.Sprintf("%s is not known until apply. The provider needs it to plan: "+
					"set it from a value known in advance, or use an environment variable.", name))
		}
	}
	if resp.Diagnostics.HasError() {
		return
	}
	s, err := resolveSettings(explicit{
		Profile:         cfg.Profile.ValueString(),
		Endpoint:        cfg.Endpoint.ValueString(),
		Organization:    cfg.Organization.ValueString(),
		Project:         cfg.Project.ValueString(),
		AccessKeyID:     cfg.AccessKeyID.ValueString(),
		SecretAccessKey: cfg.SecretAccessKey.ValueString(),
	})
	if err != nil {
		resp.Diagnostics.AddError("TebibHost provider settings", err.Error())
		return
	}
	poll := 2 * time.Second
	if v := cfg.PollInterval.ValueString(); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 {
			resp.Diagnostics.AddAttributeError(pathRoot("poll_interval"), "Invalid poll_interval",
				fmt.Sprintf("%q is not a positive duration like 2s", v))
			return
		}
		poll = d
	}
	c, err := client.New(client.Config{
		Endpoint:        s.Endpoint,
		AccessKeyID:     s.AccessKeyID,
		SecretAccessKey: s.SecretAccessKey,
		UserAgent:       fmt.Sprintf("terraform-provider-tebib/%s (+%s)", p.version, req.TerraformVersion),
		Retries:         int(cfg.MaxRetries.ValueInt64()),
		PollInterval:    poll,
	})
	if err != nil {
		resp.Diagnostics.AddError("TebibHost provider settings", err.Error())
		return
	}
	data := &providerData{client: c, organization: s.Organization, project: s.Project}
	resp.ResourceData = data
	resp.DataSourceData = data
}

func (p *tebibProvider) Resources(_ context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		newProjectResource,
		newVpcResource,
		newSubnetResource,
		newSecurityGroupResource,
		newSecurityGroupRuleResource,
		newNatGatewayResource,
		newPublicIPResource,
		newPublicIPAssociationResource,
		newSSHKeyResource,
		newVMResource,
		newVolumeResource,
		newVolumeAttachmentResource,
		newSnapshotResource,
		newServiceAccountResource,
		newAccessKeyResource,
		newRoleResource,
		newPolicyResource,
		newPolicyAttachmentResource,
		newGroupResource,
		newGroupMembershipResource,
	}
}

func (p *tebibProvider) DataSources(_ context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{
		newProjectDataSource,
		newImagesDataSource,
		newInstanceTypesDataSource,
		newPricesDataSource,
		newVpcDataSource,
		newSubnetDataSource,
		newSecurityGroupDataSource,
	}
}
