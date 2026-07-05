package api

import "fmt"

const queryTeams = `
query Teams {
  teams {
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
` + issueFieldsFragment

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
  relations { nodes { id type relatedIssue { id identifier title } } }
  inverseRelations { nodes { id type issue { id identifier title } } }
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
      nodes {
        id
        number
        name
        startsAt
        endsAt
        completedIssueCountHistory
        issueCountHistory
      }
    }
    members(first: 250) {
      pageInfo { hasNextPage endCursor }
      nodes {
        id
        name
        email
        displayName
        active
      }
    }
  }
  issueLabels(first: 250) {
    pageInfo { hasNextPage endCursor }
    nodes { ...LabelFields }
  }
}
` + labelFieldsFragment

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

const queryTeamCyclesPage = `
query TeamCyclesPage($teamId: String!, $after: String) {
  team(id: $teamId) {
    cycles(first: 250, after: $after) {
      pageInfo { hasNextPage endCursor }
      nodes {
        id
        number
        name
        startsAt
        endsAt
        completedIssueCountHistory
        issueCountHistory
      }
    }
  }
}
`

const queryTeamMembersPage = `
query TeamMembersPage($teamId: String!, $after: String) {
  team(id: $teamId) {
    members(first: 250, after: $after) {
      pageInfo { hasNextPage endCursor }
      nodes {
        id
        name
        email
        displayName
        active
      }
    }
  }
}
`

var queryWorkspaceLabelsPage = `
query WorkspaceLabelsPage($after: String) {
  issueLabels(first: 250, after: $after) {
    pageInfo { hasNextPage endCursor }
    nodes { ...LabelFields }
  }
}
` + labelFieldsFragment

const queryTeamStates = `
query TeamStates($teamId: String!) {
  team(id: $teamId) {
    states {
      nodes {
        id
        name
        type
      }
    }
  }
}
`

var queryTeamLabels = `
query TeamLabels($teamId: String!) {
  team(id: $teamId) {
    labels {
      nodes { ...LabelFields }
    }
  }
  issueLabels {
    nodes { ...LabelFields }
  }
}
` + labelFieldsFragment

const queryTeamCycles = `
query TeamCycles($teamId: String!) {
  team(id: $teamId) {
    cycles {
      nodes {
        id
        number
        name
        startsAt
        endsAt
        completedIssueCountHistory
        issueCountHistory
      }
    }
  }
}
`

// queryTeamProjects pages at 50: the nested initiatives/projectMilestones
// selections cost ~187 complexity points per project node, so 50 is the
// largest page that fits Linear's 10k complexity budget (measured live:
// first:100 scores 18751).
const queryTeamProjects = `
query TeamProjects($teamId: String!, $after: String) {
  team(id: $teamId) {
    projects(first: 50, after: $after) {
      pageInfo { hasNextPage endCursor }
      nodes {
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
          nodes {
            id
            name
            description
            targetDate
            sortOrder
          }
        }
      }
    }
  }
}
`

const queryProject = `
query Project($id: String!) {
  project(id: $id) {
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
      nodes {
        id
        name
        description
        targetDate
        sortOrder
      }
    }
  }
}
`

const queryProjectMilestones = `
query ProjectMilestones($projectId: String!) {
  project(id: $projectId) {
    projectMilestones {
      nodes {
        id
        name
        description
        targetDate
        sortOrder
      }
    }
  }
}
`

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

const mutationCreateProjectMilestone = `
mutation CreateProjectMilestone($projectId: String!, $name: String!, $description: String) {
  projectMilestoneCreate(input: { projectId: $projectId, name: $name, description: $description }) {
    success
    projectMilestone {
      id
      name
      description
      targetDate
      sortOrder
    }
  }
}
`

const mutationUpdateProjectMilestone = `
mutation UpdateProjectMilestone($id: String!, $input: ProjectMilestoneUpdateInput!) {
  projectMilestoneUpdate(id: $id, input: $input) {
    success
    projectMilestone {
      id
      name
      description
      targetDate
      sortOrder
    }
  }
}
`

const mutationDeleteProjectMilestone = `
mutation DeleteProjectMilestone($id: String!) {
  projectMilestoneDelete(id: $id) {
    success
  }
}
`

const queryProjectUpdates = `
query ProjectUpdates($projectId: String!) {
  project(id: $projectId) {
    projectUpdates {
      nodes {
        id
        body
        health
        createdAt
        updatedAt
        user {
          id
          name
          email
        }
      }
    }
  }
}
`

const mutationCreateProjectUpdate = `
mutation CreateProjectUpdate($projectId: String!, $body: String!, $health: ProjectUpdateHealthType) {
  projectUpdateCreate(input: {projectId: $projectId, body: $body, health: $health}) {
    success
    projectUpdate {
      id
      body
      health
      createdAt
      user {
        id
        name
        email
      }
    }
  }
}
`

const queryInitiativeUpdates = `
query InitiativeUpdates($initiativeId: String!) {
  initiative(id: $initiativeId) {
    initiativeUpdates {
      nodes {
        id
        body
        health
        createdAt
        updatedAt
        user {
          id
          name
          email
        }
      }
    }
  }
}
`

const mutationCreateInitiativeUpdate = `
mutation CreateInitiativeUpdate($initiativeId: String!, $body: String!, $health: InitiativeUpdateHealthType) {
  initiativeUpdateCreate(input: {initiativeId: $initiativeId, body: $body, health: $health}) {
    success
    initiativeUpdate {
      id
      body
      health
      createdAt
      user {
        id
        name
        email
      }
    }
  }
}
`

const mutationCreateProject = `
mutation CreateProject($input: ProjectCreateInput!) {
  projectCreate(input: $input) {
    success
    project {
      id
      name
      slugId
      description
      url
      state
      createdAt
      updatedAt
    }
  }
}
`

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
const queryWorkspace = `
query Workspace {
  users(first: 250) {
    pageInfo { hasNextPage endCursor }
    nodes {
      id
      name
      email
      displayName
      active
    }
  }
  initiatives(first: 50) {
    pageInfo { hasNextPage endCursor }
    nodes {
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
`

const queryWorkspaceUsersPage = `
query WorkspaceUsersPage($after: String) {
  users(first: 250, after: $after) {
    pageInfo { hasNextPage endCursor }
    nodes {
      id
      name
      email
      displayName
      active
    }
  }
}
`

const queryWorkspaceInitiativesPage = `
query WorkspaceInitiativesPage($after: String) {
  initiatives(first: 50, after: $after) {
    pageInfo { hasNextPage endCursor }
    nodes {
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
`

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

const queryUsers = `
query Users {
  users {
    nodes {
      id
      name
      email
      displayName
      active
    }
  }
}
`

const queryViewer = `
query Viewer {
  viewer {
    id
    name
    email
    displayName
    active
  }
}
`

const queryTeamMembers = `
query TeamMembers($teamId: String!) {
  team(id: $teamId) {
    members {
      nodes {
        id
        name
        email
        displayName
        active
      }
    }
  }
}
`

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

const mutationCreateIssue = `
mutation CreateIssue($input: IssueCreateInput!) {
  issueCreate(input: $input) {
    success
    issue {
      id
      identifier
      title
      description
      state {
        id
        name
        type
      }
      assignee {
        id
        name
        email
      }
      priority
      labels {
        nodes {
          id
          name
          color
          description
        }
      }
      createdAt
      updatedAt
      url
      team {
        id
        key
        name
      }
    }
  }
}
`

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

// queryIssueDetails fetches comments, documents, and attachments for an issue in one query
var queryIssueDetails = fmt.Sprintf(`
query IssueDetails($issueId: String!) {
  issue(id: $issueId) {
    comments(first: %d) {
      nodes { ...CommentFields }
    }
    documents(first: %d) {
      nodes { ...DocumentFields }
    }
    attachments(first: %d) {
      nodes { ...AttachmentFields }
    }
  }
}
`, IssueDetailsPageSize, IssueDetailsPageSize, IssueDetailsPageSize) +
	CommentFieldsFragment + DocumentFieldsFragment + AttachmentFieldsFragment

// queryIssueAttachments fetches only attachments for an issue
var queryIssueAttachments = `
query IssueAttachments($issueId: String!) {
  issue(id: $issueId) {
    attachments(first: 100) {
      nodes { ...AttachmentFields }
    }
  }
}
` + AttachmentFieldsFragment

var queryIssueComments = `
query IssueComments($issueId: String!) {
  issue(id: $issueId) {
    comments(first: 100) {
      nodes { ...CommentFields }
    }
  }
}
` + CommentFieldsFragment

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

var queryIssueDocuments = `
query IssueDocuments($issueId: String!) {
  issue(id: $issueId) {
    documents(first: 100) {
      nodes { ...DocumentFields }
    }
  }
}
` + DocumentFieldsFragment

var queryProjectDocuments = `
query ProjectDocuments($projectId: ID!) {
  documents(first: 100, filter: { project: { id: { eq: $projectId } } }) {
    nodes { ...DocumentFields }
  }
}
` + DocumentFieldsFragment

var queryInitiativeDocuments = `
query InitiativeDocuments($initiativeId: ID!) {
  documents(first: 100, filter: { initiative: { id: { eq: $initiativeId } } }) {
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

// Filtered team issues queries - server-side filtering for by/ directories

const queryInitiatives = `
query Initiatives {
  initiatives {
    nodes {
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
      projects {
        nodes {
          id
          name
          slugId
        }
      }
    }
  }
}
`

const queryInitiative = `
query Initiative($id: String!) {
  initiative(id: $id) {
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
    projects {
      nodes {
        id
        name
        slugId
      }
    }
  }
}
`

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

const mutationCreateAttachment = `
mutation CreateAttachment($issueId: String!, $title: String!, $url: String!, $subtitle: String) {
  attachmentCreate(input: { issueId: $issueId, title: $title, url: $url, subtitle: $subtitle }) {
    success
    attachment {
      id
      title
      subtitle
      url
      sourceType
      createdAt
      updatedAt
    }
  }
}
`

const mutationLinkURL = `
mutation AttachmentLinkURL($issueId: String!, $url: String!, $title: String) {
  attachmentLinkURL(issueId: $issueId, url: $url, title: $title) {
    success
    attachment {
      id
      title
      subtitle
      url
      sourceType
      createdAt
      updatedAt
    }
  }
}
`

const mutationDeleteAttachment = `
mutation DeleteAttachment($id: String!) {
  attachmentDelete(id: $id) {
    success
  }
}
`

// queryIssueHistory fetches the history/audit trail for an issue
const queryIssueHistory = `
query IssueHistory($issueId: String!) {
  issue(id: $issueId) {
    history(first: 100) {
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
query TeamIssueIDs($teamId: String!, $first: Int!, $after: String) {
  team(id: $teamId) {
    issues(first: $first, after: $after) {
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
