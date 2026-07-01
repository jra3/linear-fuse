package api

import (
	"context"
	"encoding/json"
	"fmt"
)

// mutateVoid runs a Linear mutation whose only meaningful result is a success
// flag — deletes, archives, link add/remove, and updates that return no entity.
// op is the mutation's payload key in the GraphQL response (e.g.
// "issueLabelDelete"); a false or missing success flag becomes an error. The
// response's entity fields, if any, are ignored.
func mutateVoid(ctx context.Context, c *Client, query string, vars map[string]any, op string) error {
	var resp map[string]struct {
		Success bool `json:"success"`
	}
	if err := c.query(ctx, query, vars, &resp); err != nil {
		return err
	}
	if !resp[op].Success {
		return fmt.Errorf("mutation %s failed", op)
	}
	return nil
}

// mutateEntity runs a Linear mutation that returns an affected entity. op is the
// mutation's payload key (e.g. "issueLabelCreate") and entityField is the key of
// the entity within that payload (e.g. "issueLabel"); both are the Linear schema
// names that used to live in per-mutation struct tags. A false success flag
// becomes an error; on success the entity is decoded into T.
func mutateEntity[T any](ctx context.Context, c *Client, query string, vars map[string]any, op, entityField string) (*T, error) {
	var resp map[string]json.RawMessage
	if err := c.query(ctx, query, vars, &resp); err != nil {
		return nil, err
	}
	payload, ok := resp[op]
	if !ok {
		return nil, fmt.Errorf("mutation %s: missing %q in response", op, op)
	}
	var flag struct {
		Success bool `json:"success"`
	}
	if err := json.Unmarshal(payload, &flag); err != nil {
		return nil, fmt.Errorf("mutation %s: decode success: %w", op, err)
	}
	if !flag.Success {
		return nil, fmt.Errorf("mutation %s failed", op)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(payload, &fields); err != nil {
		return nil, fmt.Errorf("mutation %s: decode payload: %w", op, err)
	}
	var entity T
	if err := json.Unmarshal(fields[entityField], &entity); err != nil {
		return nil, fmt.Errorf("mutation %s: decode %s: %w", op, entityField, err)
	}
	return &entity, nil
}
