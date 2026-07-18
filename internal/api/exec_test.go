package api

import (
	"context"
	"strings"
	"testing"
)

// execThing is a minimal entity for exercising execMutation's decode + null
// guard without depending on a real api.* type's field set.
type execThing struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

const execTestMutation = `mutation DoThing($id: String!) { thingUpdate(id: $id) { success thing { id name } } }`

// TestExecMutation_NullEntityIsError is the #273 guard: a success:true payload
// whose entity field is an explicit null must error, not decode into a silent
// zero-value entity (json.Unmarshal([]byte("null")) succeeds and yields one).
func TestExecMutation_NullEntityIsError(t *testing.T) {
	mock, c := fetchTestClient(t)
	mock.SetResponse("DoThing", map[string]any{
		"thingUpdate": map[string]any{"success": true, "thing": nil},
	})
	_, err := execMutation[execThing](context.Background(), c, execTestMutation,
		map[string]any{"id": "t1"}, "thingUpdate", "thing")
	if err == nil || !strings.Contains(err.Error(), "thingUpdate: thing missing or null in a success response") {
		t.Fatalf("err = %v, want a missing-or-null guard error", err)
	}
}

// TestExecMutation_MissingEntityIsError covers the absent-field case: it errored
// before (decode of a nil RawMessage) and now errors through the same guard, so
// both faces of "no entity" report the same reason.
func TestExecMutation_MissingEntityIsError(t *testing.T) {
	mock, c := fetchTestClient(t)
	mock.SetResponse("DoThing", map[string]any{
		"thingUpdate": map[string]any{"success": true},
	})
	_, err := execMutation[execThing](context.Background(), c, execTestMutation,
		map[string]any{"id": "t1"}, "thingUpdate", "thing")
	if err == nil || !strings.Contains(err.Error(), "thingUpdate: thing missing or null in a success response") {
		t.Fatalf("err = %v, want a missing-or-null guard error", err)
	}
}

// TestExecMutation_DecodesEntity is the happy path: a present, non-null entity
// decodes into T.
func TestExecMutation_DecodesEntity(t *testing.T) {
	mock, c := fetchTestClient(t)
	mock.SetResponse("DoThing", map[string]any{
		"thingUpdate": map[string]any{
			"success": true,
			"thing":   map[string]any{"id": "t1", "name": "One"},
		},
	})
	got, err := execMutation[execThing](context.Background(), c, execTestMutation,
		map[string]any{"id": "t1"}, "thingUpdate", "thing")
	if err != nil {
		t.Fatalf("execMutation: %v", err)
	}
	if got.ID != "t1" || got.Name != "One" {
		t.Errorf("entity = %+v, want t1/One", got)
	}
}

// TestExecMutation_SuccessFalseIsError pins that the envelope's success check
// still fires ahead of the entity decode.
func TestExecMutation_SuccessFalseIsError(t *testing.T) {
	mock, c := fetchTestClient(t)
	mock.SetResponse("DoThing", map[string]any{
		"thingUpdate": map[string]any{
			"success": false,
			"thing":   map[string]any{"id": "t1", "name": "One"},
		},
	})
	_, err := execMutation[execThing](context.Background(), c, execTestMutation,
		map[string]any{"id": "t1"}, "thingUpdate", "thing")
	if err == nil || !strings.Contains(err.Error(), "mutation reported failure") {
		t.Fatalf("err = %v, want a success-false error", err)
	}
}
