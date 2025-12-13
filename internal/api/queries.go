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
`

const queryMyIssues = `
query MyIssues {
  viewer {
    assignedIssues(first: 100) {
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
