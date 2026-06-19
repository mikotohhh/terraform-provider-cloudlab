package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

// Ensure allAvailabilityDataSource satisfies the datasource.DataSource interface.
var _ datasource.DataSource = &allAvailabilityDataSource{}

// NewAllAvailabilityDataSource returns a new "all availability" data source.
func NewAllAvailabilityDataSource() datasource.DataSource {
	return &allAvailabilityDataSource{}
}

// allAvailabilityDataSource answers "across every node type CloudLab offers,
// when is the earliest each one is available for the requested duration?".
//
// The Portal REST API cannot enumerate node types, so this data source
// delegates discovery to an external command (discover_command) — typically a
// geni-lib script that queries the GENI AM advertisement RSpec — and then runs
// a Portal reservation search (Client.SearchResgroup) once per discovered type.
//
// The discover_command must print a JSON array to stdout, each element:
//
//	{"cluster": "Wisconsin",
//	 "urn": "urn:publicid:IDN+wisc.cloudlab.us+authority+cm",
//	 "node_type": "d8545", "free": 3, "total": 10}
type allAvailabilityDataSource struct {
	client *Client
}

// discoveredNodeType is one entry emitted by discover_command.
type discoveredNodeType struct {
	Cluster  string `json:"cluster"`
	URN      string `json:"urn"`
	NodeType string `json:"node_type"`
	Free     int64  `json:"free"`
	Total    int64  `json:"total"`
}

// allAvailabilityResultModel is one row of the output.
type allAvailabilityResultModel struct {
	Cluster   types.String `tfsdk:"cluster"`
	URN       types.String `tfsdk:"urn"`
	NodeType  types.String `tfsdk:"node_type"`
	Free      types.Int64  `tfsdk:"free"`
	Total     types.Int64  `tfsdk:"total"`
	StartAt   types.String `tfsdk:"start_at"`
	ExpiresAt types.String `tfsdk:"expires_at"`
	Error     types.String `tfsdk:"error"`
}

// allAvailabilityDataSourceModel maps the data source schema data.
type allAvailabilityDataSourceModel struct {
	Project         types.String                 `tfsdk:"project"`
	Group           types.String                 `tfsdk:"group"`
	DurationHours   types.Int64                  `tfsdk:"duration_hours"`
	DiscoverCommand []types.String               `tfsdk:"discover_command"`
	OnlyNodeTypes   []types.String               `tfsdk:"only_node_types"`
	OnlyWithFree    types.Bool                   `tfsdk:"only_with_free"`
	Results         []allAvailabilityResultModel `tfsdk:"results"`
}

// Metadata returns the data source type name.
func (d *allAvailabilityDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_all_availability"
}

// Schema defines the schema for the data source.
func (d *allAvailabilityDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Surveys the earliest reservation availability across every node type CloudLab offers. " +
			"Because the Portal API cannot enumerate node types, this data source runs an external " +
			"`discover_command` (typically a geni-lib script that reads the GENI AM advertisement RSpec) to " +
			"obtain the full list of (cluster, node type) pairs, then performs a Portal reservation search " +
			"(POST /resgroups/search) for the requested duration once per type. " +
			"NOTE: this requires the discover_command's dependencies (Python, geni-lib, an Emulab " +
			"certificate, and the CLOUDLAB_PASS environment variable) to be present wherever Terraform runs.",
		Attributes: map[string]schema.Attribute{
			"project": schema.StringAttribute{
				Description: "The CloudLab project the reservations would belong to.",
				Required:    true,
			},
			"group": schema.StringAttribute{
				Description: "The project subgroup (optional).",
				Optional:    true,
			},
			"duration_hours": schema.Int64Attribute{
				Description: "How long each reservation is needed, in hours (e.g. 168 for 7 days).",
				Required:    true,
			},
			"discover_command": schema.ListAttribute{
				Description: "Command (program + args) to execute for node-type discovery. It must print a JSON " +
					"array of {cluster, urn, node_type, free, total} objects to stdout. " +
					"Example: [\"python3\", \"/Users/me/cloudlab_nodetypes.py\"].",
				ElementType: types.StringType,
				Required:    true,
			},
			"only_node_types": schema.ListAttribute{
				Description: "Optional allow-list of node types to search. If set, types not in this list are skipped.",
				ElementType: types.StringType,
				Optional:    true,
			},
			"only_with_free": schema.BoolAttribute{
				Description: "If true, only search node types that currently report at least one free node " +
					"(free > 0 from discovery). Defaults to false (search every discovered type).",
				Optional: true,
			},
			"results": schema.ListNestedAttribute{
				Description: "One entry per searched node type, with the earliest reservable window.",
				Computed:    true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"cluster":    schema.StringAttribute{Description: "Cluster name from discovery.", Computed: true},
						"urn":        schema.StringAttribute{Description: "Aggregate URN.", Computed: true},
						"node_type":  schema.StringAttribute{Description: "Hardware node type.", Computed: true},
						"free":       schema.Int64Attribute{Description: "Nodes free right now (from discovery).", Computed: true},
						"total":      schema.Int64Attribute{Description: "Total nodes of this type (from discovery).", Computed: true},
						"start_at":   schema.StringAttribute{Description: "Earliest reservable start (RFC3339), empty if search failed.", Computed: true},
						"expires_at": schema.StringAttribute{Description: "When that reservation would expire, empty if search failed.", Computed: true},
						"error":      schema.StringAttribute{Description: "Search error for this type, if any (e.g. no window found).", Computed: true},
					},
				},
			},
		},
	}
}

// Configure sets the provider-configured client on the data source.
func (d *allAvailabilityDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

// Read discovers all node types then searches availability for each.
func (d *allAvailabilityDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var state allAvailabilityDataSourceModel
	diags := req.Config.Get(ctx, &state)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	if len(state.DiscoverCommand) == 0 {
		resp.Diagnostics.AddError("Invalid Request", "discover_command must contain at least the program to run.")
		return
	}

	// 1. Run discovery command and parse its JSON output.
	argv := make([]string, 0, len(state.DiscoverCommand))
	for _, a := range state.DiscoverCommand {
		argv = append(argv, a.ValueString())
	}

	tflog.Debug(ctx, "Running node-type discovery command", map[string]any{"argv": argv})

	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	out, err := cmd.Output()
	if err != nil {
		detail := err.Error()
		if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
			detail = fmt.Sprintf("%s\nstderr: %s", detail, string(ee.Stderr))
		}
		resp.Diagnostics.AddError("Node-Type Discovery Failed",
			fmt.Sprintf("discover_command %v failed: %s", argv, detail))
		return
	}

	var discovered []discoveredNodeType
	if err := json.Unmarshal(out, &discovered); err != nil {
		resp.Diagnostics.AddError("Invalid Discovery Output",
			fmt.Sprintf("discover_command did not produce a valid JSON array of node types: %s", err.Error()))
		return
	}

	// Build optional allow-list set.
	var allow map[string]bool
	if len(state.OnlyNodeTypes) > 0 {
		allow = make(map[string]bool, len(state.OnlyNodeTypes))
		for _, t := range state.OnlyNodeTypes {
			allow[t.ValueString()] = true
		}
	}
	onlyWithFree := state.OnlyWithFree.ValueBool()

	// 2. Search availability for each discovered (and selected) node type.
	results := make([]allAvailabilityResultModel, 0, len(discovered))
	for _, nt := range discovered {
		if allow != nil && !allow[nt.NodeType] {
			continue
		}
		if onlyWithFree && nt.Free <= 0 {
			continue
		}

		row := allAvailabilityResultModel{
			Cluster:   types.StringValue(nt.Cluster),
			URN:       types.StringValue(nt.URN),
			NodeType:  types.StringValue(nt.NodeType),
			Free:      types.Int64Value(nt.Free),
			Total:     types.Int64Value(nt.Total),
			StartAt:   types.StringNull(),
			ExpiresAt: types.StringNull(),
			Error:     types.StringNull(),
		}

		searchReq := &ResgroupSearchRequest{
			Project: state.Project.ValueString(),
			NodeTypes: &ResgroupNodeTypes{NodeTypes: []ResgroupNodeType{{
				URN:      nt.URN,
				NodeType: nt.NodeType,
				Count:    1,
			}}},
		}
		if !state.Group.IsNull() && !state.Group.IsUnknown() {
			searchReq.Group = state.Group.ValueString()
		}

		res, err := d.client.SearchResgroup(ctx, searchReq, state.DurationHours.ValueInt64())
		if err != nil {
			row.Error = types.StringValue(err.Error())
		} else {
			row.StartAt = types.StringValue(res.StartAt)
			row.ExpiresAt = types.StringValue(res.ExpiresAt)
		}
		results = append(results, row)
	}

	state.Results = results

	diags = resp.State.Set(ctx, state)
	resp.Diagnostics.Append(diags...)
}
