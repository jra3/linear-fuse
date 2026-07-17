// Package mockmutation provides an in-memory fake of the Linear mutation surface
// (the fs.MutationClient interface) for offline tests. It lets fixture-mode
// integration tests exercise the *create* success path of the write contract —
// mkdir / _create reach ClearWriteError/AppendWriteSuccess and upsert to the
// store — without a network or API key.
//
// It also implements fs's read-your-writes verify seam (GetIssue/GetProject/
// GetInitiative), so the *edit* success path is provable offline too: an issue/
// project/initiative Update records the edited free-text fields, and the matching
// getter serves them back (falling back to the store for unedited entities). That
// makes the edit-commit tail (fetch → persist → compare) run against fake state
// in fixture mode instead of taking commitWriteBack's "unverified" early return.
//
// Each create echoes its input into a well-formed entity with a generated,
// unique identity (id/identifier/url) and current timestamps. The fs write
// handlers upsert the returned entity into the injected SQLite store, so
// subsequent reads observe it.
package mockmutation

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jra3/linear-fuse/internal/api"
	"github.com/jra3/linear-fuse/internal/db"
)

// globalSeq is a process-wide counter so identities are unique across every fake
// instance in a test run — independent New() clients share one shared mount/store,
// so a per-instance counter would collide (two issues minted as TST-1001).
var globalSeq int64 = 1000

// Client is an in-memory fake implementing fs.MutationClient. Construct with New.
type Client struct {
	teamKey string    // identifier prefix for created issues (default "TST")
	now     time.Time // fixed clock for deterministic timestamps
	store   *db.Store // optional: reverse-resolve IDs -> names for faithful read-back

	// edits records the post-Update free-text state of entities so the verify
	// getters return read-your-writes-faithful values offline. Seeded from the
	// store on first edit; getters fall back to the store when nothing is recorded.
	mu        sync.Mutex
	issueEdit map[string]api.Issue
	projEdit  map[string]api.Project
	initEdit  map[string]api.Initiative
	// docState records each mock-created document (with its parent association)
	// so an edit preserves the linkage the real documentUpdate response carries
	// via DocumentFields (issue/project/team/initiative); without it the upsert
	// would clear issue_id and drop the doc from its parent listing.
	docState map[string]api.Document
}

// Option configures a Client.
type Option func(*Client)

// WithTeamKey sets the identifier prefix used for created issues (e.g. "TST").
func WithTeamKey(key string) Option {
	return func(c *Client) { c.teamKey = key }
}

// WithStore lets the fake reverse-resolve resolved IDs (stateId, labelIds,
// projectId) back to names so a created issue reads back with real status/labels/
// project — matching what the live API returns. Without it those render blank.
func WithStore(store *db.Store) Option {
	return func(c *Client) { c.store = store }
}

// New returns a fake mutation client. Created issues get identifiers like
// "TST-1001" (prefix configurable via WithTeamKey), starting above the usual
// fixture range so they never collide with seeded fixtures.
func New(opts ...Option) *Client {
	c := &Client{
		teamKey:   "TST",
		now:       time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		issueEdit: make(map[string]api.Issue),
		projEdit:  make(map[string]api.Project),
		initEdit:  make(map[string]api.Initiative),
		docState:  make(map[string]api.Document),
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// next returns a fresh process-unique integer for id generation.
func (c *Client) next() int {
	return int(atomic.AddInt64(&globalSeq, 1))
}

func str(m map[string]any, k string) string {
	if v, ok := m[k]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

func intVal(m map[string]any, k string) int {
	v, ok := m[k]
	if !ok {
		return 0
	}
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	}
	return 0
}

// ---- Issues ----

func (c *Client) CreateIssue(ctx context.Context, input map[string]any) (*api.Issue, error) {
	n := c.next()
	id := fmt.Sprintf("mock-issue-%d", n)
	identifier := fmt.Sprintf("%s-%d", c.teamKey, n)

	issue := api.Issue{
		ID:          id,
		Identifier:  identifier,
		Title:       str(input, "title"),
		Description: str(input, "description"),
		Priority:    intVal(input, "priority"),
		URL:         "https://linear.app/test/issue/" + strings.ToLower(identifier),
		BranchName:  "mock/" + strings.ToLower(identifier),
		CreatedAt:   c.now,
		UpdatedAt:   c.now,
		Team:        &api.Team{ID: str(input, "teamId"), Key: c.teamKey},
	}
	if sid := str(input, "stateId"); sid != "" {
		issue.State = api.State{ID: sid, Name: c.stateName(ctx, sid)}
	}
	if aid := str(input, "assigneeId"); aid != "" {
		issue.Assignee = &api.User{ID: aid}
	}
	if pid := str(input, "projectId"); pid != "" {
		issue.Project = &api.Project{ID: pid, Name: c.projectName(ctx, pid)}
	}
	if v, ok := input["labelIds"]; ok {
		if ids, ok := v.([]string); ok {
			nodes := make([]api.Label, len(ids))
			for i, lid := range ids {
				nodes[i] = api.Label{ID: lid, Name: c.labelName(ctx, lid)}
			}
			issue.Labels = api.Labels{Nodes: nodes}
		}
	}
	if pid := str(input, "parentId"); pid != "" {
		issue.Parent = &api.ParentIssue{ID: pid}
	}
	if due := str(input, "dueDate"); due != "" {
		issue.DueDate = &due
	}
	if v, ok := input["estimate"]; ok {
		var est float64
		switch e := v.(type) {
		case int:
			est = float64(e)
		case int64:
			est = float64(e)
		case float64:
			est = e
		}
		if est != 0 {
			issue.Estimate = &est
		}
	}
	if cid := str(input, "cycleId"); cid != "" {
		issue.Cycle = &api.IssueCycle{ID: cid}
	}
	if mid := str(input, "projectMilestoneId"); mid != "" {
		issue.ProjectMilestone = &api.ProjectMilestone{ID: mid}
	}
	return &issue, nil
}

// stateName/labelName/projectName reverse-resolve an ID to its name via the
// injected store (empty if no store or not found), so a created issue reads back
// with real status/labels/project like the live API returns.
func (c *Client) stateName(ctx context.Context, id string) string {
	if c.store == nil {
		return ""
	}
	if s, err := c.store.Queries().GetState(ctx, id); err == nil {
		return s.Name
	}
	return ""
}

func (c *Client) labelName(ctx context.Context, id string) string {
	if c.store == nil {
		return ""
	}
	if l, err := c.store.Queries().GetLabel(ctx, id); err == nil {
		return l.Name
	}
	return ""
}

func (c *Client) projectName(ctx context.Context, id string) string {
	if c.store == nil {
		return ""
	}
	if p, err := c.store.Queries().GetProject(ctx, id); err == nil {
		return p.Name
	}
	return ""
}

func (c *Client) UpdateIssue(ctx context.Context, issueID string, input map[string]any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	iss := c.currentIssueLocked(ctx, issueID)
	if v, ok := input["title"].(string); ok {
		iss.Title = v
	}
	if v, ok := input["description"].(string); ok {
		iss.Description = v
	}
	// Overlay the editable scalar frontmatter fields the issue Flush can send, so
	// the verify getter reads them back — mirroring CreateIssue's field handling.
	// A field absent from input is left untouched (a partial edit must not zero it).
	if _, ok := input["priority"]; ok {
		iss.Priority = intVal(input, "priority")
	}
	if _, ok := input["estimate"]; ok {
		est := float64(intVal(input, "estimate"))
		iss.Estimate = &est
	}
	if due, ok := input["dueDate"].(string); ok {
		iss.DueDate = &due
	}
	if sid, ok := input["stateId"].(string); ok && sid != "" {
		iss.State = api.State{ID: sid, Name: c.stateName(ctx, sid)}
	}
	c.issueEdit[issueID] = iss
	return nil
}

func (c *Client) ArchiveIssue(ctx context.Context, issueID string) error { return nil }

// ---- Comments ----

func (c *Client) CreateComment(ctx context.Context, issueID string, body string) (*api.Comment, error) {
	n := c.next()
	return &api.Comment{ID: fmt.Sprintf("mock-comment-%d", n), Body: body, CreatedAt: c.now, UpdatedAt: c.now}, nil
}

func (c *Client) UpdateComment(ctx context.Context, commentID string, body string) (*api.Comment, error) {
	return &api.Comment{ID: commentID, Body: body, CreatedAt: c.now, UpdatedAt: c.now}, nil
}

func (c *Client) DeleteComment(ctx context.Context, commentID string) error { return nil }

// ---- Documents ----

func (c *Client) CreateDocument(ctx context.Context, input map[string]any) (*api.Document, error) {
	n := c.next()
	id := fmt.Sprintf("mock-doc-%d", n)
	d := api.Document{
		ID:        id,
		Title:     str(input, "title"),
		Content:   str(input, "content"),
		SlugID:    fmt.Sprintf("mock-doc-%d", n),
		URL:       "https://linear.app/test/document/" + id,
		CreatedAt: c.now,
		UpdatedAt: c.now,
	}
	// Echo the parent association the input carries, exactly as the real
	// documentCreate response does (DocumentFields projects issue/project/
	// team/initiative). Without it the upsert stores a blank issue_id and the
	// doc never appears in its parent's listing.
	if v := str(input, "issueId"); v != "" {
		d.Issue = &api.Issue{ID: v}
	}
	if v := str(input, "projectId"); v != "" {
		d.Project = &api.Project{ID: v}
	}
	if v := str(input, "teamId"); v != "" {
		d.Team = &api.Team{ID: v}
	}
	if v := str(input, "initiativeId"); v != "" {
		d.Initiative = &api.Initiative{ID: v}
	}
	c.mu.Lock()
	c.docState[id] = d
	c.mu.Unlock()
	return &d, nil
}

func (c *Client) UpdateDocument(ctx context.Context, documentID string, input map[string]any) (*api.Document, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	d := c.docState[documentID] // zero value if unknown (e.g. a fixture-seeded doc)
	d.ID = documentID
	if v, ok := input["title"].(string); ok {
		d.Title = v
	}
	if v, ok := input["content"].(string); ok {
		d.Content = v
	}
	d.UpdatedAt = c.now
	if d.CreatedAt.IsZero() {
		d.CreatedAt = c.now
	}
	c.docState[documentID] = d
	return &d, nil
}

func (c *Client) DeleteDocument(ctx context.Context, documentID string) error { return nil }

// ---- Labels ----

func (c *Client) CreateLabel(ctx context.Context, input map[string]any) (*api.Label, error) {
	n := c.next()
	return &api.Label{
		ID:          fmt.Sprintf("mock-label-%d", n),
		Name:        str(input, "name"),
		Color:       str(input, "color"),
		Description: str(input, "description"),
	}, nil
}

func (c *Client) UpdateLabel(ctx context.Context, id string, input map[string]any) (*api.Label, error) {
	// The real mutation returns the WHOLE updated label, so overlay the input onto
	// the current stored state — echoing only the edited fields would zero the
	// untouched ones (name/color/description), corrupting the upsert.
	l := api.Label{ID: id, Name: str(input, "name"), Color: str(input, "color"), Description: str(input, "description")}
	if c.store != nil {
		if row, err := c.store.Queries().GetLabel(ctx, id); err == nil {
			if _, ok := input["name"]; !ok {
				l.Name = row.Name
			}
			if _, ok := input["color"]; !ok {
				l.Color = row.Color.String
			}
			if _, ok := input["description"]; !ok {
				l.Description = row.Description.String
			}
		}
	}
	return &l, nil
}

func (c *Client) DeleteLabel(ctx context.Context, id string) error { return nil }

// ---- Projects ----

func (c *Client) CreateProject(ctx context.Context, input map[string]any) (*api.Project, error) {
	n := c.next()
	id := fmt.Sprintf("mock-project-%d", n)
	name := str(input, "name")
	return &api.Project{
		ID:        id,
		Name:      name,
		Slug:      fmt.Sprintf("mock-project-%d", n),
		URL:       "https://linear.app/test/project/" + id,
		State:     "planned",
		CreatedAt: c.now,
		UpdatedAt: c.now,
	}, nil
}

func (c *Client) UpdateProject(ctx context.Context, projectID string, input api.ProjectUpdateInput) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	proj := c.currentProjectLocked(ctx, projectID)
	if input.Name != nil {
		proj.Name = *input.Name
	}
	if input.Content != nil { // the editable body maps here (#5)
		proj.Content = *input.Content
	}
	if input.Description != nil {
		proj.Description = *input.Description
	}
	if input.LabelIds != nil { // full-set write, like the real mutation
		proj.LabelIds = append([]string(nil), (*input.LabelIds)...)
	}
	c.projEdit[projectID] = proj
	return nil
}

func (c *Client) ArchiveProject(ctx context.Context, projectID string) error { return nil }

// ---- Project milestones ----

func (c *Client) CreateProjectMilestone(ctx context.Context, projectID, name, description string) (*api.ProjectMilestone, error) {
	n := c.next()
	return &api.ProjectMilestone{ID: fmt.Sprintf("mock-milestone-%d", n), Name: name, Description: description}, nil
}

func (c *Client) UpdateProjectMilestone(ctx context.Context, milestoneID string, input api.ProjectMilestoneUpdateInput) (*api.ProjectMilestone, error) {
	// The real mutation returns the WHOLE updated milestone, so overlay the input
	// onto the current stored state — echoing only the edited fields would zero
	// the untouched ones (name/targetDate/sortOrder), corrupting the upsert.
	c.mu.Lock()
	defer c.mu.Unlock()
	m := c.currentMilestoneLocked(ctx, milestoneID)
	if input.Name != nil {
		m.Name = *input.Name
	}
	if input.Description != nil {
		m.Description = *input.Description
	}
	if input.TargetDate != nil {
		td := *input.TargetDate
		if td == "" {
			m.TargetDate = nil
		} else {
			m.TargetDate = &td
		}
	}
	if input.SortOrder != nil {
		m.SortOrder = *input.SortOrder
	}
	return &m, nil
}

// currentMilestoneLocked returns the current stored milestone, else a bare {ID}.
// The caller must hold c.mu. Milestones have no post-edit record (no verify
// getter); the store is the source of truth for the merge base.
func (c *Client) currentMilestoneLocked(ctx context.Context, id string) api.ProjectMilestone {
	if c.store != nil {
		if row, err := c.store.Queries().GetProjectMilestone(ctx, id); err == nil {
			return db.DBMilestoneToAPIProjectMilestone(row)
		}
	}
	return api.ProjectMilestone{ID: id}
}

func (c *Client) DeleteProjectMilestone(ctx context.Context, milestoneID string) error { return nil }

// ---- Status updates ----

func (c *Client) CreateProjectUpdate(ctx context.Context, projectID, body, health string) (*api.ProjectUpdate, error) {
	n := c.next()
	return &api.ProjectUpdate{ID: fmt.Sprintf("mock-projupdate-%d", n), Body: body, Health: health, CreatedAt: c.now, UpdatedAt: c.now}, nil
}

func (c *Client) CreateInitiativeUpdate(ctx context.Context, initiativeID, body, health string) (*api.InitiativeUpdate, error) {
	n := c.next()
	return &api.InitiativeUpdate{ID: fmt.Sprintf("mock-initupdate-%d", n), Body: body, Health: health, CreatedAt: c.now, UpdatedAt: c.now}, nil
}

// ---- Initiatives ----

func (c *Client) UpdateInitiative(ctx context.Context, initiativeID string, input api.InitiativeUpdateInput) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	init := c.currentInitiativeLocked(ctx, initiativeID)
	if input.Name != nil {
		init.Name = *input.Name
	}
	if input.Content != nil { // the editable body maps here (#5)
		init.Content = *input.Content
	}
	if input.Description != nil {
		init.Description = *input.Description
	}
	c.initEdit[initiativeID] = init
	return nil
}

func (c *Client) AddProjectToInitiative(ctx context.Context, projectID, initiativeID string) error {
	return nil
}

func (c *Client) RemoveProjectFromInitiative(ctx context.Context, projectID, initiativeID string) error {
	return nil
}

// ---- Relations ----

func (c *Client) CreateIssueRelation(ctx context.Context, issueID, relatedIssueID, relationType string) (*api.IssueRelation, error) {
	n := c.next()
	return &api.IssueRelation{
		ID:           fmt.Sprintf("mock-relation-%d", n),
		Type:         relationType,
		RelatedIssue: &api.ParentIssue{ID: relatedIssueID},
	}, nil
}

func (c *Client) DeleteIssueRelation(ctx context.Context, relationID string) error { return nil }

// ---- Attachments ----

func (c *Client) LinkURL(ctx context.Context, issueID, url, title string) (*api.Attachment, error) {
	n := c.next()
	return &api.Attachment{ID: fmt.Sprintf("mock-attachment-%d", n), Title: title, URL: url, CreatedAt: c.now, UpdatedAt: c.now}, nil
}

func (c *Client) DeleteAttachment(ctx context.Context, attachmentID string) error { return nil }

// ---- Entity external links (project/initiative "Links / Resources") ----

func (c *Client) CreateEntityExternalLink(ctx context.Context, input map[string]any) (*api.EntityExternalLink, error) {
	n := c.next()
	return &api.EntityExternalLink{
		ID:        fmt.Sprintf("mock-extlink-%d", n),
		Label:     str(input, "label"),
		URL:       str(input, "url"),
		CreatedAt: c.now,
		UpdatedAt: c.now,
	}, nil
}

func (c *Client) DeleteEntityExternalLink(ctx context.Context, id string) error { return nil }

// ---- Read-your-writes verify seam (fs.verifyReader) ----
//
// These serve the edit-commit tail's re-fetch: the recorded post-Update state if
// the entity was edited, else the current stored entity. Returning stored state
// keeps a no-op rewrite byte-stable; returning the edit keeps an actual edit's
// read-your-writes verification faithful — both offline.

func (c *Client) GetIssue(ctx context.Context, issueID string) (*api.Issue, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	iss := c.currentIssueLocked(ctx, issueID)
	return &iss, nil
}

func (c *Client) GetProject(ctx context.Context, projectID string) (*api.Project, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	proj := c.currentProjectLocked(ctx, projectID)
	return &proj, nil
}

func (c *Client) GetInitiative(ctx context.Context, initiativeID string) (*api.Initiative, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	init := c.currentInitiativeLocked(ctx, initiativeID)
	return &init, nil
}

// currentIssueLocked returns the recorded post-edit issue, else the stored issue,
// else a bare {ID}. The caller must hold c.mu.
func (c *Client) currentIssueLocked(ctx context.Context, id string) api.Issue {
	if e, ok := c.issueEdit[id]; ok {
		return e
	}
	if c.store != nil {
		if row, err := c.store.Queries().GetIssueByID(ctx, id); err == nil {
			if iss, err := db.DBIssueToAPIIssue(row); err == nil {
				return iss
			}
		}
	}
	return api.Issue{ID: id}
}

func (c *Client) currentProjectLocked(ctx context.Context, id string) api.Project {
	if e, ok := c.projEdit[id]; ok {
		return e
	}
	if c.store != nil {
		if row, err := c.store.Queries().GetProject(ctx, id); err == nil {
			if proj, err := db.DBProjectToAPIProject(row); err == nil {
				return proj
			}
		}
	}
	return api.Project{ID: id}
}

func (c *Client) currentInitiativeLocked(ctx context.Context, id string) api.Initiative {
	if e, ok := c.initEdit[id]; ok {
		return e
	}
	if c.store != nil {
		if row, err := c.store.Queries().GetInitiative(ctx, id); err == nil {
			if init, err := db.DBInitiativeToAPIInitiative(row); err == nil {
				return init
			}
		}
	}
	return api.Initiative{ID: id}
}
