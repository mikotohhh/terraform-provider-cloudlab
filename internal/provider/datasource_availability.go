package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

// Ensure availabilityDataSource satisfies the datasource.DataSource interface.
var _ datasource.DataSource = &availabilityDataSource{}

// NewAvailabilityDataSource returns a new availability data source.
func NewAvailabilityDataSource() datasource.DataSource {
	return &availabilityDataSource{}
}

// availabilityDataSource answers "for this set of node types, held for this
// duration, what is the earliest time slot the whole group can be scheduled?"
// It wraps the Portal API's POST /resgroups/search (Client.SearchResgroup),
// which returns a single {start_at, expires_at} window where the entire
// requested group fits — it does NOT return a per-type window.
type availabilityDataSource struct {
	client *Client
}

// availabilityNodeTypeModel maps one requested node type.
type availabilityNodeTypeModel struct {
	URN      types.String `tfsdk:"urn"`
	NodeType types.String `tfsdk:"node_type"`
	Count    types.Int64  `tfsdk:"count"`
}

// availabilityDataSourceModel maps the data source schema data.
type availabilityDataSourceModel struct {
	Project       types.String                `tfsdk:"project"`
	Group         types.String                `tfsdk:"group"`
	DurationHours types.Int64                 `tfsdk:"duration_hours"`
	NodeTypes     []availabilityNodeTypeModel `tfsdk:"node_types"`
	StartAt       types.String                `tfsdk:"start_at"`
	ExpiresAt     types.String                `tfsdk:"expires_at"`
}

// Metadata returns the data source type name.
func (d *availabilityDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_availability"
}

// Schema defines the schema for the data source.
func (d *availabilityDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Finds the earliest time slot where a set of nodes can be reserved for a given duration. " +
			"Wraps the CloudLab Portal reservation search (POST /resgroups/search): given one or more node " +
			"types and a duration in hours, it returns the single earliest window (start_at..expires_at) where " +
			"the entire requested group can be scheduled together. " +
			"Use this to answer \"I want N nodes of type X for 7 days — when is the earliest I can start?\". " +
			"An error is returned if no window can accommodate the request.",
		Attributes: map[string]schema.Attribute{
			"project": schema.StringAttribute{
				Description: "The CloudLab project the reservation would belong to.",
				Required:    true,
			},
			"group": schema.StringAttribute{
				Description: "The project subgroup (optional).",
				Optional:    true,
			},
			"duration_hours": schema.Int64Attribute{
				Description: "How long the reservation is needed, in hours (e.g. 168 for 7 days).",
				Required:    true,
			},
			"node_types": schema.ListNestedAttribute{
				Description: "The node types to reserve. The returned window fits all of them together.",
				Required:    true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"urn": schema.StringAttribute{
							Description: "The aggregate URN, e.g. urn:publicid:IDN+wisc.cloudlab.us+authority+cm.",
							Required:    true,
						},
						"node_type": schema.StringAttribute{
							Description: "The hardware node type, e.g. d8545.",
							Required:    true,
						},
						"count": schema.Int64Attribute{
							Description: "Number of nodes of this type (default 1).",
							Optional:    true,
						},
					},
				},
			},
			"start_at": schema.StringAttribute{
				Description: "The earliest time the reservation can start (RFC3339).",
				Computed:    true,
			},
			"expires_at": schema.StringAttribute{
				Description: "When the reservation would expire (start_at + duration, RFC3339).",
				Computed:    true,
			},
		},
	}
}

// Configure sets the provider-configured client on the data source.
func (d *availabilityDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}

	client, ok := req.ProviderData.(*Client)
	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected Data Source Configure Type",
			fmt.Sprintf("Expected *provider.Client, got: %T. Please report this issue to the provider developers.", req.ProviderData),
		)
		return
	}

	d.client = client
}

// Read performs the reservation search.
func (d *availabilityDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var state availabilityDataSourceModel
	diags := req.Config.Get(ctx, &state)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	if len(state.NodeTypes) == 0 {
		resp.Diagnostics.AddError("Invalid Request", "node_types must contain at least one entry.")
		return
	}

	nodeTypes := make([]ResgroupNodeType, 0, len(state.NodeTypes))
	for _, nt := range state.NodeTypes {
		count := int64(1)
		if !nt.Count.IsNull() && !nt.Count.IsUnknown() {
			count = nt.Count.ValueInt64()
		}
		nodeTypes = append(nodeTypes, ResgroupNodeType{
			URN:      nt.URN.ValueString(),
			NodeType: nt.NodeType.ValueString(),
			Count:    count,
		})
	}

	searchReq := &ResgroupSearchRequest{
		Project:   state.Project.ValueString(),
		NodeTypes: &ResgroupNodeTypes{NodeTypes: nodeTypes},
	}
	if !state.Group.IsNull() && !state.Group.IsUnknown() {
		searchReq.Group = state.Group.ValueString()
	}

	tflog.Debug(ctx, "Searching CloudLab reservation availability", map[string]any{
		"project":        searchReq.Project,
		"duration_hours": state.DurationHours.ValueInt64(),
		"node_types":     len(nodeTypes),
	})

	result, err := d.client.SearchResgroup(ctx, searchReq, state.DurationHours.ValueInt64())
	if err != nil {
		resp.Diagnostics.AddError(
			"Error Searching Reservation Availability",
			fmt.Sprintf("No available window was found, or the search failed: %s", err.Error()),
		)
		return
	}

	state.StartAt = types.StringValue(result.StartAt)
	state.ExpiresAt = types.StringValue(result.ExpiresAt)

	diags = resp.State.Set(ctx, state)
	resp.Diagnostics.Append(diags...)
}
