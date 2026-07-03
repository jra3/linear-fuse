package fs

import (
	"context"
	"syscall"
	"testing"
)

func TestCollectionTrioEntries(t *testing.T) {
	t.Parallel()

	withCreate := collectionTrio{kind: "comments", parentID: "issue-1",
		onFlush: func(context.Context, []byte) syscall.Errno { return 0 }}
	names := []string{}
	for _, e := range withCreate.entries() {
		names = append(names, e.Name)
	}
	if len(names) != 3 || names[0] != "_create" || names[1] != ".error" || names[2] != ".last" {
		t.Errorf("entries() with onFlush = %v, want [_create .error .last]", names)
	}

	// mkdir-created collections (projects) have no _create trigger; the trio
	// degrades to the two feedback sidecars.
	mkdirOnly := collectionTrio{kind: "projects", parentID: "team-1"}
	names = names[:0]
	for _, e := range mkdirOnly.entries() {
		names = append(names, e.Name)
	}
	if len(names) != 2 || names[0] != ".error" || names[1] != ".last" {
		t.Errorf("entries() without onFlush = %v, want [.error .last]", names)
	}
}
