package api

import "fmt"

// queryTeams drains: this is the sync worker's root fetch, and Linear
// silently caps a connection without first: at 50 nodes — a 51st team would
// have silently truncated the whole sync.
const queryTeams = `
query Teams($after: String) {
  teams(first: 50, after: $after) {
    pageInfo { hasNextPage endCursor }
    nodes {
      id
      key
      name
      icon
      createdAt
      updatedAt
    }
  }
}
`

// queryTeamIssuesByUpdatedAt fetches issues ordered by updatedAt DESC for incremental sync
var queryTeamIssuesByUpdatedAt = `
query TeamIssuesByUpdatedAt($teamId: String!, $first: Int!, $after: String) {
  team(id: $teamId) {
    issues(first: $first, after: $after, orderBy: updatedAt) {
      pageInfo { hasNextPage endCursor }
      nodes { ...IssueFieldsLite }
    }
  }
}
` + issueFieldsFragmentLite

var queryIssue = `
query Issue($id: String!) {
  issue(id: $id) { ...IssueFields }
}
` + issueFieldsFragment + issueRelationFieldsFragment + issueInverseRelationFieldsFragment

// issueFieldsFragmentLite is a lighter fragment for bulk queries (no relations).
// Use this for fetching many issues at once to avoid GraphQL complexity limits.
const issueFieldsFragmentLite = `
fragment IssueFieldsLite on Issue {
  id
  identifier
  title
  description
  branchName
  state { id name type }
  assignee { id name email }
  creator { id name email }
  priority
  labels { nodes { id name color description } }
  dueDate
  estimate
  createdAt
  updatedAt
  startedAt
  completedAt
  canceledAt
  archivedAt
  url
  team { id key name }
  project { id name slugId }
  projectMilestone { id name }
  parent { id identifier title }
  children { nodes { id identifier title createdAt updatedAt } }
  cycle { id name number }
}
`

// issueRelationFieldsFragment / issueInverseRelationFieldsFragment project a
// relation node's fields exactly once — referenced by both the IssueFields
// fragment and IssueDetailsSelection, so the two selections can never drift
// (the fragment rule: an inlined copy silently diverges when one gains a
// field). A query that references them must append them.
const issueRelationFieldsFragment = `
fragment IssueRelationFields on IssueRelation { id type relatedIssue { id identifier title } createdAt updatedAt }
`

const issueInverseRelationFieldsFragment = `
fragment IssueInverseRelationFields on IssueRelation { id type issue { id identifier title } createdAt updatedAt }
`

// issueFieldsFragment is a GraphQL fragment containing all fields fetched for an issue.
// Includes relations - use only for single-issue queries to avoid complexity limits.
const issueFieldsFragment = `
fragment IssueFields on Issue {
  id
  identifier
  title
  description
  branchName
  state { id name type }
  assignee { id name email }
  creator { id name email }
  priority
  labels { nodes { id name color description } }
  dueDate
  estimate
  createdAt
  updatedAt
  startedAt
  completedAt
  canceledAt
  archivedAt
  url
  team { id key name }
  project { id name slugId }
  projectMilestone { id name }
  parent { id identifier title }
  children { nodes { id identifier title createdAt updatedAt } }
  cycle { id name number }
  relations { nodes { ...IssueRelationFields } }
  inverseRelations { nodes { ...IssueInverseRelationFields } }
}
`

// CommentFieldsFragment is a GraphQL fragment for comment fields.
const CommentFieldsFragment = `
fragment CommentFields on Comment {
  id
  body
  createdAt
  updatedAt
  editedAt
  user { id name email }
}
`

// DocumentFieldsFragment is a GraphQL fragment for document fields.
const DocumentFieldsFragment = `
fragment DocumentFields on Document {
  id
  title
  content
  slugId
  url
  icon
  color
  createdAt
  updatedAt
  creator { id name email }
  issue { id identifier }
  project { id }
  initiative { id name }
}
`

// labelFieldsFragment is a GraphQL fragment for label fields.
const labelFieldsFragment = `
fragment LabelFields on IssueLabel {
  id
  name
  color
  description
  team { id }
}
`

// UserFields is the shared projection for a user, wherever whole users are
// listed: team members + drain page, workspace users + drain page, viewer.
// (Issue assignees and entity owners select their own narrower inline sets.)
const userFieldsFragment = `
fragment UserFields on User {
  id
  name
  email
  displayName
  active
}
`

// CycleFields is the shared projection for a cycle: the combined team
// metadata query and its drain page. A combined query and its drain twin
// MUST project identically — a field added to one but not the other means
// nodes past page one silently carry zero values.
const cycleFieldsFragment = `
fragment CycleFields on Cycle {
  id
  number
  name
  startsAt
  endsAt
  completedIssueCountHistory
  issueCountHistory
}
`

// InitiativeFields is the shared projection for an initiative's scalar
// fields: queryWorkspace, its initiatives drain page, and queryInitiative.
// The nested projects connection stays inline per query — page sizes differ
// (the workspace pair keeps nesting cheap at 50; the single-initiative
// query drains at 250).
const initiativeFieldsFragment = `
fragment InitiativeFields on Initiative {
  id
  name
  slugId
  description
  status
  color
  icon
  targetDate
  url
  createdAt
  updatedAt
  owner {
    id
    name
    email
  }
}
`

// ProjectLabelFields is the shared projection for a workspace project label.
// Defined mutation-less in this slice so future catalog CRUD mutations project
// through it (see CLAUDE.md: mutations must project through the entity's
// fragment). parent selects only the id — the catalog is always fully in hand
// locally, so parent/group names stitch at the repo read, not on the wire.
// retiredAt is doc-tagged [Internal] in the schema but live-verified selectable
// by API-key clients (2026-07-08).
const projectLabelFieldsFragment = `
fragment ProjectLabelFields on ProjectLabel {
  id
  name
  color
  description
  isGroup
  retiredAt
  createdAt
  updatedAt
  parent { id }
}
`

// AttachmentFieldsFragment is a GraphQL fragment for attachment fields.
const AttachmentFieldsFragment = `
fragment AttachmentFields on Attachment {
  id
  title
  subtitle
  url
  sourceType
  metadata
  createdAt
  updatedAt
  creator { id name email }
}
`

// ProjectMilestoneFields is the shared projection for a project milestone,
// used by the nested selections in queryTeamProjects/queryProject and the
// create/update mutations.
const projectMilestoneFieldsFragment = `
fragment ProjectMilestoneFields on ProjectMilestone {
  id
  name
  description
  targetDate
  sortOrder
}
`

// ProjectUpdateFields / InitiativeUpdateFields are the shared projections for
// status updates. The create mutations previously omitted updatedAt; the
// fragment canonicalizes to the query's fuller set so a created update carries
// the same fields a fetched one does.
const projectUpdateFieldsFragment = `
fragment ProjectUpdateFields on ProjectUpdate {
  id
  body
  health
  createdAt
  updatedAt
  user { id name email }
}
`

const initiativeUpdateFieldsFragment = `
fragment InitiativeUpdateFields on InitiativeUpdate {
  id
  body
  health
  createdAt
  updatedAt
  user { id name email }
}
`

// queryTeamMetadata fetches team metadata in a single query: states,
// labels, cycles, members, and workspace labels. Projects deliberately live
// in their own paginated query (queryTeamProjects): their nested selections
// cost ~187 complexity points per node, so even 50 of them consume nearly
// the whole 10k complexity budget Linear allows a single query.
//
// Every unbounded connection selects pageInfo and is drained to completion
// by GetTeamMetadata when hasNextPage reports more (Linear caps a page at
// 250 nodes); states are workflow-bounded (~a dozen) and stay undrained.
var queryTeamMetadata = `
query TeamMetadata($teamId: String!) {
  team(id: $teamId) {
    states {
      nodes {
        id
        name
        type
      }
    }
    labels(first: 250) {
      pageInfo { hasNextPage endCursor }
      nodes { ...LabelFields }
    }
    cycles(first: 250) {
      pageInfo { hasNextPage endCursor }
      nodes { ...CycleFields }
    }
    members(first: 250) {
      pageInfo { hasNextPage endCursor }
      nodes { ...UserFields }
    }
  }
  issueLabels(first: 250) {
    pageInfo { hasNextPage endCursor }
    nodes { ...LabelFields }
  }
}
` + labelFieldsFragment + cycleFieldsFragment + userFieldsFragment

// Per-connection drain queries: resumed from the combined query's endCursor
// when a connection reports hasNextPage (see the paginate module).

var queryTeamLabelsPage = `
query TeamLabelsPage($teamId: String!, $after: String) {
  team(id: $teamId) {
    labels(first: 250, after: $after) {
      pageInfo { hasNextPage endCursor }
      nodes { ...LabelFields }
    }
  }
}
` + labelFieldsFragment

var queryTeamCyclesPage = `
query TeamCyclesPage($teamId: String!, $after: String) {
  team(id: $teamId) {
    cycles(first: 250, after: $after) {
      pageInfo { hasNextPage endCursor }
      nodes { ...CycleFields }
    }
  }
}
` + cycleFieldsFragment

var queryTeamMembersPage = `
query TeamMembersPage($teamId: String!, $after: String) {
  team(id: $teamId) {
    members(first: 250, after: $after) {
      pageInfo { hasNextPage endCursor }
      nodes { ...UserFields }
    }
  }
}
` + userFieldsFragment

var queryWorkspaceLabelsPage = `
query WorkspaceLabelsPage($after: String) {
  issueLabels(first: 250, after: $after) {
    pageInfo { hasNextPage endCursor }
    nodes { ...LabelFields }
  }
}
` + labelFieldsFragment

// queryProjectLabelsPage drains the workspace project-label catalog. No
// filter: the drain must include retired and group labels — completeness is
// what licenses the sync pass's full-table prune (retirement is
// keep-but-not-newly-assignable, so a retired label absent from the drain
// would read as deleted and be pruned, which live verification 2026-07-08
// confirmed does not happen: retired labels ARE in the default drain).
var queryProjectLabelsPage = `
query ProjectLabelsPage($after: String) {
  projectLabels(first: 250, after: $after) {
    pageInfo { hasNextPage endCursor }
    nodes { ...ProjectLabelFields }
  }
}
` + projectLabelFieldsFragment

// ProjectFields is the shared projection for a project — the team-projects
// page, the single-project fetch (the WriteBack verify read), and the create
// mutation's echo all project through it, per the fragment rule: an inlined
// copy silently drifts when one site gains a field (the create echo had
// already drifted, omitting startDate/targetDate/lead/status/initiatives/
// milestones/labelIds). References ProjectMilestoneFields; queries appending
// this fragment get that one with it.
const projectFieldsFragment = `
fragment ProjectFields on Project {
  id
  name
  slugId
  description
  url
  state
  startDate
  targetDate
  createdAt
  updatedAt
  labelIds
  lead {
    id
    name
    email
  }
  status {
    id
    name
  }
  initiatives {
    nodes {
      id
      name
    }
  }
  projectMilestones {
    nodes { ...ProjectMilestoneFields }
  }
}
` + projectMilestoneFieldsFragment

// queryTeamProjects pages at 50: the nested initiatives/projectMilestones
// selections cost ~187 complexity points per project node, so 50 is the
// largest page that fits Linear's 10k complexity budget (measured live:
// first:100 scores 18751).
var queryTeamProjects = `
query TeamProjects($teamId: String!, $after: String) {
  team(id: $teamId) {
    projects(first: 50, after: $after) {
      pageInfo { hasNextPage endCursor }
      nodes { ...ProjectFields }
    }
  }
}
` + projectFieldsFragment

// queryTeamProjectsByUpdatedAt fetches a team's projects newest-first — the
// lean cycle's change-detection probe and its resume pages (#243), the
// projects sibling of queryTeamIssuesByUpdatedAt. $first is a variable, not a
// constant, because the two callers want different pages: the probe pays for
// only a handful of nodes (each costs ~187 complexity via the nested
// milestone/initiative selections), while a resume page uses the full-drain
// page size. Nodes project through ProjectFields, identical to
// queryTeamProjects — the fragment rule: a probed project must carry the
// same fields a drained one does.
var queryTeamProjectsByUpdatedAt = `
query TeamProjectsByUpdatedAt($teamId: String!, $first: Int!, $after: String) {
  team(id: $teamId) {
    projects(first: $first, after: $after, orderBy: updatedAt) {
      pageInfo { hasNextPage endCursor }
      nodes { ...ProjectFields }
    }
  }
}
` + projectFieldsFragment

var queryProject = `
query Project($id: String!) {
  project(id: $id) { ...ProjectFields }
}
` + projectFieldsFragment

// =============================================================================
// Project Milestones Mutations
// =============================================================================

const mutationUpdateProject = `
mutation UpdateProject($id: String!, $input: ProjectUpdateInput!) {
  projectUpdate(id: $id, input: $input) {
    success
  }
}
`

const mutationUpdateInitiative = `
mutation UpdateInitiative($id: String!, $input: InitiativeUpdateInput!) {
  initiativeUpdate(id: $id, input: $input) {
    success
  }
}
`

var mutationCreateProjectMilestone = `
mutation CreateProjectMilestone($projectId: String!, $name: String!, $description: String) {
  projectMilestoneCreate(input: { projectId: $projectId, name: $name, description: $description }) {
    success
    projectMilestone { ...ProjectMilestoneFields }
  }
}
` + projectMilestoneFieldsFragment

var mutationUpdateProjectMilestone = `
mutation UpdateProjectMilestone($id: String!, $input: ProjectMilestoneUpdateInput!) {
  projectMilestoneUpdate(id: $id, input: $input) {
    success
    projectMilestone { ...ProjectMilestoneFields }
  }
}
` + projectMilestoneFieldsFragment

const mutationDeleteProjectMilestone = `
mutation DeleteProjectMilestone($id: String!) {
  projectMilestoneDelete(id: $id) {
    success
  }
}
`

// queryProjectUpdates drains: updates accumulate past 50 over a project's
// lifetime, and the SWR refresh is upsert-only — the implicit 50-cap was
// silently freezing completeness.
var queryProjectUpdates = `
query ProjectUpdates($projectId: String!, $after: String) {
  project(id: $projectId) {
    projectUpdates(first: 50, after: $after) {
      pageInfo { hasNextPage endCursor }
      nodes { ...ProjectUpdateFields }
    }
  }
}
` + projectUpdateFieldsFragment

var mutationCreateProjectUpdate = `
mutation CreateProjectUpdate($projectId: String!, $body: String!, $health: ProjectUpdateHealthType) {
  projectUpdateCreate(input: {projectId: $projectId, body: $body, health: $health}) {
    success
    projectUpdate { ...ProjectUpdateFields }
  }
}
` + projectUpdateFieldsFragment

// queryInitiativeUpdates drains, for the same reason as queryProjectUpdates.
var queryInitiativeUpdates = `
query InitiativeUpdates($initiativeId: String!, $after: String) {
  initiative(id: $initiativeId) {
    initiativeUpdates(first: 50, after: $after) {
      pageInfo { hasNextPage endCursor }
      nodes { ...InitiativeUpdateFields }
    }
  }
}
` + initiativeUpdateFieldsFragment

var mutationCreateInitiativeUpdate = `
mutation CreateInitiativeUpdate($initiativeId: String!, $body: String!, $health: InitiativeUpdateHealthType) {
  initiativeUpdateCreate(input: {initiativeId: $initiativeId, body: $body, health: $health}) {
    success
    initiativeUpdate { ...InitiativeUpdateFields }
  }
}
` + initiativeUpdateFieldsFragment

var mutationCreateProject = `
mutation CreateProject($input: ProjectCreateInput!) {
  projectCreate(input: $input) {
    success
    project { ...ProjectFields }
  }
}
` + projectFieldsFragment

const mutationArchiveProject = `
mutation ArchiveProject($id: String!) {
  projectArchive(id: $id) {
    success
  }
}
`

const mutationInitiativeToProjectCreate = `
mutation InitiativeToProjectCreate($initiativeId: String!, $projectId: String!) {
  initiativeToProjectCreate(initiativeId: $initiativeId, projectId: $projectId) {
    success
  }
}
`

const mutationInitiativeToProjectDelete = `
mutation InitiativeToProjectDelete($initiativeId: String!, $projectId: String!) {
  initiativeToProjectDelete(initiativeId: $initiativeId, projectId: $projectId) {
    success
  }
}
`

// queryWorkspace fetches workspace-level entities (users and initiatives)
// in a single query. Initiatives page at 50 because each node carries a
// nested projects connection; that nested connection selects pageInfo too,
// and GetWorkspace drains it per initiative (an initiative's junction rows
// feed a prune, so its project list must be provably complete — a
// truncated list would read as removals).
var queryWorkspace = `
query Workspace {
  users(first: 250) {
    pageInfo { hasNextPage endCursor }
    nodes { ...UserFields }
  }
  initiatives(first: 50) {
    pageInfo { hasNextPage endCursor }
    nodes {
      ...InitiativeFields
      projects(first: 50) {
        pageInfo { hasNextPage endCursor }
        nodes {
          id
          name
          slugId
        }
      }
    }
  }
}
` + userFieldsFragment + initiativeFieldsFragment

var queryWorkspaceUsersPage = `
query WorkspaceUsersPage($after: String) {
  users(first: 250, after: $after) {
    pageInfo { hasNextPage endCursor }
    nodes { ...UserFields }
  }
}
` + userFieldsFragment

var queryWorkspaceInitiativesPage = `
query WorkspaceInitiativesPage($after: String) {
  initiatives(first: 50, after: $after) {
    pageInfo { hasNextPage endCursor }
    nodes {
      ...InitiativeFields
      projects(first: 50) {
        pageInfo { hasNextPage endCursor }
        nodes {
          id
          name
          slugId
        }
      }
    }
  }
}
` + initiativeFieldsFragment

const queryInitiativeProjectsPage = `
query InitiativeProjectsPage($id: String!, $after: String) {
  initiative(id: $id) {
    projects(first: 250, after: $after) {
      pageInfo { hasNextPage endCursor }
      nodes {
        id
        name
        slugId
      }
    }
  }
}
`

var queryViewer = `
query Viewer {
  viewer { ...UserFields }
}
` + userFieldsFragment

const mutationUpdateIssue = `
mutation UpdateIssue($id: String!, $input: IssueUpdateInput!) {
  issueUpdate(id: $id, input: $input) {
    success
    issue {
      id
      updatedAt
    }
  }
}
`

var mutationCreateIssue = `
mutation CreateIssue($input: IssueCreateInput!) {
  issueCreate(input: $input) {
    success
    issue { ...IssueFieldsLite }
  }
}
` + issueFieldsFragmentLite

const mutationArchiveIssue = `
mutation ArchiveIssue($id: String!) {
  issueArchive(id: $id) {
    success
  }
}
`

// IssueDetailsPageSize is the `first:` page cap on the issue-details queries
// (single and batch). Exported because the sync worker's stale-row pruning may
// only treat a fetched set as complete when its length is below this cap — a
// full page may be truncated, and pruning against a truncated set would delete
// real rows.
const IssueDetailsPageSize = 100

// IssueRelationsPageSize caps the relations/inverseRelations connections in
// the details queries. Linear caps a connection without `first:` at 50
// anyway; a short page (len < IssueRelationsPageSize) is provably complete,
// which is what licenses the relations prune.
const IssueRelationsPageSize = 50

// IssueDetailsSelection is the per-issue selection body shared by the
// single-issue details query and every alias of the batch query, so the two
// can never drift. The relation selections mirror the IssueFields fragment's
// (the row needs only the ids; identifier/title ride along for parity).
var IssueDetailsSelection = fmt.Sprintf(`comments(first: %d) { nodes { ...CommentFields } }
    documents(first: %d) { nodes { ...DocumentFields } }
    attachments(first: %d) { nodes { ...AttachmentFields } }
    relations(first: %d) { nodes { ...IssueRelationFields } }
    inverseRelations(first: %d) { nodes { ...IssueInverseRelationFields } }`,
	IssueDetailsPageSize, IssueDetailsPageSize, IssueDetailsPageSize, IssueRelationsPageSize, IssueRelationsPageSize)

// queryIssueDetails fetches comments, documents, attachments, and relations
// for an issue in one query
var queryIssueDetails = fmt.Sprintf(`
query IssueDetails($issueId: String!) {
  issue(id: $issueId) {
    %s
  }
}
`, IssueDetailsSelection) +
	CommentFieldsFragment + DocumentFieldsFragment + AttachmentFieldsFragment +
	issueRelationFieldsFragment + issueInverseRelationFieldsFragment

// queryIssueAttachments fetches only attachments for an issue.
//
// DELIBERATE cap: first: 100, single page, no drain. This query serves the
// interactive attachment-create re-check (the authoritative read a user's
// FUSE write blocks on), and fetchAll's LowBudget gate must never sit on a
// write path — a low budget would turn a create into a spurious failure. An
// issue with more than 100 attachments is out of scope.
var queryIssueAttachments = `
query IssueAttachments($issueId: String!) {
  issue(id: $issueId) {
    attachments(first: 100) {
      nodes { ...AttachmentFields }
    }
  }
}
` + AttachmentFieldsFragment

var mutationCreateComment = `
mutation CreateComment($issueId: String!, $body: String!) {
  commentCreate(input: { issueId: $issueId, body: $body }) {
    success
    comment { ...CommentFields }
  }
}
` + CommentFieldsFragment

var mutationUpdateComment = `
mutation UpdateComment($id: String!, $body: String!) {
  commentUpdate(id: $id, input: { body: $body }) {
    success
    comment { ...CommentFields }
  }
}
` + CommentFieldsFragment

const mutationDeleteComment = `
mutation DeleteComment($id: String!) {
  commentDelete(id: $id) {
    success
  }
}
`

var queryProjectDocuments = `
query ProjectDocuments($projectId: ID!, $after: String) {
  documents(first: 100, after: $after, filter: { project: { id: { eq: $projectId } } }) {
    pageInfo { hasNextPage endCursor }
    nodes { ...DocumentFields }
  }
}
` + DocumentFieldsFragment

var queryInitiativeDocuments = `
query InitiativeDocuments($initiativeId: ID!, $after: String) {
  documents(first: 100, after: $after, filter: { initiative: { id: { eq: $initiativeId } } }) {
    pageInfo { hasNextPage endCursor }
    nodes { ...DocumentFields }
  }
}
` + DocumentFieldsFragment

var mutationCreateDocument = `
mutation CreateDocument($input: DocumentCreateInput!) {
  documentCreate(input: $input) {
    success
    document { ...DocumentFields }
  }
}
` + DocumentFieldsFragment

var mutationUpdateDocument = `
mutation UpdateDocument($id: String!, $input: DocumentUpdateInput!) {
  documentUpdate(id: $id, input: $input) {
    success
    document { ...DocumentFields }
  }
}
` + DocumentFieldsFragment

const mutationDeleteDocument = `
mutation DeleteDocument($id: String!) {
  documentDelete(id: $id) {
    success
  }
}
`

var mutationCreateLabel = `
mutation CreateLabel($input: IssueLabelCreateInput!) {
  issueLabelCreate(input: $input) {
    success
    issueLabel { ...LabelFields }
  }
}
` + labelFieldsFragment

var mutationUpdateLabel = `
mutation UpdateLabel($id: String!, $input: IssueLabelUpdateInput!) {
  issueLabelUpdate(id: $id, input: $input) {
    success
    issueLabel { ...LabelFields }
  }
}
` + labelFieldsFragment

const mutationDeleteLabel = `
mutation DeleteLabel($id: String!) {
  issueLabelDelete(id: $id) {
    success
  }
}
`

var queryInitiative = `
query Initiative($id: String!) {
  initiative(id: $id) {
    ...InitiativeFields
    projects(first: 250) {
      pageInfo { hasNextPage endCursor }
      nodes {
        id
        name
        slugId
      }
    }
  }
}
` + initiativeFieldsFragment

// =============================================================================
// Issue Relations
// =============================================================================

const mutationCreateIssueRelation = `
mutation CreateIssueRelation($issueId: String!, $relatedIssueId: String!, $type: IssueRelationType!) {
  issueRelationCreate(input: { issueId: $issueId, relatedIssueId: $relatedIssueId, type: $type }) {
    success
    issueRelation {
      id
      type
      issue { id identifier title }
      relatedIssue { id identifier title }
      createdAt
      updatedAt
    }
  }
}
`

const mutationDeleteIssueRelation = `
mutation DeleteIssueRelation($id: String!) {
  issueRelationDelete(id: $id) {
    success
  }
}
`

// =============================================================================
// Attachments Create/Link
// =============================================================================

var mutationCreateAttachment = `
mutation CreateAttachment($issueId: String!, $title: String!, $url: String!, $subtitle: String) {
  attachmentCreate(input: { issueId: $issueId, title: $title, url: $url, subtitle: $subtitle }) {
    success
    attachment { ...AttachmentFields }
  }
}
` + AttachmentFieldsFragment

var mutationLinkURL = `
mutation AttachmentLinkURL($issueId: String!, $url: String!, $title: String) {
  attachmentLinkURL(issueId: $issueId, url: $url, title: $title) {
    success
    attachment { ...AttachmentFields }
  }
}
` + AttachmentFieldsFragment

const mutationDeleteAttachment = `
mutation DeleteAttachment($id: String!) {
  attachmentDelete(id: $id) {
    success
  }
}
`

// queryIssueHistory fetches the history/audit trail for an issue, drained —
// it backs history.md live, and an old issue's audit trail outgrows a page.
const queryIssueHistory = `
query IssueHistory($issueId: String!, $after: String) {
  issue(id: $issueId) {
    history(first: 100, after: $after) {
      pageInfo { hasNextPage endCursor }
      nodes {
        id
        createdAt
        actor { id name email }
        fromAssignee { id name email }
        toAssignee { id name email }
        fromState { id name type }
        toState { id name type }
        fromPriority
        toPriority
        fromTitle
        toTitle
        fromDueDate
        toDueDate
        fromEstimate
        toEstimate
        fromParent { id identifier }
        toParent { id identifier }
        fromProject { id name }
        toProject { id name }
        fromCycle { id name }
        toCycle { id name }
        addedLabels { id name }
        removedLabels { id name }
        updatedDescription
      }
    }
  }
}
`

// queryTeamIssueIDs paginates issue IDs for a team. Used by the
// reconciliation pass to enumerate the authoritative set of issue IDs
// without paying the cost of full IssueFields.
const queryTeamIssueIDs = `
query TeamIssueIDs($teamId: String!, $after: String) {
  team(id: $teamId) {
    issues(first: 100, after: $after) {
      pageInfo { hasNextPage endCursor }
      nodes { id }
    }
  }
}
`

// queryWorkspaceProjectIDs returns IDs of all projects in the workspace,
// paginated. The reconcile pass diffs-and-deletes against this set, so it
// must be complete or fail loudly — a truncated page would read as mass
// deletion (the paginate module guarantees all-or-nothing).
const queryWorkspaceProjectIDs = `
query WorkspaceProjectIDs($after: String) {
  projects(first: 250, after: $after) {
    pageInfo { hasNextPage endCursor }
    nodes { id }
  }
}
`

// queryWorkspaceInitiativeIDs returns IDs of all initiatives in the
// workspace, paginated. See queryWorkspaceProjectIDs for why completeness
// is load-bearing.
const queryWorkspaceInitiativeIDs = `
query WorkspaceInitiativeIDs($after: String) {
  initiatives(first: 250, after: $after) {
    pageInfo { hasNextPage endCursor }
    nodes { id }
  }
}
`
