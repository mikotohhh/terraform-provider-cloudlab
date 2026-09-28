package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

// Ensure allAvailabilityDataSource satisfies the datasource.DataSource interface.
var _ datasource.DataSource = &allAvailabilityDataSource{}

const (
	defaultAvailabilitySearchConcurrency int64 = 16
	maxAvailabilitySearchConcurrency     int64 = 16
	defaultSearchLaunchIntervalMS        int64 = 200
	minSearchLaunchIntervalMS            int64 = 200
	maxSearchLaunchIntervalMS            int64 = 2000
	availabilityJobBuffer                      = 256

	// Portal searches may overlap, but launching requests with the same token
	// too close together triggers a transient 401 "No such user" in the Portal
	// session layer. Live tests succeeded at 200-250 ms spacing and failed at
	// 0-100 ms. Read-only searches retry the remaining transient 401/5xx errors.
)

// resgroupSearcher is the narrow part of Client used by this data source. It
// keeps the scheduling pipeline independently testable without a live portal.
type resgroupSearcher interface {
	SearchResgroup(context.Context, *ResgroupSearchRequest, int64) (*ResgroupSearchResult, error)
}

// NewAllAvailabilityDataSource returns a new "all availability" data source.
func NewAllAvailabilityDataSource() datasource.DataSource {
	return &allAvailabilityDataSource{}
}

// allAvailabilityDataSource answers "across every allocatable physical node
// type CloudLab offers, when is the earliest each one is available for the
// requested duration?".
//
// This data source delegates discovery to an external command
// (discover_command) — typically the included central Portal inventory script
// with strict GENI fallback — and then runs a Portal reservation search
// (Client.SearchResgroup) once per discovered type.
//
// The discover_command must print either a JSON array or a stream of JSON
// objects to stdout, each object shaped like:
//
//	{"cluster": "Wisconsin",
//	 "urn": "urn:publicid:IDN+wisc.cloudlab.us+authority+cm",
//	 "node_type": "d8545", "free": 3, "total": 10}
type allAvailabilityDataSource struct {
	client resgroupSearcher
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
	Project           types.String                 `tfsdk:"project"`
	Group             types.String                 `tfsdk:"group"`
	DurationHours     types.Int64                  `tfsdk:"duration_hours"`
	DiscoverCommand   []types.String               `tfsdk:"discover_command"`
	OnlyNodeTypes     []types.String               `tfsdk:"only_node_types"`
	OnlyWithFree      types.Bool                   `tfsdk:"only_with_free"`
	SearchConcurrency types.Int64                  `tfsdk:"search_concurrency"`
	SearchIntervalMS  types.Int64                  `tfsdk:"search_launch_interval_ms"`
	Results           []allAvailabilityResultModel `tfsdk:"results"`
}

// Metadata returns the data source type name.
func (d *allAvailabilityDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_all_availability"
}

// Schema defines the schema for the data source.
func (d *allAvailabilityDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Surveys the earliest reservation availability across allocatable physical node types CloudLab offers. " +
			"This data source runs an external `discover_command` (typically the included script, which uses " +
			"the central Portal inventory with GENI advertisement RSpecs as a fallback) to " +
			"obtain the full list of (cluster, node type) pairs, then performs a Portal reservation search " +
			"(POST /resgroups/search) for the requested duration once per type.",
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
				Description: "Command (program + args) to execute for node-type discovery. It must print " +
					"{cluster, urn, node_type, free, total} objects to stdout — either as one JSON array, or " +
					"as a stream of concatenated/newline-delimited objects. When a producer emits rows " +
					"incrementally, their Portal searches begin immediately. " +
					"Example: [\"python3\", \"/Users/me/cloudlab_nodetypes.py\", \"--stream\"].",
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
			"search_concurrency": schema.Int64Attribute{
				Description: "Maximum number of reservation searches to run concurrently, from 1 through 16 " +
					"(default 16). Requests are also launch-spaced to avoid a Portal token-session race.",
				Optional: true,
			},
			"search_launch_interval_ms": schema.Int64Attribute{
				Description: "Minimum spacing between reservation-search starts in milliseconds, from 200 " +
					"through 2000 (default 200). Lower spacing is faster but triggers transient 401 responses " +
					"on the current Portal implementation.",
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
	readStarted := time.Now()
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

	searchConcurrency := defaultAvailabilitySearchConcurrency
	if !state.SearchConcurrency.IsNull() && !state.SearchConcurrency.IsUnknown() {
		searchConcurrency = state.SearchConcurrency.ValueInt64()
	}
	if searchConcurrency < 1 || searchConcurrency > maxAvailabilitySearchConcurrency {
		resp.Diagnostics.AddError(
			"Invalid Search Concurrency",
			fmt.Sprintf("search_concurrency must be between 1 and %d.", maxAvailabilitySearchConcurrency),
		)
		return
	}
	searchIntervalMS := defaultSearchLaunchIntervalMS
	if !state.SearchIntervalMS.IsNull() && !state.SearchIntervalMS.IsUnknown() {
		searchIntervalMS = state.SearchIntervalMS.ValueInt64()
	}
	if searchIntervalMS < minSearchLaunchIntervalMS || searchIntervalMS > maxSearchLaunchIntervalMS {
		resp.Diagnostics.AddError(
			"Invalid Search Launch Interval",
			fmt.Sprintf(
				"search_launch_interval_ms must be between %d and %d.",
				minSearchLaunchIntervalMS,
				maxSearchLaunchIntervalMS,
			),
		)
		return
	}

	// 1. Launch the discovery command and consume its stdout incrementally.
	// The command may print the legacy JSON array (all rows at the end) or a
	// stream of JSON objects (one per row). Rows begin their Portal searches the
	// moment they arrive, so incremental producers can overlap discovery and
	// reservation searches instead of running phase by phase.
	argv := make([]string, 0, len(state.DiscoverCommand))
	for _, a := range state.DiscoverCommand {
		argv = append(argv, a.ValueString())
	}

	tflog.Debug(ctx, "Running node-type discovery command", map[string]any{"argv": argv})

	discoveryStarted := time.Now()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		resp.Diagnostics.AddError("Node-Type Discovery Failed", err.Error())
		return
	}
	if err := cmd.Start(); err != nil {
		resp.Diagnostics.AddError("Node-Type Discovery Failed",
			fmt.Sprintf("discover_command %v failed to start: %s", argv, err.Error()))
		return
	}

	group := ""
	if !state.Group.IsNull() && !state.Group.IsUnknown() {
		group = state.Group.ValueString()
	}
	var allow map[string]bool
	if len(state.OnlyNodeTypes) > 0 {
		allow = make(map[string]bool, len(state.OnlyNodeTypes))
		for _, t := range state.OnlyNodeTypes {
			allow[t.ValueString()] = true
		}
	}

	survey := startAvailabilitySurvey(
		ctx,
		d.client,
		state.Project.ValueString(),
		group,
		state.DurationHours.ValueInt64(),
		int(searchConcurrency),
		time.Duration(searchIntervalMS)*time.Millisecond,
	)
	selector := &discoverySelector{
		allow:        allow,
		onlyWithFree: state.OnlyWithFree.ValueBool(),
		submit:       survey.submit,
	}

	consumeErr := consumeDiscoveryOutput(stdout, func(nt discoveredNodeType) error {
		return selector.accept(ctx, nt)
	})
	if consumeErr != nil {
		// Stop a still-running producer so cmd.Wait cannot block on it.
		_ = cmd.Process.Kill()
	}
	waitErr := cmd.Wait()
	tflog.Debug(ctx, "Node-type discovery command completed", map[string]any{
		"duration_ms": time.Since(discoveryStarted).Milliseconds(),
		"error":       waitErr != nil,
	})
	results := survey.wait()

	if consumeErr != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			resp.Diagnostics.AddError("Availability Survey Interrupted", ctxErr.Error())
		} else {
			resp.Diagnostics.AddError("Invalid Discovery Output", consumeErr.Error())
		}
		return
	}
	if waitErr != nil {
		detail := waitErr.Error()
		if message := strings.TrimSpace(stderr.String()); message != "" {
			detail = fmt.Sprintf("%s\nstderr: %s", detail, message)
		}
		resp.Diagnostics.AddError("Node-Type Discovery Failed",
			fmt.Sprintf("discover_command %v failed: %s", argv, detail))
		return
	}
	if message := strings.TrimSpace(stderr.String()); message != "" {
		resp.Diagnostics.AddWarning(
			"Node-Type Discovery Warning",
			"discover_command completed with warnings; availability may be incomplete:\n"+message,
		)
	}
	if ctx.Err() != nil {
		resp.Diagnostics.AddError("Availability Survey Interrupted", ctx.Err().Error())
		return
	}
	if selector.discovered == 0 {
		resp.Diagnostics.AddError(
			"Empty Discovery Result",
			"discover_command returned no node types; refusing to report an apparently successful but empty survey.",
		)
		return
	}
	if selector.selected == 0 {
		resp.Diagnostics.AddWarning(
			"No Node Types Selected",
			"Discovery succeeded, but no node types matched only_node_types and only_with_free.",
		)
	}

	selector.applyMergedCounts(results)
	// Stable ordering: results is a Terraform list and order-only changes
	// would create noisy state diffs.
	sort.Slice(results, func(i, j int) bool {
		if results[i].Cluster != results[j].Cluster {
			return results[i].Cluster.ValueString() < results[j].Cluster.ValueString()
		}
		if results[i].URN != results[j].URN {
			return results[i].URN.ValueString() < results[j].URN.ValueString()
		}
		return results[i].NodeType.ValueString() < results[j].NodeType.ValueString()
	})

	tflog.Debug(ctx, "Availability survey completed", map[string]any{
		"discovered_count":   selector.discovered,
		"selected_count":     selector.selected,
		"concurrency":        searchConcurrency,
		"launch_interval_ms": searchIntervalMS,
		"total_duration_ms":  time.Since(readStarted).Milliseconds(),
	})

	state.Results = results

	diags = resp.State.Set(ctx, state)
	resp.Diagnostics.Append(diags...)
}

// consumeDiscoveryOutput parses discover_command stdout and calls emit for
// each row as soon as it is available. Both output formats are accepted: one
// JSON array, or a stream of concatenated/newline-delimited JSON objects.
func consumeDiscoveryOutput(r io.Reader, emit func(discoveredNodeType) error) error {
	br := bufio.NewReader(r)
	var first byte
	for {
		b, err := br.ReadByte()
		if err == io.EOF {
			return nil // no rows; the caller reports the empty survey
		}
		if err != nil {
			return err
		}
		if b == ' ' || b == '\t' || b == '\n' || b == '\r' {
			continue
		}
		first = b
		if err := br.UnreadByte(); err != nil {
			return err
		}
		break
	}

	dec := json.NewDecoder(br)
	if first == '[' {
		var rows []discoveredNodeType
		if err := dec.Decode(&rows); err != nil {
			return fmt.Errorf("discover_command did not produce a valid JSON array of node types: %s", err.Error())
		}
		for _, row := range rows {
			if err := emit(row); err != nil {
				return err
			}
		}
		return nil
	}
	for {
		var row discoveredNodeType
		if err := dec.Decode(&row); err == io.EOF {
			return nil
		} else if err != nil {
			return fmt.Errorf("discover_command did not produce valid JSON node-type objects: %s", err.Error())
		}
		if err := emit(row); err != nil {
			return err
		}
	}
}

// discoverySelector validates, de-duplicates, and filters discovery rows as
// they arrive, submitting each selected row for an immediate Portal search.
type discoverySelector struct {
	allow        map[string]bool
	onlyWithFree bool
	submit       func(context.Context, discoveredNodeType) (int, error)

	discovered int
	selected   int
	seen       map[string]int   // row key -> submitted result index
	counts     map[int][2]int64 // result index -> merged {free, total}
}

func (s *discoverySelector) accept(ctx context.Context, nt discoveredNodeType) error {
	rowIndex := s.discovered
	s.discovered++

	nt.Cluster = strings.TrimSpace(nt.Cluster)
	nt.URN = strings.TrimSpace(nt.URN)
	nt.NodeType = strings.TrimSpace(nt.NodeType)
	if nt.Cluster == "" || nt.URN == "" || nt.NodeType == "" {
		return fmt.Errorf("discovery row %d must contain non-empty cluster, urn, and node_type", rowIndex)
	}
	if nt.Free < 0 || nt.Total < 0 || nt.Free > nt.Total {
		return fmt.Errorf(
			"discovery row %d (%s/%s) has invalid counts free=%d total=%d",
			rowIndex, nt.Cluster, nt.NodeType, nt.Free, nt.Total,
		)
	}

	if s.seen == nil {
		s.seen = make(map[string]int)
		s.counts = make(map[int][2]int64)
	}
	key := nt.Cluster + "\x00" + nt.URN + "\x00" + nt.NodeType
	if index, ok := s.seen[key]; ok {
		merged := s.counts[index]
		merged[0] += nt.Free
		merged[1] += nt.Total
		s.counts[index] = merged
		return nil
	}
	if s.allow != nil && !s.allow[nt.NodeType] {
		return nil
	}
	if s.onlyWithFree && nt.Free <= 0 {
		return nil
	}

	index, err := s.submit(ctx, nt)
	if err != nil {
		return err
	}
	s.seen[key] = index
	s.counts[index] = [2]int64{nt.Free, nt.Total}
	s.selected++
	return nil
}

// applyMergedCounts writes back free/total for rows that arrived more than
// once (duplicate keys merge their counts, but only the first triggers a
// search).
func (s *discoverySelector) applyMergedCounts(results []allAvailabilityResultModel) {
	for index, merged := range s.counts {
		if index < len(results) {
			results[index].Free = types.Int64Value(merged[0])
			results[index].Total = types.Int64Value(merged[1])
		}
	}
}

// launchGate spaces out the starts of Portal searches so that no two requests
// arrive at the same instant (which races in the Portal's session layer),
// while still allowing them to be in flight concurrently.
type launchGate struct {
	mu       sync.Mutex
	next     time.Time
	interval time.Duration
}

// wait blocks until this caller's launch slot arrives or ctx is done.
func (g *launchGate) wait(ctx context.Context) error {
	g.mu.Lock()
	now := time.Now()
	if g.next.Before(now) {
		g.next = now
	}
	delay := g.next.Sub(now)
	g.next = g.next.Add(g.interval)
	g.mu.Unlock()

	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// surveyJob is one node type queued for a reservation search, tagged with its
// slot in the results slice.
type surveyJob struct {
	index int
	nt    discoveredNodeType
}

// availabilitySurvey is a worker pool that runs one Portal reservation search
// per submitted node type. Rows can be submitted while discovery is still
// producing more, so searches overlap discovery. Search starts are staggered
// through a launchGate (see searchLaunchInterval).
type availabilitySurvey struct {
	ctx           context.Context
	searcher      resgroupSearcher
	project       string
	group         string
	durationHours int64
	gate          *launchGate
	jobs          chan surveyJob
	workers       sync.WaitGroup

	mu      sync.Mutex
	results []allAvailabilityResultModel
}

// startAvailabilitySurvey launches the worker pool. Callers must invoke wait
// exactly once after their final submit.
func startAvailabilitySurvey(
	ctx context.Context,
	searcher resgroupSearcher,
	project string,
	group string,
	durationHours int64,
	concurrency int,
	launchInterval time.Duration,
) *availabilitySurvey {
	if concurrency < 1 {
		concurrency = 1
	}
	s := &availabilitySurvey{
		ctx:           ctx,
		searcher:      searcher,
		project:       project,
		group:         group,
		durationHours: durationHours,
		gate:          &launchGate{interval: launchInterval},
		// Discovery inventories are small (normally fewer than 100 rows). A
		// bounded buffer lets the producer finish promptly instead of making its
		// stdout consumption wait for Portal search workers, while retaining a
		// hard memory bound for arbitrary external discover commands.
		jobs: make(chan surveyJob, availabilityJobBuffer),
	}
	s.workers.Add(concurrency)
	for workerID := 0; workerID < concurrency; workerID++ {
		go func(workerID int) {
			defer s.workers.Done()
			for job := range s.jobs {
				s.search(workerID, job)
			}
		}(workerID)
	}
	return s
}

// submit queues one node type and returns its index in the results slice.
// It only back-pressures discovery if the bounded job buffer is full.
func (s *availabilitySurvey) submit(ctx context.Context, nt discoveredNodeType) (int, error) {
	s.mu.Lock()
	index := len(s.results)
	s.results = append(s.results, allAvailabilityResultModel{})
	s.mu.Unlock()

	select {
	case s.jobs <- surveyJob{index: index, nt: nt}:
		return index, nil
	case <-ctx.Done():
		return index, ctx.Err()
	}
}

// wait closes intake, waits for in-flight searches, and returns rows in
// submit order.
func (s *availabilitySurvey) wait() []allAvailabilityResultModel {
	close(s.jobs)
	s.workers.Wait()
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.results
}

func (s *availabilitySurvey) search(workerID int, job surveyJob) {
	nt := job.nt
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
	defer func() {
		s.mu.Lock()
		s.results[job.index] = row
		s.mu.Unlock()
	}()

	if err := s.gate.wait(s.ctx); err != nil {
		row.Error = types.StringValue(err.Error())
		return
	}

	searchReq := &ResgroupSearchRequest{
		Project: s.project,
		NodeTypes: &ResgroupNodeTypes{NodeTypes: []ResgroupNodeType{{
			URN:      nt.URN,
			NodeType: nt.NodeType,
			Count:    1,
		}}},
	}
	if s.group != "" {
		searchReq.Group = s.group
	}

	started := time.Now()
	tflog.Debug(s.ctx, "Searching node-type availability", map[string]any{
		"worker":    workerID,
		"cluster":   nt.Cluster,
		"node_type": nt.NodeType,
	})
	res, err := s.searcher.SearchResgroup(s.ctx, searchReq, s.durationHours)
	if err != nil {
		row.Error = types.StringValue(err.Error())
	} else {
		row.StartAt = types.StringValue(res.StartAt)
		row.ExpiresAt = types.StringValue(res.ExpiresAt)
	}
	tflog.Debug(s.ctx, "Node-type availability search completed", map[string]any{
		"worker":      workerID,
		"cluster":     nt.Cluster,
		"node_type":   nt.NodeType,
		"duration_ms": time.Since(started).Milliseconds(),
		"error":       err != nil,
	})
}

// surveyNodeTypeAvailability runs a fixed batch of node types through an
// availabilitySurvey and returns rows in input order.
func surveyNodeTypeAvailability(
	ctx context.Context,
	searcher resgroupSearcher,
	project string,
	group string,
	durationHours int64,
	nodeTypes []discoveredNodeType,
	concurrency int,
	launchInterval time.Duration,
) ([]allAvailabilityResultModel, error) {
	if concurrency < 1 {
		return nil, fmt.Errorf("concurrency must be at least 1")
	}
	if len(nodeTypes) == 0 {
		return []allAvailabilityResultModel{}, nil
	}
	if concurrency > len(nodeTypes) {
		concurrency = len(nodeTypes)
	}

	s := startAvailabilitySurvey(ctx, searcher, project, group, durationHours, concurrency, launchInterval)
	var submitErr error
	for _, nt := range nodeTypes {
		if _, err := s.submit(ctx, nt); err != nil {
			submitErr = err
			break
		}
	}
	results := s.wait()
	if submitErr != nil || ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return results, nil
}
