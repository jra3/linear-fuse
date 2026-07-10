package fs

import (
	"context"

	"github.com/jra3/linear-fuse/internal/api"
)

// MutationClient is the narrow slice of the Linear API that write handlers use to
// mutate state (create/update/delete/archive/link). Reads and infrastructure
// (GetIssue, GetViewer, Stats, Close, …) stay on the concrete *api.Client — only
// mutations go through this seam so a test can inject an in-memory fake and
// exercise the *success* half of the write contract offline.
//
// The concrete *api.Client satisfies this interface directly, so production
// wiring is unchanged (LinearFS.mutator is set to the same client it uses for
// reads). Fixture-mode tests call InjectTestMutationClient to swap in a fake;
// see internal/testutil/mockmutation.
type MutationClient interface {
	// Issues
	CreateIssue(ctx context.Context, input map[string]any) (*api.Issue, error)
	UpdateIssue(ctx context.Context, issueID string, input map[string]any) error
	ArchiveIssue(ctx context.Context, issueID string) error

	// Comments
	CreateComment(ctx context.Context, issueID string, body string) (*api.Comment, error)
	UpdateComment(ctx context.Context, commentID string, body string) (*api.Comment, error)
	DeleteComment(ctx context.Context, commentID string) error

	// Documents
	CreateDocument(ctx context.Context, input map[string]any) (*api.Document, error)
	UpdateDocument(ctx context.Context, documentID string, input map[string]any) (*api.Document, error)
	DeleteDocument(ctx context.Context, documentID string) error

	// Labels
	CreateLabel(ctx context.Context, input map[string]any) (*api.Label, error)
	UpdateLabel(ctx context.Context, id string, input map[string]any) (*api.Label, error)
	DeleteLabel(ctx context.Context, id string) error

	// Projects
	CreateProject(ctx context.Context, input map[string]any) (*api.Project, error)
	UpdateProject(ctx context.Context, projectID string, input api.ProjectUpdateInput) error
	ArchiveProject(ctx context.Context, projectID string) error

	// Project milestones
	CreateProjectMilestone(ctx context.Context, projectID, name, description string) (*api.ProjectMilestone, error)
	UpdateProjectMilestone(ctx context.Context, milestoneID string, input api.ProjectMilestoneUpdateInput) (*api.ProjectMilestone, error)
	DeleteProjectMilestone(ctx context.Context, milestoneID string) error

	// Status updates
	CreateProjectUpdate(ctx context.Context, projectID, body, health string) (*api.ProjectUpdate, error)
	CreateInitiativeUpdate(ctx context.Context, initiativeID, body, health string) (*api.InitiativeUpdate, error)

	// Initiatives
	UpdateInitiative(ctx context.Context, initiativeID string, input api.InitiativeUpdateInput) error
	AddProjectToInitiative(ctx context.Context, projectID, initiativeID string) error
	RemoveProjectFromInitiative(ctx context.Context, projectID, initiativeID string) error

	// Relations
	CreateIssueRelation(ctx context.Context, issueID, relatedIssueID, relationType string) (*api.IssueRelation, error)
	DeleteIssueRelation(ctx context.Context, relationID string) error

	// Attachments
	LinkURL(ctx context.Context, issueID, url, title string) (*api.Attachment, error)
	DeleteAttachment(ctx context.Context, attachmentID string) error

	// Entity external links (project/initiative "Links / Resources")
	CreateEntityExternalLink(ctx context.Context, input map[string]any) (*api.EntityExternalLink, error)
	DeleteEntityExternalLink(ctx context.Context, id string) error
}

// compile-time assertion that the concrete client satisfies the seam.
var _ MutationClient = (*api.Client)(nil)

// verifyReader is the read-your-writes surface the edit-commit tail uses to
// re-fetch an entity after a mutation and verify what actually persisted. It is
// split from MutationClient (mutations-only) so a test can serve the verify
// fetch from an in-memory fake. With a fixture API key the real client's
// re-fetch 401s, so commitWriteBack takes its "unverified success" early return
// and the edit tail (fetch → persist → compare) never runs offline — making the
// no-op byte-stability conformance checks vacuous. Injecting a store-backed
// reader lets that tail actually execute in fixture mode.
//
// The concrete *api.Client satisfies this directly, so production wiring is
// unchanged (LinearFS.verifier is the same client used for every other read).
type verifyReader interface {
	GetIssue(ctx context.Context, issueID string) (*api.Issue, error)
	GetProject(ctx context.Context, projectID string) (*api.Project, error)
	GetInitiative(ctx context.Context, initiativeID string) (*api.Initiative, error)
}

// compile-time assertion that the concrete client satisfies the verify seam.
var _ verifyReader = (*api.Client)(nil)
