package repo

import (
	"database/sql"
	"errors"
	"fmt"
	"testing"
)

// TestQueryOne pins the single-row read contract the 17 getters share.
func TestQueryOne(t *testing.T) {
	t.Parallel()

	t.Run("not found is nil, nil", func(t *testing.T) {
		t.Parallel()
		got, err := queryOne("get thing",
			func() (int, error) { return 0, sql.ErrNoRows },
			pure(func(r int) string { return "unused" }))
		if err != nil || got != nil {
			t.Errorf("ErrNoRows must map to (nil, nil), got (%v, %v)", got, err)
		}
	})

	t.Run("fetch error wraps with the op label", func(t *testing.T) {
		t.Parallel()
		boom := errors.New("boom")
		_, err := queryOne("get thing",
			func() (int, error) { return 0, boom },
			pure(func(r int) string { return "unused" }))
		if err == nil || !errors.Is(err, boom) {
			t.Fatalf("expected wrapped fetch error, got %v", err)
		}
		if want := "get thing: boom"; err.Error() != want {
			t.Errorf("wrap message = %q, want %q", err.Error(), want)
		}
	})

	t.Run("convert error propagates unwrapped", func(t *testing.T) {
		t.Parallel()
		bad := errors.New("bad row")
		_, err := queryOne("get thing",
			func() (int, error) { return 7, nil },
			func(r int) (string, error) { return "", bad })
		if err != bad {
			t.Errorf("convert error must propagate unwrapped, got %v", err)
		}
	})

	t.Run("success returns the converted row by address", func(t *testing.T) {
		t.Parallel()
		got, err := queryOne("get thing",
			func() (int, error) { return 7, nil },
			pure(func(r int) string { return fmt.Sprintf("row-%d", r) }))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got == nil || *got != "row-7" {
			t.Errorf("got %v, want row-7", got)
		}
	})
}
