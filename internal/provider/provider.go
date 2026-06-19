package provider

import (
	"context"
	"os"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// Ensure cloudlabProvider satisfies the provider.Provider interface.
var _ provider.Provider = &cloudlabProvider{}

// New returns a function that constructs the provider.
func New(version string) func() provider.Provider {
	return func() provider.Provider {
		return &cloudlabProvider{version: version}
	}
}

// cloudlabProvider is the provider implementation.
type cloudlabProvider struct {
	version string
}

// cloudlabProviderModel maps provider schema data to a Go type.
type cloudlabProviderModel struct {
	Token     types.String `tfsdk:"token"`
	PortalURL types.String `tfsdk:"portal_url"`
}

// Metadata returns the provider type name.
func (p *cloudlabProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "cloudlab"
	resp.Version = p.version
}

// Schema defines the provider-level schema for configuration data.
func (p *cloudlabProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "The CloudLab provider manages resources on CloudLab (cloudlab.us), " +
			"the academic cloud and network testbed.",
		Attributes: map[string]schema.Attribute{
			"token": schema.StringAttribute{
				Description: "CloudLab Portal API token. Can also be set via the CLOUDLAB_TOKEN environment variable.",
				Optional:    true,
				Sensitive:   true,
			},
			"portal_url": schema.StringAttribute{
				Description: "CloudLab portal base URL. Defaults to https://www.cloudlab.us. " +
					"Can also be set via the CLOUDLAB_PORTAL_URL environment variable.",
				Optional: true,
			},
		},
	}
}

// Configure prepares a CloudLab API client for data sources and resources.
func (p *cloudlabProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var config cloudlabProviderModel
	diags := req.Config.Get(ctx, &config)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	if config.Token.IsUnknown() {
		resp.Diagnostics.AddAttributeError(
			path.Root("token"),
			"Unknown CloudLab API Token",
			"The provider cannot authenticate with CloudLab as the token value is unknown. "+
				"Either target apply the source of the value first, set the value statically in the configuration, "+
				"or use the CLOUDLAB_TOKEN environment variable.",
		)
	}

	if resp.Diagnostics.HasError() {
		return
	}

	token := os.Getenv("CLOUDLAB_TOKEN")
	if !config.Token.IsNull() && !config.Token.IsUnknown() {
		token = config.Token.ValueString()
	}

	portalURL := os.Getenv("CLOUDLAB_PORTAL_URL")
	if portalURL == "" {
		portalURL = defaultPortalURL
	}
	if !config.PortalURL.IsNull() && !config.PortalURL.IsUnknown() {
		portalURL = config.PortalURL.ValueString()
	}

	if token == "" {
		resp.Diagnostics.AddAttributeError(
			path.Root("token"),
			"Missing CloudLab API Token",
			"The provider requires a CloudLab API token to authenticate. "+
				"Set the token attribute in the provider configuration or use the CLOUDLAB_TOKEN environment variable.",
		)
		return
	}

	client := NewClient(portalURL, token)
	resp.DataSourceData = client
	resp.ResourceData = client
}

// DataSources defines the data sources implemented in the provider.
func (p *cloudlabProvider) DataSources(_ context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{
		NewExperimentDataSource,
		NewManifestDataSource,
		NewProfileDataSource,
		NewResgroupDataSource,
		NewNodeDataSource,
		NewAvailabilityDataSource,
		NewAllAvailabilityDataSource,
	}
}

// Resources defines the resources implemented in the provider.
func (p *cloudlabProvider) Resources(_ context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		NewExperimentResource,
		NewProfileResource,
		NewResgroupResource,
		NewVlanConnectionResource,
		NewSnapshotResource,
	}
}
