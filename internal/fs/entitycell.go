package fs

import "sync"

// entityCell is the volatile-state slot every entity-carrying directory node
// embeds: the one lock-guarded field that the nodeRefresher seam (refresh.go)
// swaps when go-fuse dedups a later Lookup onto an already-known node. Before
// it, each of the ~11 regular entity nodes (TeamNode, IssuesNode, UserNode,
// CyclesNode, RecentNode, InitiativeNode, the three filter nodes, …) hand-wrote
// the identical entity()/setEntity() lock dance, so the "every read/write of the
// entity goes under the lock" discipline was enforced only by copy-paste — a new
// node could silently omit it.
//
// Embedding entityCell[api.Team] promotes entity()/setEntity() onto the node, so
// call sites read exactly as before (n.entity()) but the accessor is behavior it
// inherits, not code it maintains. refreshFrom stays per-node — the type
// assertion is a runtime dispatch a generic can't own — but shrinks to the
// assert plus n.setEntity(f.entity()).
//
// It carries its OWN mutex, distinct from the sibling attrNode.stateMu: the two
// guard disjoint data (attr times vs the entity) and no code reads them jointly,
// so the split is behavior-preserving and the entity's own read/write still
// serialize. The two irregular nodes that carry two entities (ProjectNode:
// team+project; CycleDirNode: team+cycle) keep their bespoke triplets — bending
// the generic to a two-field cell would be interface width for two callers.
type entityCell[E any] struct {
	mu  sync.Mutex
	val E
}

// entity returns a snapshot of the current entity under the lock.
func (c *entityCell[E]) entity() E {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.val
}

// setEntity swaps the entity under the lock. The nodeRefresher seam
// (refresh.go) calls it to push freshly-fetched state into a reused node.
func (c *entityCell[E]) setEntity(v E) {
	c.mu.Lock()
	c.val = v
	c.mu.Unlock()
}
