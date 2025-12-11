package api

const queryTeams = `
query Teams {
  teams {
    nodes {
      id
      key
      name
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
          }
        }
        dueDate
        estimate
        createdAt
        updatedAt
        url
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
