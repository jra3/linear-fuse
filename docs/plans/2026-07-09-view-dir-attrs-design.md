# View/entity directory normalization — round-18 card 4 (grilling-locked)

Locked with John 2026-07-09. One PR, full normalization.

## Friction
~10 hand-rolled Lookup attr blocks (root.go ×4, cycles.go, filter.go ×2,
my.go, teams.go, users.go) stamping `time.Now()` — every `ls -l` of the mount
root or a view parent reshuffles times, the drift attrNode exists to kill —
plus the recorded ino-namespace inconsistency: these nodes (and TeamNode /
UserNode, per the nodeRefresher CONTEXT entry) use auto-assigned inos,
dodging rather than solving the captured-snapshot staleness bug.

## Locked decisions
- **Full normalization, everything in one PR** (grilled; the conservative
  view-dirs-only split and a times-only fix were offered and declined):
  every directory node currently on auto inos gets a stable ino, routes
  through `newDirInode` (attrNode mixin ⇒ Lookup==Getattr by construction),
  and reports honest times.
- Nodes in scope: TeamsNode, UsersNode, MyNode, InitiativesNode (stateless
  containers — zero times, no refresh needs), CyclesNode{team},
  CycleDirNode{team,cycle}, RecentNode{team}, the by/ filter dirs
  (ByNode/FilterTypeNode/FilterValueNode shapes), my/ subdirs, **TeamNode**
  (teams/{KEY}) and **UserNode** (users/{name}) — the per-entity dirs, fully
  erasing the recorded inconsistency.
- Times: entity times where they exist (team times for TeamNode/CyclesNode/
  RecentNode; cycle times for CycleDirNode), zero (honest unknown) for
  stateless containers and UserNode (api.User has no time fields).
- Snapshot carriers implement the nodeRefresher seam (entity()/setEntity
  under attrNode.stateMu, refreshFrom re-stamps nodeAttr) so kernel-reused
  nodes pick up fresh snapshots — the round-15 pattern.
- New ino wrappers per kind with composite keys where needed (e.g.
  filter-value dirs key on team+category+value), all registered in
  TestInodeNamespaceDistinct.
- Guard: extend the round-15 pinned-fd revalidation test technique to a
  TeamNode case (remote team change visible after entry-timeout expiry with
  the inode chain pinned).
