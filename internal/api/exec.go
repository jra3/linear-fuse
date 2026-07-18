package api

import (
	"context"
	"encoding/json"
	"fmt"
)

// The mutation execute core.
//
// Every Linear mutation answers with the same envelope: one top-level field
// named after the operation, containing {success, <entity>}. The per-method
// declare-struct / call / success-check / unwrap sequence was copied 28 times
// across client.go; these two helpers own it once. Methods keep only what
// genuinely varies: the query constant, the variable map, and the envelope
// field names. Cross-cutting behavior added here (error wrapping today;
// retries or logging tomorrow) reaches every mutation at once.

// execMutation runs a mutation and decodes the entity at
// data.<opField>.<entityField> into T, failing if success is false or absent.
//
// A success:true payload whose entity field is absent or an explicit null is an
// error, not a silent zero-value T — the same "null terminal is an error" rule
// the read side enforces via walkPath (#273). Absent already errored on decode
// ("unexpected end of JSON input"); an explicit null decoded cleanly into a
// zero value, so it is the case the guard adds.
func execMutation[T any](ctx context.Context, c *Client, query string, vars map[string]any, opField, entityField string) (*T, error) {
	payload, err := execEnvelope(ctx, c, query, vars, opField)
	if err != nil {
		return nil, err
	}
	raw := payload[entityField]
	if isJSONNull(raw) {
		return nil, fmt.Errorf("%s: %s missing or null in a success response", opField, entityField)
	}
	var entity T
	if err := json.Unmarshal(raw, &entity); err != nil {
		return nil, fmt.Errorf("%s: decode %s: %w", opField, entityField, err)
	}
	return &entity, nil
}

// execMutationOK runs a mutation whose payload carries only the success flag.
func execMutationOK(ctx context.Context, c *Client, query string, vars map[string]any, opField string) error {
	_, err := execEnvelope(ctx, c, query, vars, opField)
	return err
}

// execEnvelope calls the API and unwraps the standard mutation envelope,
// returning the raw payload fields after the success check.
func execEnvelope(ctx context.Context, c *Client, query string, vars map[string]any, opField string) (map[string]json.RawMessage, error) {
	var envelope map[string]json.RawMessage
	if err := c.query(ctx, query, vars, &envelope); err != nil {
		return nil, err
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(envelope[opField], &payload); err != nil {
		return nil, fmt.Errorf("%s: malformed mutation payload: %w", opField, err)
	}
	var success bool
	if raw, ok := payload["success"]; ok {
		if err := json.Unmarshal(raw, &success); err != nil {
			return nil, fmt.Errorf("%s: malformed success flag: %w", opField, err)
		}
	}
	if !success {
		return nil, fmt.Errorf("%s: mutation reported failure", opField)
	}
	return payload, nil
}
