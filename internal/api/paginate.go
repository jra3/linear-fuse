package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// The connection drain core.
//
// Every Linear GraphQL connection answers with the same envelope:
// {pageInfo {hasNextPage endCursor}, nodes [...]}. The thread-cursor /
// append / terminate loop was hand-copied 13 times across client.go — with
// no guard against a non-advancing cursor and no respect for the rate-limit
// budget — and skipped entirely in the reconcile ID fetches, where a
// silently truncated "authoritative" set feeds a diff-and-delete. These
// helpers own the invariant once, the way exec.go owns the mutation
// envelope. Call sites keep only what genuinely varies: the query constant,
// the variable map, and the JSON path from the response root to the
// connection.
//
// The result is all-or-nothing: a non-nil error means no nodes are
// returned, never a silent partial set. Callers that diff-and-delete
// against a drained result may trust any nil-error return completely.

// ErrBudget reports a drain refused before its first fetch because the
// rate-limit budget was already low (see Client.LowBudget) — nothing was
// spent, so deferring to the next sync cycle costs nothing. A drain that
// has started runs to completion instead: aborting between pages would
// discard pages already paid for, and observed live it burned one token
// per cycle on a page-1 fetch it then threw away, forever. The transport's
// own gate (the rateBudget priority-reserve ladder in Client.query) remains
// the hard floor under a running drain. The composite fetches
// (GetTeamMetadata, GetWorkspace) return it from their own LowBudget
// preflight too, for the same reason: refusing before the paid combined
// query beats discarding it when a follow-up drain refuses. Exported so
// callers can distinguish "retry later" from "broken" with errors.Is;
// treating it as an ordinary error is always safe. (The reconcile pass in
// internal/repo preflights with LowBudget directly instead of matching
// this error.)
var ErrBudget = errors.New("pagination deferred: rate-limit budget low")

// errStalledCursor reports a connection that claims more pages but supplies
// no genuinely advancing cursor — empty, repeated, or revisiting any cursor
// already consumed this drain (an A→B→A cycle would otherwise run to
// maxDrainPages before erroring) — returned rather than looping forever.
var errStalledCursor = errors.New("pagination stalled: hasNextPage with non-advancing endCursor")

// maxDrainPages is a defence-in-depth backstop behind the stall guard; no
// real connection approaches it.
const maxDrainPages = 10000

// conn is the standard Linear connection envelope, decoded at the end of a
// path. PageInfo is a pointer so that a query which forgot to select
// pageInfo is distinguishable from hasNextPage=false — the former is an
// error, never a silent single-page truncation. Also usable directly in
// hand-written result structs (the combined metadata queries) in place of
// anonymous {Nodes} pairs.
type conn[N any] struct {
	PageInfo *PageInfo `json:"pageInfo"`
	Nodes    []N       `json:"nodes"`
}

// pageFetch returns one page of a connection, resuming immediately after
// the given cursor; after == "" means the start of the connection. It is
// the module's seam: drainFrom never touches HTTP, GraphQL, or JSON — only
// pageFetches. Implementations must return the connection's PageInfo
// verbatim.
type pageFetch[N any] func(ctx context.Context, after string) (conn[N], error)

// drainFrom fetches every remaining page of a connection, starting after
// `after` ("" = from the beginning), and returns the concatenated nodes in
// the order the API produced them.
//
// Error modes (each returns nil nodes — all-or-nothing):
//   - ErrBudget if the rate-limit budget is already low before the first
//     fetch (nothing is spent; a started drain runs to completion);
//   - any error from fetch, wrapped with the page number and cursor;
//   - a missing pageInfo (the query did not select it);
//   - errStalledCursor if a page reports hasNextPage with an empty endCursor
//     or one already consumed this drain (immediate repeats and longer
//     cycles alike);
//   - ctx cancellation between pages.
func drainFrom[N any](ctx context.Context, c *Client, after string, fetch pageFetch[N]) ([]N, error) {
	if c.LowBudget() {
		return nil, fmt.Errorf("paginate: %w", ErrBudget)
	}
	seen := map[string]bool{after: true}
	var all []N
	for page := 1; ; page++ {
		if page > maxDrainPages {
			return nil, fmt.Errorf("paginate: exceeded %d pages", maxDrainPages)
		}
		if page > 1 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		cn, err := fetch(ctx, after)
		if err != nil {
			return nil, fmt.Errorf("paginate: page %d (after %q): %w", page, after, err)
		}
		if cn.PageInfo == nil {
			return nil, fmt.Errorf("paginate: page %d: response missing pageInfo (query must select it)", page)
		}
		all = append(all, cn.Nodes...)
		if !cn.PageInfo.HasNextPage {
			return all, nil
		}
		next := cn.PageInfo.EndCursor
		if next == "" || seen[next] {
			return nil, fmt.Errorf("paginate: page %d (cursor %q): %w", page, next, errStalledCursor)
		}
		seen[next] = true
		after = next
	}
}

// fetchAll fetches every node of the connection at path, from the start.
//
// Query contract: the query text must declare `$after: String` (nullable),
// pass it as the connection's `after` argument, and select
// `pageInfo { hasNextPage endCursor }` alongside `nodes`. Page size stays
// in the query text (first: 50 etc.); fetchAll does not set it.
//
// Vars: fetchAll owns the "after" key — vars must not contain it. vars may
// be nil; the caller's map is never mutated.
//
// All drainFrom invariants and error modes apply; walk failures (a missing
// or null path element) name the path element that failed.
func fetchAll[N any](ctx context.Context, c *Client, query string, vars map[string]any, path ...string) ([]N, error) {
	if _, ok := vars["after"]; ok {
		return nil, fmt.Errorf("paginate: vars must not contain %q (owned by the module)", "after")
	}
	return drainFrom(ctx, c, "", connFetch[N](c, query, vars, path))
}

// drain fetches the REMAINDER of a connection whose first page was already
// obtained by another query (the combined metadata queries select pageInfo
// per connection; drain resumes from that cursor) and returns only the
// remaining nodes; the caller appends them to the nodes it already holds.
//
// pi is the PageInfo of the last consumed page, exactly as decoded — pass
// result.Team.Labels.PageInfo. nil pi is an error (the combined query
// forgot to select pageInfo). If pi.HasNextPage is false, drain returns
// (nil, nil) without any API call — the common case costs nothing.
//
// Same query contract, vars rules, ordering, all-or-nothing result, and
// error modes as fetchAll.
func drain[N any](ctx context.Context, c *Client, query string, vars map[string]any, pi *PageInfo, path ...string) ([]N, error) {
	// The vars contract is checked before the !HasNextPage short-circuit: a
	// violation is a programming bug and must fail on every call, not ship
	// latently and detonate the first time the connection overflows a page.
	if _, ok := vars["after"]; ok {
		return nil, fmt.Errorf("paginate: vars must not contain %q (owned by the module)", "after")
	}
	if pi == nil {
		return nil, fmt.Errorf("paginate: connection %q missing pageInfo (query must select it)", strings.Join(path, "."))
	}
	if !pi.HasNextPage {
		return nil, nil
	}
	if pi.EndCursor == "" {
		return nil, fmt.Errorf("paginate: connection %q: %w", strings.Join(path, "."), errStalledCursor)
	}
	return drainFrom(ctx, c, pi.EndCursor, connFetch[N](c, query, vars, path))
}

// connFetch adapts one GraphQL query on c into a pageFetch: it copies vars
// for every page (the caller's map is never mutated), sets "after" only
// when the cursor is non-empty, executes via c.query, and walks path from
// the data root to the connection envelope.
func connFetch[N any](c *Client, query string, vars map[string]any, path []string) pageFetch[N] {
	return func(ctx context.Context, after string) (conn[N], error) {
		pv := make(map[string]any, len(vars)+1)
		for k, v := range vars {
			pv[k] = v
		}
		if after != "" {
			pv["after"] = after
		}
		var root map[string]json.RawMessage
		if err := c.query(ctx, query, pv, &root); err != nil {
			return conn[N]{}, err
		}
		return walkConn[N](root, path)
	}
}

// walkConn descends path from the decoded data root (the shared walkPath
// descent, see fetch.go) and decodes the terminal value as a connection
// envelope. A missing or null element errors with the path walked so far.
func walkConn[N any](root map[string]json.RawMessage, path []string) (conn[N], error) {
	if len(path) == 0 {
		return conn[N]{}, fmt.Errorf("paginate: empty connection path")
	}
	raw, err := walkPath(root, path)
	if err != nil {
		return conn[N]{}, fmt.Errorf("paginate: %w", err)
	}
	var cn conn[N]
	if err := json.Unmarshal(raw, &cn); err != nil {
		return conn[N]{}, fmt.Errorf("paginate: decode connection at %q: %w", strings.Join(path, "."), err)
	}
	return cn, nil
}
