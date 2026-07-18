package api

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// The read fetch core.
//
// Every Linear read answers with the same envelope: a data root whose nested
// fields path down to one entity or one connection. The per-method
// declare-anonymous-struct / call / unwrap sequence was copied ~21 times
// across client.go; these helpers own it once, the way exec.go owns the
// mutation envelope and paginate.go owns the connection drain. Call sites
// keep only what genuinely varies: the query constant, the variable map, and
// the JSON path from the response root to the payload.
//
// Null policy — a DELIBERATE behavior change: a missing or null element
// anywhere on the path, including the terminal, is an error naming the
// dotted path walked so far, prefixed "fetch:". The old anonymous structs
// decoded a null terminal as a silent zero-value entity (GetIssue of a
// nonexistent ID returned an empty Issue with a nil error); these fronts
// refuse instead.
//
// The helpers never mutate the caller's vars map.

// isJSONNull reports whether a raw JSON value is absent or an explicit null —
// the single definition of the envelope's "null terminal is an error" rule,
// shared by walkPath (read side) and execMutation (mutation side). A map miss
// yields a nil RawMessage (len 0); an explicit JSON null is the four bytes
// "null". Both fail the policy; a present, non-null value passes.
func isJSONNull(raw json.RawMessage) bool {
	return len(raw) == 0 || string(raw) == "null"
}

// walkPath descends path from the decoded data root and returns the raw
// terminal value. A missing or null element errors naming the path walked so
// far. Errors carry no module prefix — callers wrap ("fetch: %w" here,
// "paginate: %w" in walkConn) so both fronts share one descent.
func walkPath(root map[string]json.RawMessage, path []string) (json.RawMessage, error) {
	if len(path) == 0 {
		return nil, fmt.Errorf("empty path")
	}
	cur := root
	for i, elem := range path {
		raw := cur[elem]
		if isJSONNull(raw) {
			return nil, fmt.Errorf("%q missing or null in response", strings.Join(path[:i+1], "."))
		}
		if i == len(path)-1 {
			return raw, nil
		}
		next := make(map[string]json.RawMessage)
		if err := json.Unmarshal(raw, &next); err != nil {
			return nil, fmt.Errorf("decode object at %q: %w", strings.Join(path[:i+1], "."), err)
		}
		cur = next
	}
	// Unreachable: the loop returns on the last element.
	return nil, fmt.Errorf("empty path")
}

// fetchPath executes the query and walks path from the data root, returning
// the raw terminal value with fetch-front error prefixes.
func fetchPath(ctx context.Context, c *Client, query string, vars map[string]any, path []string) (json.RawMessage, error) {
	var root map[string]json.RawMessage
	if err := c.query(ctx, query, vars, &root); err != nil {
		return nil, err
	}
	raw, err := walkPath(root, path)
	if err != nil {
		return nil, fmt.Errorf("fetch: %w", err)
	}
	return raw, nil
}

// fetchOne executes the query and decodes the terminal value at path into T.
// A missing or null terminal is an error (see the null policy above), never
// a zero-value entity.
func fetchOne[T any](ctx context.Context, c *Client, query string, vars map[string]any, path ...string) (*T, error) {
	raw, err := fetchPath(ctx, c, query, vars, path)
	if err != nil {
		return nil, err
	}
	var entity T
	if err := json.Unmarshal(raw, &entity); err != nil {
		return nil, fmt.Errorf("fetch: decode %q: %w", strings.Join(path, "."), err)
	}
	return &entity, nil
}

// connAt walks path from an already-decoded root and decodes the terminal as
// a connection envelope, with fetch-front error prefixes. It applies no
// PageInfo policy — that belongs to the front (fetchNodes tolerates an
// absent pageInfo, fetchConn requires one); it is the decode the two fronts
// share.
func connAt[T any](root map[string]json.RawMessage, path []string) (conn[T], error) {
	raw, err := walkPath(root, path)
	if err != nil {
		return conn[T]{}, fmt.Errorf("fetch: %w", err)
	}
	var cn conn[T]
	if err := json.Unmarshal(raw, &cn); err != nil {
		return conn[T]{}, fmt.Errorf("fetch: decode connection at %q: %w", strings.Join(path, "."), err)
	}
	return cn, nil
}

// fetchNodes executes the query, decodes the connection envelope at path,
// and returns its nodes.
//
// Truncation tripwire: a response that selects pageInfo and reports
// hasNextPage is an error — the connection has more pages and must be
// drained (fetchAll), never silently truncated. Inert today (no converted
// query selects pageInfo), live for any future query that does. An absent
// pageInfo is fine: single-page queries don't select it.
func fetchNodes[T any](ctx context.Context, c *Client, query string, vars map[string]any, path ...string) ([]T, error) {
	var root map[string]json.RawMessage
	if err := c.query(ctx, query, vars, &root); err != nil {
		return nil, err
	}
	cn, err := connAt[T](root, path)
	if err != nil {
		return nil, err
	}
	if cn.PageInfo != nil && cn.PageInfo.HasNextPage {
		return nil, fmt.Errorf("fetch: connection %q has more pages — it must be drained (fetchAll), not fetched single-page", strings.Join(path, "."))
	}
	return cn.Nodes, nil
}

// fetchConn executes the query and returns the whole connection envelope at
// path. A page-shaped caller needs the PageInfo, so a response missing it is
// an error — the same contract as fetchAll's "query must select pageInfo".
func fetchConn[T any](ctx context.Context, c *Client, query string, vars map[string]any, path ...string) (conn[T], error) {
	var root map[string]json.RawMessage
	if err := c.query(ctx, query, vars, &root); err != nil {
		return conn[T]{}, err
	}
	cn, err := connAt[T](root, path)
	if err != nil {
		return conn[T]{}, err
	}
	if cn.PageInfo == nil {
		return conn[T]{}, fmt.Errorf("fetch: connection %q missing pageInfo (query must select it)", strings.Join(path, "."))
	}
	return cn, nil
}
