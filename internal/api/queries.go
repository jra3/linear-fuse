package api

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

const queryTeamIssues = `
query TeamIssues($teamId: String!, $after: String) {
  team(id: $teamId) {
    issues(first: 100, after: $after) {
      pageInfo {
        hasNextPage
        endCursor
      }
      nodes {
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
        dueDate
        estimate
        createdAt
        updatedAt
        url
        team {
          id
          key
          name
        }
        project {
          id
          name
          slugId
        }
        projectMilestone {
          id
          name
        }
        parent {
          id
          identifier
          title
        }
        children {
          nodes {
            id
            identifier
            title
          }
        }
      }
    }
  }
}
`

const queryIssue = `
query Issue($id: String!) {
  issue(id: $id) {
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
    dueDate
    estimate
    createdAt
    updatedAt
    url
    team {
      id
      key
      name
    }
    project {
      id
      name
      slugId
    }
    projectMilestone {
      id
      name
    }
    parent {
      id
      identifier
      title
    }
    children {
      nodes {
        id
        identifier
        title
      }
    }
  }
}
`

const issueFields = `
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
  dueDate
  estimate
  createdAt
  updatedAt
  url
  team {
    id
    key
    name
  }
  project {
    id
    name
    slugId
  }
  projectMilestone {
    id
    name
  }
  parent {
    id
    identifier
    title
  }
  children {
    nodes {
      id
      identifier
      title
    }
  }
`

const queryMyIssues = `
query MyIssues($after: String) {
  viewer {
    assignedIssues(first: 100, after: $after) {
      pageInfo {
        hasNextPage
        endCursor
      }
      nodes {
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
        dueDate
        estimate
        createdAt
        updatedAt
        url
        team {
          id
          key
          name
        }
        project {
          id
          name
          slugId
        }
        projectMilestone {
          id
          name
        }
        parent {
          id
          identifier
          title
        }
        children {
          nodes {
            id
            identifier
            title
          }
        }
      }
    }
  }
}
`

const queryMyCreatedIssues = `
query MyCreatedIssues($after: String) {
  viewer {
    createdIssues(first: 100, after: $after) {
      pageInfo {
        hasNextPage
        endCursor
      }
      nodes {
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
        dueDate
        estimate
        createdAt
        updatedAt
        url
        team {
          id
          key
          name
        }
        project {
          id
          name
          slugId
        }
        projectMilestone {
          id
          name
        }
        parent {
          id
          identifier
          title
        }
        children {
          nodes {
            id
            identifier
            title
          }
        }
      }
    }
  }
}
`

const queryMyActiveIssues = `
query MyActiveIssues($after: String) {
  viewer {
    assignedIssues(
      first: 100
      after: $after
      filter: {
        state: { type: { nin: ["completed", "canceled"] } }
      }
    ) {
      pageInfo {
        hasNextPage
        endCursor
      }
      nodes {
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
        dueDate
        estimate
        createdAt
        updatedAt
        url
        team {
          id
          key
          name
        }
        project {
          id
          name
          slugId
        }
        projectMilestone {
          id
          name
        }
        parent {
          id
          identifier
          title
        }
        children {
          nodes {
            id
            identifier
            title
          }
        }
      }
    }
  }
}
`

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

const queryTeamLabels = `
query TeamLabels($teamId: String!) {
  team(id: $teamId) {
    labels {
      nodes {
        id
        name
        color
        description
      }
    }
  }
  issueLabels {
    nodes {
      id
      name
      color
      description
    }
  }
}
`

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

const queryCycleIssues = `
query CycleIssues($cycleId: String!, $after: String) {
  cycle(id: $cycleId) {
    issues(first: 100, after: $after) {
      pageInfo {
        hasNextPage
        endCursor
      }
      nodes {
        id
        identifier
        title
        updatedAt
        team {
          id
          key
        }
      }
    }
  }
}
`

const queryTeamProjects = `
query TeamProjects($teamId: String!) {
  team(id: $teamId) {
    projects {
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
      }
    }
  }
}
`

const queryProjectIssues = `
query ProjectIssues($projectId: String!, $after: String) {
  project(id: $projectId) {
    issues(first: 100, after: $after) {
      pageInfo {
        hasNextPage
        endCursor
      }
      nodes {
        id
        identifier
        title
        team {
          id
          key
        }
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

const queryUserIssues = `
query UserIssues($userId: String!, $after: String) {
  user(id: $userId) {
    assignedIssues(first: 100, after: $after) {
      pageInfo {
        hasNextPage
        endCursor
      }
      nodes {
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
        dueDate
        estimate
        createdAt
        updatedAt
        url
        team {
          id
          key
          name
        }
        project {
          id
          name
          slugId
        }
        projectMilestone {
          id
          name
        }
        parent {
          id
          identifier
          title
        }
        children {
          nodes {
            id
            identifier
            title
          }
        }
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

const queryIssueComments = `
query IssueComments($issueId: String!) {
  issue(id: $issueId) {
    comments(first: 100) {
      nodes {
        id
        body
        createdAt
        updatedAt
        editedAt
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

const mutationCreateComment = `
mutation CreateComment($issueId: String!, $body: String!) {
  commentCreate(input: { issueId: $issueId, body: $body }) {
    success
    comment {
      id
      body
      createdAt
      updatedAt
      editedAt
      user {
        id
        name
        email
      }
    }
  }
}
`

const mutationUpdateComment = `
mutation UpdateComment($id: String!, $body: String!) {
  commentUpdate(id: $id, input: { body: $body }) {
    success
    comment {
      id
      body
      createdAt
      updatedAt
      editedAt
      user {
        id
        name
        email
      }
    }
  }
}
`

const mutationDeleteComment = `
mutation DeleteComment($id: String!) {
  commentDelete(id: $id) {
    success
  }
}
`

const queryIssueDocuments = `
query IssueDocuments($issueId: String!) {
  issue(id: $issueId) {
    documents(first: 100) {
      nodes {
        id
        title
        content
        slugId
        url
        icon
        color
        createdAt
        updatedAt
        creator {
          id
          name
          email
        }
      }
    }
  }
}
`

const queryProjectDocuments = `
query ProjectDocuments($projectId: ID!) {
  documents(first: 100, filter: { project: { id: { eq: $projectId } } }) {
    nodes {
      id
      title
      content
      slugId
      url
      icon
      color
      createdAt
      updatedAt
      creator {
        id
        name
        email
      }
    }
  }
}
`

const mutationCreateDocument = `
mutation CreateDocument($input: DocumentCreateInput!) {
  documentCreate(input: $input) {
    success
    document {
      id
      title
      content
      slugId
      url
      icon
      color
      createdAt
      updatedAt
      creator {
        id
        name
        email
      }
    }
  }
}
`

const mutationUpdateDocument = `
mutation UpdateDocument($id: String!, $input: DocumentUpdateInput!) {
  documentUpdate(id: $id, input: $input) {
    success
    document {
      id
      title
      content
      slugId
      url
      updatedAt
    }
  }
}
`

const mutationDeleteDocument = `
mutation DeleteDocument($id: String!) {
  documentDelete(id: $id) {
    success
  }
}
`

const mutationCreateLabel = `
mutation CreateLabel($input: IssueLabelCreateInput!) {
  issueLabelCreate(input: $input) {
    success
    issueLabel {
      id
      name
      color
      description
    }
  }
}
`

const mutationUpdateLabel = `
mutation UpdateLabel($id: String!, $input: IssueLabelUpdateInput!) {
  issueLabelUpdate(id: $id, input: $input) {
    success
    issueLabel {
      id
      name
      color
      description
    }
  }
}
`

const mutationDeleteLabel = `
mutation DeleteLabel($id: String!) {
  issueLabelDelete(id: $id) {
    success
  }
}
`

// Filtered team issues queries - server-side filtering for by/ directories

const queryTeamIssuesByStatus = `
query TeamIssuesByStatus($teamId: String!, $statusName: String!, $after: String) {
  team(id: $teamId) {
    issues(first: 100, after: $after, filter: { state: { name: { eq: $statusName } } }) {
      pageInfo {
        hasNextPage
        endCursor
      }
      nodes {
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
        dueDate
        estimate
        createdAt
        updatedAt
        url
        team {
          id
          key
          name
        }
        project {
          id
          name
          slugId
        }
        projectMilestone {
          id
          name
        }
        parent {
          id
          identifier
          title
        }
        children {
          nodes {
            id
            identifier
            title
          }
        }
      }
    }
  }
}
`

const queryTeamIssuesByPriority = `
query TeamIssuesByPriority($teamId: ID!, $priority: Int!, $after: String) {
  issues(first: 100, after: $after, filter: { team: { id: { eq: $teamId } }, priority: { eq: $priority } }) {
    pageInfo {
      hasNextPage
      endCursor
    }
    nodes {
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
      dueDate
      estimate
      createdAt
      updatedAt
      url
      team {
        id
        key
        name
      }
      project {
        id
        name
        slugId
      }
      projectMilestone {
        id
        name
      }
      parent {
        id
        identifier
        title
      }
      children {
        nodes {
          id
          identifier
          title
        }
      }
    }
  }
}
`

const queryTeamIssuesByLabel = `
query TeamIssuesByLabel($teamId: String!, $labelName: String!, $after: String) {
  team(id: $teamId) {
    issues(first: 100, after: $after, filter: { labels: { name: { eq: $labelName } } }) {
      pageInfo {
        hasNextPage
        endCursor
      }
      nodes {
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
        dueDate
        estimate
        createdAt
        updatedAt
        url
        team {
          id
          key
          name
        }
        project {
          id
          name
          slugId
        }
        projectMilestone {
          id
          name
        }
        parent {
          id
          identifier
          title
        }
        children {
          nodes {
            id
            identifier
            title
          }
        }
      }
    }
  }
}
`

const queryTeamIssuesByAssignee = `
query TeamIssuesByAssignee($teamId: String!, $assigneeId: ID!, $after: String) {
  team(id: $teamId) {
    issues(first: 100, after: $after, filter: { assignee: { id: { eq: $assigneeId } } }) {
      pageInfo {
        hasNextPage
        endCursor
      }
      nodes {
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
        dueDate
        estimate
        createdAt
        updatedAt
        url
        team {
          id
          key
          name
        }
        project {
          id
          name
          slugId
        }
        projectMilestone {
          id
          name
        }
        parent {
          id
          identifier
          title
        }
        children {
          nodes {
            id
            identifier
            title
          }
        }
      }
    }
  }
}
`

const queryTeamIssuesUnassigned = `
query TeamIssuesUnassigned($teamId: String!, $after: String) {
  team(id: $teamId) {
    issues(first: 100, after: $after, filter: { assignee: { null: true } }) {
      pageInfo {
        hasNextPage
        endCursor
      }
      nodes {
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
        dueDate
        estimate
        createdAt
        updatedAt
        url
        team {
          id
          key
          name
        }
        project {
          id
          name
          slugId
        }
        projectMilestone {
          id
          name
        }
        parent {
          id
          identifier
          title
        }
        children {
          nodes {
            id
            identifier
            title
          }
        }
      }
    }
  }
}
`

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
