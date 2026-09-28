package provider

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeResgroupSearcher struct {
	mu        sync.Mutex
	delay     time.Duration
	errors    map[string]error
	calls     []string
	active    int
	maxActive int
}

func (f *fakeResgroupSearcher) SearchResgroup(ctx context.Context, req *ResgroupSearchRequest, _ int64) (*ResgroupSearchResult, error) {
	nodeType := req.NodeTypes.NodeTypes[0].NodeType
	f.mu.Lock()
	f.calls = append(f.calls, nodeType)
	f.active++
	if f.active > f.maxActive {
		f.maxActive = f.active
	}
	f.mu.Unlock()

	defer func() {
		f.mu.Lock()
		f.active--
		f.mu.Unlock()
	}()

	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if err := f.errors[nodeType]; err != nil {
		return nil, err
	}
	return &ResgroupSearchResult{
		StartAt:   nodeType + "-start",
		ExpiresAt: nodeType + "-end",
	}, nil
}

func TestConsumeDiscoveryOutputAcceptsArrayAndStream(t *testing.T) {
	t.Parallel()
	array := ` [{"cluster":"A","urn":"u","node_type":"t1","free":1,"total":2},
	            {"cluster":"B","urn":"v","node_type":"t2","free":0,"total":3}]`
	stream := `{"cluster":"A","urn":"u","node_type":"t1","free":1,"total":2}
	           {"cluster":"B","urn":"v","node_type":"t2","free":0,"total":3}`

	for name, input := range map[string]string{"array": array, "stream": stream} {
		var got []discoveredNodeType
		err := consumeDiscoveryOutput(strings.NewReader(input), func(nt discoveredNodeType) error {
			got = append(got, nt)
			return nil
		})
		if err != nil {
			t.Fatalf("%s: consumeDiscoveryOutput returned error: %v", name, err)
		}
		if len(got) != 2 || got[0].NodeType != "t1" || got[1].NodeType != "t2" {
			t.Fatalf("%s: got %#v, want t1 then t2", name, got)
		}
	}
}

func TestConsumeDiscoveryOutputRejectsGarbage(t *testing.T) {
	t.Parallel()
	err := consumeDiscoveryOutput(strings.NewReader("not-json"), func(discoveredNodeType) error {
		return nil
	})
	if err == nil {
		t.Fatal("expected parse error")
	}
}

func newTestSelector(allow map[string]bool, onlyWithFree bool) (*discoverySelector, *[]discoveredNodeType) {
	submitted := &[]discoveredNodeType{}
	s := &discoverySelector{
		allow:        allow,
		onlyWithFree: onlyWithFree,
		submit: func(_ context.Context, nt discoveredNodeType) (int, error) {
			*submitted = append(*submitted, nt)
			return len(*submitted) - 1, nil
		},
	}
	return s, submitted
}

func TestDiscoverySelectorMergesDuplicatesAndFilters(t *testing.T) {
	t.Parallel()
	s, submitted := newTestSelector(nil, true)
	rows := []discoveredNodeType{
		{Cluster: "Wisconsin", URN: "urn:wisc", NodeType: "z-type", Free: 0, Total: 1},
		{Cluster: "Clemson", URN: "urn:clemson", NodeType: "c-type", Free: 1, Total: 2},
		{Cluster: " Clemson ", URN: " urn:clemson ", NodeType: " c-type ", Free: 2, Total: 3},
	}
	for _, nt := range rows {
		if err := s.accept(context.Background(), nt); err != nil {
			t.Fatalf("accept returned error: %v", err)
		}
	}

	if len(*submitted) != 1 || (*submitted)[0].NodeType != "c-type" {
		t.Fatalf("submitted = %#v, want only c-type (z-type has no free nodes, duplicate merges)", *submitted)
	}
	results := []allAvailabilityResultModel{{}}
	s.applyMergedCounts(results)
	if results[0].Free.ValueInt64() != 3 || results[0].Total.ValueInt64() != 5 {
		t.Fatalf("merged counts = %d/%d, want 3/5",
			results[0].Free.ValueInt64(), results[0].Total.ValueInt64())
	}
}

func TestDiscoverySelectorAppliesAllowList(t *testing.T) {
	t.Parallel()
	s, submitted := newTestSelector(map[string]bool{"keep": true}, false)
	for _, nt := range []discoveredNodeType{
		{Cluster: "Apt", URN: "urn:apt", NodeType: "keep", Free: 1, Total: 1},
		{Cluster: "Apt", URN: "urn:apt", NodeType: "drop", Free: 1, Total: 1},
	} {
		if err := s.accept(context.Background(), nt); err != nil {
			t.Fatalf("accept returned error: %v", err)
		}
	}
	if len(*submitted) != 1 || (*submitted)[0].NodeType != "keep" {
		t.Fatalf("submitted %#v, want only keep", *submitted)
	}
}

func TestDiscoverySelectorRejectsInvalidCounts(t *testing.T) {
	t.Parallel()
	s, _ := newTestSelector(nil, false)
	err := s.accept(context.Background(), discoveredNodeType{
		Cluster: "Apt", URN: "urn:apt", NodeType: "bad", Free: 2, Total: 1,
	})
	if err == nil {
		t.Fatal("expected invalid counts error")
	}
}

func TestSurveyNodeTypeAvailabilityHonorsConcurrencyAndOrder(t *testing.T) {
	t.Parallel()
	searcher := &fakeResgroupSearcher{delay: 20 * time.Millisecond}
	nodeTypes := make([]discoveredNodeType, 6)
	for i := range nodeTypes {
		nodeTypes[i] = discoveredNodeType{
			Cluster:  "Test",
			URN:      "urn:test",
			NodeType: fmt.Sprintf("type-%d", i),
			Free:     1,
			Total:    1,
		}
	}

	results, err := surveyNodeTypeAvailability(context.Background(), searcher, "project", "", 168, nodeTypes, 2, 0)
	if err != nil {
		t.Fatalf("surveyNodeTypeAvailability returned error: %v", err)
	}
	if searcher.maxActive != 2 {
		t.Fatalf("max active searches = %d, want 2", searcher.maxActive)
	}
	for i, result := range results {
		want := fmt.Sprintf("type-%d", i)
		if result.NodeType.ValueString() != want {
			t.Fatalf("result %d node type = %q, want %q", i, result.NodeType.ValueString(), want)
		}
		if result.StartAt.ValueString() != want+"-start" {
			t.Fatalf("result %d start = %q, want %q", i, result.StartAt.ValueString(), want+"-start")
		}
	}
}

func TestSurveyNodeTypeAvailabilityKeepsPerRowErrors(t *testing.T) {
	t.Parallel()
	searcher := &fakeResgroupSearcher{errors: map[string]error{"bad": errors.New("no fit")}}
	nodeTypes := []discoveredNodeType{
		{Cluster: "Test", URN: "urn:test", NodeType: "good", Free: 1, Total: 1},
		{Cluster: "Test", URN: "urn:test", NodeType: "bad", Free: 1, Total: 1},
	}

	results, err := surveyNodeTypeAvailability(context.Background(), searcher, "project", "group", 168, nodeTypes, 2, 0)
	if err != nil {
		t.Fatalf("surveyNodeTypeAvailability returned error: %v", err)
	}
	if !results[0].Error.IsNull() {
		t.Fatalf("good result error = %q, want null", results[0].Error.ValueString())
	}
	if results[1].Error.ValueString() != "no fit" {
		t.Fatalf("bad result error = %q, want no fit", results[1].Error.ValueString())
	}
}

func TestSurveyNodeTypeAvailabilityStaggersLaunches(t *testing.T) {
	t.Parallel()
	searcher := &fakeResgroupSearcher{delay: 5 * time.Millisecond}
	nodeTypes := make([]discoveredNodeType, 4)
	for i := range nodeTypes {
		nodeTypes[i] = discoveredNodeType{
			Cluster:  "Test",
			URN:      "urn:test",
			NodeType: fmt.Sprintf("type-%d", i),
			Free:     1,
			Total:    1,
		}
	}

	interval := 30 * time.Millisecond
	started := time.Now()
	results, err := surveyNodeTypeAvailability(context.Background(), searcher, "project", "", 168, nodeTypes, 4, interval)
	if err != nil {
		t.Fatalf("surveyNodeTypeAvailability returned error: %v", err)
	}
	if len(results) != 4 {
		t.Fatalf("got %d results, want 4", len(results))
	}
	// 4 launches spaced ≥30ms apart cannot all start before 3×interval.
	if elapsed := time.Since(started); elapsed < 3*interval {
		t.Fatalf("survey finished in %v, want at least %v of launch stagger", elapsed, 3*interval)
	}
}

func TestSurveyNodeTypeAvailabilityHonorsCancellation(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	searcher := &fakeResgroupSearcher{delay: time.Second}
	nodeTypes := []discoveredNodeType{
		{Cluster: "Test", URN: "urn:test", NodeType: "one", Free: 1, Total: 1},
		{Cluster: "Test", URN: "urn:test", NodeType: "two", Free: 1, Total: 1},
	}
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	_, err := surveyNodeTypeAvailability(ctx, searcher, "project", "", 168, nodeTypes, 1, 0)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}
