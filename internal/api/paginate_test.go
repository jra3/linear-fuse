package api

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jra3/linear-fuse/internal/testutil"
)

// pi builds a *PageInfo literal.
func pi(hasNext bool, cursor string) *PageInfo {
	return &PageInfo{HasNextPage: hasNext, EndCursor: cursor}
}

// scriptedFetch serves canned pages keyed by the after cursor and records
// the cursors it was asked for.
func scriptedFetch(t *testing.T, script map[string]conn[string], afters *[]string) pageFetch[string] {
	t.Helper()
	return func(_ context.Context, after string) (conn[string], error) {
		*afters = append(*afters, after)
		cn, ok := script[after]
		if !ok {
			t.Fatalf("unexpected cursor %q", after)
		}
		return cn, nil
	}
}

func TestDrainFromThreadsCursorAndMerges(t *testing.T) {
	var afters []string
	script := map[string]conn[string]{
		"":   {PageInfo: pi(true, "c1"), Nodes: []string{"a", "b"}},
		"c1": {PageInfo: pi(true, "c2"), Nodes: []string{"c"}},
		"c2": {PageInfo: pi(false, ""), Nodes: []string{"d"}},
	}
	got, err := drainFrom(context.Background(), NewClient("test"), "", scriptedFetch(t, script, &afters))
	if err != nil {
		t.Fatalf("drainFrom: %v", err)
	}
	if want := []string{"a", "b", "c", "d"}; !equalStrings(got, want) {
		t.Errorf("nodes = %v, want %v", got, want)
	}
	if want := []string{"", "c1", "c2"}; !equalStrings(afters, want) {
		t.Errorf("cursors fetched = %v, want %v", afters, want)
	}
}

func TestDrainFromResumesFromCursor(t *testing.T) {
	var afters []string
	script := map[string]conn[string]{
		"resume": {PageInfo: pi(false, ""), Nodes: []string{"tail"}},
	}
	got, err := drainFrom(context.Background(), NewClient("test"), "resume", scriptedFetch(t, script, &afters))
	if err != nil {
		t.Fatalf("drainFrom: %v", err)
	}
	if len(got) != 1 || got[0] != "tail" {
		t.Errorf("nodes = %v, want [tail]", got)
	}
	if len(afters) != 1 || afters[0] != "resume" {
		t.Errorf("cursors fetched = %v, want [resume]", afters)
	}
}

func TestDrainFromStalledCursorFailsLoudly(t *testing.T) {
	// hasNextPage=true with the same cursor echoed back: previously an
	// infinite loop, now a loud error.
	fetch := func(_ context.Context, after string) (conn[int], error) {
		return conn[int]{PageInfo: pi(true, after), Nodes: []int{1}}, nil
	}
	_, err := drainFrom(context.Background(), NewClient("test"), "stuck", fetch)
	if !errors.Is(err, errStalledCursor) {
		t.Fatalf("err = %v, want errStalledCursor", err)
	}

	// Empty endCursor with hasNextPage=true is the same defect.
	fetch = func(_ context.Context, _ string) (conn[int], error) {
		return conn[int]{PageInfo: pi(true, ""), Nodes: []int{1}}, nil
	}
	_, err = drainFrom(context.Background(), NewClient("test"), "", fetch)
	if !errors.Is(err, errStalledCursor) {
		t.Fatalf("err = %v, want errStalledCursor", err)
	}
}

func TestDrainFromMissingPageInfoIsError(t *testing.T) {
	fetch := func(_ context.Context, _ string) (conn[int], error) {
		return conn[int]{Nodes: []int{1}}, nil // no pageInfo selected
	}
	_, err := drainFrom(context.Background(), NewClient("test"), "", fetch)
	if err == nil || !strings.Contains(err.Error(), "missing pageInfo") {
		t.Fatalf("err = %v, want missing-pageInfo error", err)
	}
}

func TestDrainFromAllOrNothing(t *testing.T) {
	boom := errors.New("page 2 boom")
	fetch := func(_ context.Context, after string) (conn[int], error) {
		if after == "" {
			return conn[int]{PageInfo: pi(true, "c1"), Nodes: []int{1, 2}}, nil
		}
		return conn[int]{}, boom
	}
	got, err := drainFrom(context.Background(), NewClient("test"), "", fetch)
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want wrapped page-2 error", err)
	}
	if got != nil {
		t.Errorf("nodes = %v, want nil (all-or-nothing)", got)
	}
	if !strings.Contains(err.Error(), "page 2") {
		t.Errorf("err %q should name the failing page", err)
	}
}

func TestDrainFromBudgetRefusesToStart(t *testing.T) {
	c := NewClient("test")
	for !c.LowBudget() {
		c.limiter.Reserve() // drain burst below the LowBudget threshold
	}
	calls := 0
	fetch := func(_ context.Context, _ string) (conn[int], error) {
		calls++
		return conn[int]{PageInfo: pi(true, "next"), Nodes: []int{calls}}, nil
	}
	got, err := drainFrom(context.Background(), c, "", fetch)
	if !errors.Is(err, ErrBudget) {
		t.Fatalf("err = %v, want ErrBudget", err)
	}
	if got != nil {
		t.Errorf("nodes = %v, want nil", got)
	}
	if calls != 0 {
		t.Errorf("fetch calls = %d, want 0 (refusal spends nothing)", calls)
	}
}

func TestDrainFromStartedDrainIgnoresBudget(t *testing.T) {
	// A drain that begins with budget runs to completion even if the
	// budget dips mid-way — aborting would discard pages already paid for.
	c := NewClient("test")
	fetch := func(_ context.Context, after string) (conn[int], error) {
		if after == "" {
			for !c.LowBudget() {
				c.limiter.Reserve() // budget collapses after page 1
			}
			return conn[int]{PageInfo: pi(true, "c1"), Nodes: []int{1}}, nil
		}
		return conn[int]{PageInfo: pi(false, ""), Nodes: []int{2}}, nil
	}
	got, err := drainFrom(context.Background(), c, "", fetch)
	if err != nil {
		t.Fatalf("drainFrom: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("nodes = %v, want both pages", got)
	}
}

func TestDrainFromCancelledContextBetweenPages(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	fetch := func(_ context.Context, _ string) (conn[int], error) {
		cancel() // cancel after the first page is served
		return conn[int]{PageInfo: pi(true, "next"), Nodes: []int{1}}, nil
	}
	got, err := drainFrom(ctx, NewClient("test"), "", fetch)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if got != nil {
		t.Errorf("nodes = %v, want nil", got)
	}
}

func TestDrainCompleteFirstPageCostsNothing(t *testing.T) {
	// HasNextPage=false: no API call may happen — an unusable query proves it.
	got, err := drain[int](context.Background(), NewClient("test"),
		"query Broken { broken }", nil, pi(false, "ignored"), "team", "labels")
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	if got != nil {
		t.Errorf("nodes = %v, want nil (nothing to drain)", got)
	}
}

func TestDrainNilPageInfoIsLoudError(t *testing.T) {
	_, err := drain[int](context.Background(), NewClient("test"),
		"query Broken { broken }", nil, nil, "team", "labels")
	if err == nil || !strings.Contains(err.Error(), "team.labels") || !strings.Contains(err.Error(), "missing pageInfo") {
		t.Fatalf("err = %v, want loud missing-pageInfo error naming team.labels", err)
	}
}

func TestFetchAllAndDrainRejectAfterVar(t *testing.T) {
	vars := map[string]any{"after": "smuggled"}
	if _, err := fetchAll[int](context.Background(), NewClient("test"), "q", vars, "x"); err == nil {
		t.Error("fetchAll accepted caller-supplied after var")
	}
	if _, err := drain[int](context.Background(), NewClient("test"), "q", vars, pi(true, "c"), "x"); err == nil {
		t.Error("drain accepted caller-supplied after var")
	}
}

func TestFetchAllWalksPathAndPaginates(t *testing.T) {
	mock := testutil.NewMockLinearServer()
	defer mock.Close()
	mock.SetResponse("TeamThings", map[string]any{
		"team": map[string]any{
			"things": map[string]any{
				"pageInfo": map[string]any{"hasNextPage": false, "endCursor": ""},
				"nodes":    []map[string]any{{"id": "t1"}, {"id": "t2"}},
			},
		},
	})

	c := NewClient("test")
	c.SetAPIURL(mock.URL())

	type idNode struct {
		ID string `json:"id"`
	}
	got, err := fetchAll[idNode](context.Background(), c,
		`query TeamThings($teamId: String!, $after: String) { team(id: $teamId) { things(first: 50, after: $after) { pageInfo { hasNextPage endCursor } nodes { id } } } }`,
		map[string]any{"teamId": "team-1"}, "team", "things")
	if err != nil {
		t.Fatalf("fetchAll: %v", err)
	}
	if len(got) != 2 || got[0].ID != "t1" || got[1].ID != "t2" {
		t.Errorf("nodes = %v, want [t1 t2]", got)
	}
	// First page must not send after at all.
	if call := mock.LastCall(); call != nil {
		if _, ok := call.Variables["after"]; ok {
			t.Errorf("first page sent after=%v, want omitted", call.Variables["after"])
		}
	}
}

func TestFetchAllNullPathElementIsError(t *testing.T) {
	mock := testutil.NewMockLinearServer()
	defer mock.Close()
	mock.SetResponse("TeamThings", map[string]any{"team": nil})

	c := NewClient("test")
	c.SetAPIURL(mock.URL())

	_, err := fetchAll[struct{}](context.Background(), c,
		`query TeamThings($after: String) { team { things(after: $after) { pageInfo { hasNextPage endCursor } nodes { __typename } } } }`,
		nil, "team", "things")
	if err == nil || !strings.Contains(err.Error(), `"team"`) {
		t.Fatalf("err = %v, want error naming the null path element", err)
	}
}

func TestFetchAllMissingPageInfoInResponseIsError(t *testing.T) {
	// A response whose connection lacks pageInfo (query text forgot to
	// select it) must fail loudly — this is the silent-truncation bug class.
	mock := testutil.NewMockLinearServer()
	defer mock.Close()
	mock.SetResponse("Things", map[string]any{
		"things": map[string]any{
			"nodes": []map[string]any{{"id": "t1"}},
		},
	})

	c := NewClient("test")
	c.SetAPIURL(mock.URL())

	_, err := fetchAll[struct{}](context.Background(), c,
		`query Things($after: String) { things(after: $after) { nodes { id } } }`,
		nil, "things")
	if err == nil || !strings.Contains(err.Error(), "missing pageInfo") {
		t.Fatalf("err = %v, want missing-pageInfo error", err)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
