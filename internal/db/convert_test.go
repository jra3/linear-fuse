package db

import (
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	"github.com/jra3/linear-fuse/internal/api"
)

func toNullTime(t *time.Time) sql.NullTime {
	if t == nil {
		return sql.NullTime{}
	}
	return sql.NullTime{Time: *t, Valid: true}
}

// mustMarshal marshals v to JSON or panics - for test setup only
func mustMarshal(v interface{}) []byte {
	data, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return data
}

func TestDBTeamToAPITeam(t *testing.T) {
	t.Parallel()
	now := time.Now()

	team := Team{
		ID:        "team-1",
		Key:       "TST",
		Name:      "Test Team",
		Icon:      toNullString(strPtr("icon")),
		CreatedAt: toNullTime(&now),
		UpdatedAt: toNullTime(&now),
	}

	apiTeam := DBTeamToAPITeam(team)

	if apiTeam.ID != team.ID {
		t.Errorf("ID mismatch: got %s, want %s", apiTeam.ID, team.ID)
	}
	if apiTeam.Key != team.Key {
		t.Errorf("Key mismatch: got %s, want %s", apiTeam.Key, team.Key)
	}
	if apiTeam.Name != team.Name {
		t.Errorf("Name mismatch: got %s, want %s", apiTeam.Name, team.Name)
	}
	if apiTeam.Icon != team.Icon.String {
		t.Errorf("Icon mismatch: got %s, want %s", apiTeam.Icon, team.Icon.String)
	}
}

func TestDBTeamsToAPITeams(t *testing.T) {
	t.Parallel()
	now := time.Now()

	teams := []Team{
		{ID: "team-1", Key: "TST", Name: "Test", CreatedAt: toNullTime(&now), UpdatedAt: toNullTime(&now)},
		{ID: "team-2", Key: "DEV", Name: "Dev", CreatedAt: toNullTime(&now), UpdatedAt: toNullTime(&now)},
	}

	apiTeams := DBTeamsToAPITeams(teams)

	if len(apiTeams) != 2 {
		t.Fatalf("Expected 2 teams, got %d", len(apiTeams))
	}
	if apiTeams[0].ID != "team-1" {
		t.Errorf("Expected first team ID 'team-1', got %s", apiTeams[0].ID)
	}
}

func TestNullStringValue(t *testing.T) {
	t.Parallel()

	// Invalid null string (nil-like)
	ns := sql.NullString{Valid: false}
	result := NullStringValue(ns)
	if result != "" {
		t.Errorf("Expected empty string for invalid null string, got %s", result)
	}

	// Valid null string
	ns = sql.NullString{String: "test", Valid: true}
	result = NullStringValue(ns)
	if result != "test" {
		t.Errorf("Expected 'test', got %s", result)
	}
}

func TestNullStringPtr(t *testing.T) {
	t.Parallel()

	// Invalid null string
	ns := sql.NullString{Valid: false}
	result := NullStringPtr(ns)
	if result != nil {
		t.Error("Expected nil for invalid null string")
	}

	// Valid null string
	ns = sql.NullString{String: "test", Valid: true}
	result = NullStringPtr(ns)
	if result == nil || *result != "test" {
		t.Error("Expected 'test' for valid null string")
	}
}

func TestAPIStateToDBState(t *testing.T) {
	t.Parallel()
	state := api.State{
		ID:   "state-1",
		Name: "In Progress",
		Type: "started",
	}

	params, err := APIStateToDBState(state, "team-1")
	if err != nil {
		t.Fatalf("APIStateToDBState failed: %v", err)
	}

	if params.ID != state.ID {
		t.Errorf("ID mismatch")
	}
	if params.Name != state.Name {
		t.Errorf("Name mismatch")
	}
	if params.Type != state.Type {
		t.Errorf("Type mismatch")
	}
	if params.TeamID != "team-1" {
		t.Errorf("TeamID mismatch")
	}
}

func TestDBStateToAPIState(t *testing.T) {
	t.Parallel()
	state := State{
		ID:       "state-1",
		Name:     "Done",
		Type:     "completed",
		Color:    toNullString(strPtr("#00ff00")),
		Position: sql.NullFloat64{Float64: 1.0, Valid: true},
	}

	apiState := DBStateToAPIState(state)

	if apiState.ID != state.ID {
		t.Errorf("ID mismatch")
	}
	if apiState.Name != state.Name {
		t.Errorf("Name mismatch")
	}
	if apiState.Type != state.Type {
		t.Errorf("Type mismatch")
	}
}

func TestDBStatesToAPIStates(t *testing.T) {
	t.Parallel()
	states := []State{
		{ID: "s1", Name: "Todo", Type: "unstarted"},
		{ID: "s2", Name: "Done", Type: "completed"},
	}

	apiStates := DBStatesToAPIStates(states)

	if len(apiStates) != 2 {
		t.Fatalf("Expected 2 states, got %d", len(apiStates))
	}
}

func TestAPILabelToDBLabel(t *testing.T) {
	t.Parallel()
	label := api.Label{
		ID:          "label-1",
		Name:        "Bug",
		Description: "Bug reports",
		Color:       "#ff0000",
	}

	params, err := APILabelToDBLabel(label, "team-1")
	if err != nil {
		t.Fatalf("APILabelToDBLabel failed: %v", err)
	}

	if params.ID != label.ID {
		t.Errorf("ID mismatch")
	}
	if params.Name != label.Name {
		t.Errorf("Name mismatch")
	}
	if params.Color.String != label.Color {
		t.Errorf("Color mismatch")
	}
}

func TestDBLabelToAPILabel(t *testing.T) {
	t.Parallel()
	label := Label{
		ID:          "label-1",
		Name:        "Feature",
		Description: toNullString(strPtr("New features")),
		Color:       toNullString(strPtr("#00ff00")),
	}

	apiLabel := DBLabelToAPILabel(label)

	if apiLabel.ID != label.ID {
		t.Errorf("ID mismatch")
	}
	if apiLabel.Name != label.Name {
		t.Errorf("Name mismatch")
	}
}

func TestDBLabelsToAPILabels(t *testing.T) {
	t.Parallel()
	labels := []Label{
		{ID: "l1", Name: "Bug"},
		{ID: "l2", Name: "Feature"},
	}

	apiLabels := DBLabelsToAPILabels(labels)

	if len(apiLabels) != 2 {
		t.Fatalf("Expected 2 labels, got %d", len(apiLabels))
	}
}

func TestAPIUserToDBUser(t *testing.T) {
	t.Parallel()
	user := api.User{
		ID:     "user-1",
		Name:   "Test User",
		Email:  "test@example.com",
		Active: true,
	}

	params, err := APIUserToDBUser(user)
	if err != nil {
		t.Fatalf("APIUserToDBUser failed: %v", err)
	}

	if params.ID != user.ID {
		t.Errorf("ID mismatch")
	}
	if params.Name != user.Name {
		t.Errorf("Name mismatch")
	}
	if params.Email != user.Email {
		t.Errorf("Email mismatch")
	}
	// Active is stored as int64 in DB (1=true, 0=false)
	if (params.Active == 1) != user.Active {
		t.Errorf("Active mismatch")
	}
}

func TestDBUserToAPIUser(t *testing.T) {
	t.Parallel()
	user := User{
		ID:        "user-1",
		Name:      "Test User",
		Email:     "test@example.com",
		Active:    1,
		Admin:     0,
		AvatarUrl: toNullString(strPtr("https://example.com/avatar.png")),
	}

	apiUser := DBUserToAPIUser(user)

	if apiUser.ID != user.ID {
		t.Errorf("ID mismatch")
	}
	if apiUser.Name != user.Name {
		t.Errorf("Name mismatch")
	}
	if apiUser.Email != user.Email {
		t.Errorf("Email mismatch")
	}
	if apiUser.Active != true {
		t.Errorf("Active mismatch: got %v, want true", apiUser.Active)
	}
}

func TestDBUsersToAPIUsers(t *testing.T) {
	t.Parallel()
	users := []User{
		{ID: "u1", Name: "Alice", Email: "alice@example.com", Active: 1},
		{ID: "u2", Name: "Bob", Email: "bob@example.com", Active: 1},
	}

	apiUsers := DBUsersToAPIUsers(users)

	if len(apiUsers) != 2 {
		t.Fatalf("Expected 2 users, got %d", len(apiUsers))
	}
}

func TestAPICycleToDBCycle(t *testing.T) {
	t.Parallel()
	now := time.Now()
	cycle := api.Cycle{
		ID:       "cycle-1",
		Number:   42,
		Name:     "Sprint 42",
		StartsAt: now,
		EndsAt:   now.Add(14 * 24 * time.Hour),
	}

	params, err := APICycleToDBCycle(cycle, "team-1")
	if err != nil {
		t.Fatalf("APICycleToDBCycle failed: %v", err)
	}

	if params.ID != cycle.ID {
		t.Errorf("ID mismatch")
	}
	if params.Number != int64(cycle.Number) {
		t.Errorf("Number mismatch")
	}
	if params.Name.String != cycle.Name {
		t.Errorf("Name mismatch")
	}
}

func TestDBCycleToAPICycle(t *testing.T) {
	t.Parallel()
	now := time.Now()
	cycle := Cycle{
		ID:          "cycle-1",
		Number:      42,
		Name:        toNullString(strPtr("Sprint 42")),
		Description: toNullString(strPtr("The answer")),
		StartsAt:    toNullTime(&now),
		EndsAt:      toNullTime(&now),
		Progress:    sql.NullFloat64{Float64: 0.5, Valid: true},
	}

	apiCycle := DBCycleToAPICycle(cycle)

	if apiCycle.ID != cycle.ID {
		t.Errorf("ID mismatch")
	}
	if apiCycle.Number != int(cycle.Number) {
		t.Errorf("Number mismatch")
	}
	if apiCycle.Name != cycle.Name.String {
		t.Errorf("Name mismatch")
	}
}

func TestDBCyclesToAPICycles(t *testing.T) {
	t.Parallel()
	cycles := []Cycle{
		{ID: "c1", Number: 1, Name: toNullString(strPtr("Sprint 1"))},
		{ID: "c2", Number: 2, Name: toNullString(strPtr("Sprint 2"))},
	}

	apiCycles := DBCyclesToAPICycles(cycles)

	if len(apiCycles) != 2 {
		t.Fatalf("Expected 2 cycles, got %d", len(apiCycles))
	}
}

func TestAPIProjectToDBProject(t *testing.T) {
	t.Parallel()
	now := time.Now()
	project := api.Project{
		ID:          "project-1",
		Name:        "Project Alpha",
		Slug:        "alpha",
		Description: "Alpha project",
		State:       "started",
		StartDate:   strPtr("2024-01-01"),
		TargetDate:  strPtr("2024-06-30"),
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	params, err := APIProjectToDBProject(project)
	if err != nil {
		t.Fatalf("APIProjectToDBProject failed: %v", err)
	}

	if params.ID != project.ID {
		t.Errorf("ID mismatch")
	}
	if params.Name != project.Name {
		t.Errorf("Name mismatch")
	}
	if params.SlugID != project.Slug {
		t.Errorf("SlugID mismatch")
	}
}

func TestDBProjectToAPIProject(t *testing.T) {
	t.Parallel()
	now := time.Now()
	apiData := api.Project{
		ID:    "project-1",
		Name:  "Project Beta",
		Slug:  "beta",
		State: "planned",
	}
	project := Project{
		ID:          "project-1",
		Name:        "Project Beta",
		SlugID:      "beta",
		Description: toNullString(strPtr("Beta project")),
		State:       toNullString(strPtr("planned")),
		Progress:    sql.NullFloat64{Float64: 0.25, Valid: true},
		StartDate:   toNullString(strPtr("2024-01-01")),
		TargetDate:  toNullString(strPtr("2024-12-31")),
		CreatedAt:   toNullTime(&now),
		UpdatedAt:   toNullTime(&now),
		Data:        mustMarshal(apiData),
	}

	apiProject, err := DBProjectToAPIProject(project)
	if err != nil {
		t.Fatalf("DBProjectToAPIProject failed: %v", err)
	}

	if apiProject.ID != project.ID {
		t.Errorf("ID mismatch")
	}
	if apiProject.Name != project.Name {
		t.Errorf("Name mismatch")
	}
	if apiProject.Slug != project.SlugID {
		t.Errorf("Slug mismatch")
	}
}

func TestDBProjectsToAPIProjects(t *testing.T) {
	t.Parallel()
	projects := []Project{
		{ID: "p1", Name: "Project 1", SlugID: "p1", State: toNullString(strPtr("started")), Data: mustMarshal(api.Project{ID: "p1", Name: "Project 1", Slug: "p1"})},
		{ID: "p2", Name: "Project 2", SlugID: "p2", State: toNullString(strPtr("planned")), Data: mustMarshal(api.Project{ID: "p2", Name: "Project 2", Slug: "p2"})},
	}

	apiProjects, err := DBProjectsToAPIProjects(projects)
	if err != nil {
		t.Fatalf("DBProjectsToAPIProjects failed: %v", err)
	}

	if len(apiProjects) != 2 {
		t.Fatalf("Expected 2 projects, got %d", len(apiProjects))
	}
}

func TestAPICommentToDBComment(t *testing.T) {
	t.Parallel()
	now := time.Now()
	comment := api.Comment{
		ID:        "comment-1",
		Body:      "This is a comment",
		CreatedAt: now,
		UpdatedAt: now,
		User: &api.User{
			ID:    "user-1",
			Name:  "Commenter",
			Email: "commenter@example.com",
		},
	}

	params, err := APICommentToDBComment(comment, "issue-1")
	if err != nil {
		t.Fatalf("APICommentToDBComment failed: %v", err)
	}

	if params.ID != comment.ID {
		t.Errorf("ID mismatch")
	}
	if params.Body != comment.Body {
		t.Errorf("Body mismatch")
	}
	if params.IssueID != "issue-1" {
		t.Errorf("IssueID mismatch")
	}
}

func TestDBCommentToAPIComment(t *testing.T) {
	t.Parallel()
	now := time.Now()
	apiData := api.Comment{
		ID:        "comment-1",
		Body:      "Test comment",
		CreatedAt: now,
		UpdatedAt: now,
	}
	comment := Comment{
		ID:        "comment-1",
		Body:      "Test comment",
		IssueID:   "issue-1",
		UserID:    toNullString(strPtr("user-1")),
		UserName:  toNullString(strPtr("Commenter")),
		UserEmail: toNullString(strPtr("commenter@example.com")),
		CreatedAt: now,
		UpdatedAt: now,
		Data:      mustMarshal(apiData),
	}

	apiComment, err := DBCommentToAPIComment(comment)
	if err != nil {
		t.Fatalf("DBCommentToAPIComment failed: %v", err)
	}

	if apiComment.ID != comment.ID {
		t.Errorf("ID mismatch")
	}
	if apiComment.Body != comment.Body {
		t.Errorf("Body mismatch")
	}
}

func TestDBCommentsToAPIComments(t *testing.T) {
	t.Parallel()
	now := time.Now()
	comments := []Comment{
		{ID: "c1", Body: "Comment 1", IssueID: "issue-1", CreatedAt: now, UpdatedAt: now, Data: mustMarshal(api.Comment{ID: "c1", Body: "Comment 1"})},
		{ID: "c2", Body: "Comment 2", IssueID: "issue-1", CreatedAt: now, UpdatedAt: now, Data: mustMarshal(api.Comment{ID: "c2", Body: "Comment 2"})},
	}

	apiComments, err := DBCommentsToAPIComments(comments)
	if err != nil {
		t.Fatalf("DBCommentsToAPIComments failed: %v", err)
	}

	if len(apiComments) != 2 {
		t.Fatalf("Expected 2 comments, got %d", len(apiComments))
	}
}

func TestAPIDocumentToDBDocument(t *testing.T) {
	t.Parallel()
	now := time.Now()
	doc := api.Document{
		ID:        "doc-1",
		Title:     "Test Document",
		Content:   "Document content",
		SlugID:    "test-doc",
		CreatedAt: now,
		UpdatedAt: now,
		Creator: &api.User{
			ID:    "user-1",
			Name:  "Author",
			Email: "author@example.com",
		},
		Issue: &api.Issue{ID: "issue-1"},
	}

	params, err := APIDocumentToDBDocument(doc)
	if err != nil {
		t.Fatalf("APIDocumentToDBDocument failed: %v", err)
	}

	if params.ID != doc.ID {
		t.Errorf("ID mismatch")
	}
	if params.Title != doc.Title {
		t.Errorf("Title mismatch")
	}
	if params.SlugID != doc.SlugID {
		t.Errorf("SlugID mismatch")
	}
}

func TestDBDocumentToAPIDocument(t *testing.T) {
	t.Parallel()
	now := time.Now()
	apiData := api.Document{
		ID:      "doc-1",
		Title:   "Test Doc",
		Content: "Content",
		SlugID:  "test-doc",
	}
	doc := Document{
		ID:        "doc-1",
		Title:     "Test Doc",
		Content:   toNullString(strPtr("Content")),
		SlugID:    "test-doc",
		CreatorID: toNullString(strPtr("user-1")),
		IssueID:   toNullString(strPtr("issue-1")),
		CreatedAt: toNullTime(&now),
		UpdatedAt: toNullTime(&now),
		Data:      mustMarshal(apiData),
	}

	apiDoc, err := DBDocumentToAPIDocument(doc)
	if err != nil {
		t.Fatalf("DBDocumentToAPIDocument failed: %v", err)
	}

	if apiDoc.ID != doc.ID {
		t.Errorf("ID mismatch")
	}
	if apiDoc.Title != doc.Title {
		t.Errorf("Title mismatch")
	}
}

func TestDBDocumentsToAPIDocuments(t *testing.T) {
	t.Parallel()
	now := time.Now()
	docs := []Document{
		{ID: "d1", Title: "Doc 1", Content: toNullString(strPtr("Content 1")), SlugID: "d1", CreatedAt: toNullTime(&now), UpdatedAt: toNullTime(&now), Data: mustMarshal(api.Document{ID: "d1", Title: "Doc 1"})},
		{ID: "d2", Title: "Doc 2", Content: toNullString(strPtr("Content 2")), SlugID: "d2", CreatedAt: toNullTime(&now), UpdatedAt: toNullTime(&now), Data: mustMarshal(api.Document{ID: "d2", Title: "Doc 2"})},
	}

	apiDocs, err := DBDocumentsToAPIDocuments(docs)
	if err != nil {
		t.Fatalf("DBDocumentsToAPIDocuments failed: %v", err)
	}

	if len(apiDocs) != 2 {
		t.Fatalf("Expected 2 documents, got %d", len(apiDocs))
	}
}

func TestAPIInitiativeToDBInitiative(t *testing.T) {
	t.Parallel()
	now := time.Now()
	init := api.Initiative{
		ID:          "init-1",
		Name:        "Test Initiative",
		Slug:        "test-init",
		Description: "A test initiative",
		Status:      "active",
		Color:       "#0000ff",
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	params, err := APIInitiativeToDBInitiative(init)
	if err != nil {
		t.Fatalf("APIInitiativeToDBInitiative failed: %v", err)
	}

	if params.ID != init.ID {
		t.Errorf("ID mismatch")
	}
	if params.Name != init.Name {
		t.Errorf("Name mismatch")
	}
	if params.SlugID != init.Slug {
		t.Errorf("SlugID mismatch")
	}
}

func TestDBInitiativeToAPIInitiative(t *testing.T) {
	t.Parallel()
	now := time.Now()
	apiData := api.Initiative{
		ID:   "init-1",
		Name: "Test Init",
		Slug: "test-init",
	}
	init := Initiative{
		ID:          "init-1",
		Name:        "Test Init",
		SlugID:      "test-init",
		Description: toNullString(strPtr("A test")),
		Status:      toNullString(strPtr("active")),
		Color:       toNullString(strPtr("#0000ff")),
		CreatedAt:   toNullTime(&now),
		UpdatedAt:   toNullTime(&now),
		Data:        mustMarshal(apiData),
	}

	apiInit, err := DBInitiativeToAPIInitiative(init)
	if err != nil {
		t.Fatalf("DBInitiativeToAPIInitiative failed: %v", err)
	}

	if apiInit.ID != init.ID {
		t.Errorf("ID mismatch")
	}
	if apiInit.Name != init.Name {
		t.Errorf("Name mismatch")
	}
}

func TestDBInitiativesToAPIInitiatives(t *testing.T) {
	t.Parallel()
	inits := []Initiative{
		{ID: "i1", Name: "Init 1", SlugID: "i1", Data: mustMarshal(api.Initiative{ID: "i1", Name: "Init 1"})},
		{ID: "i2", Name: "Init 2", SlugID: "i2", Data: mustMarshal(api.Initiative{ID: "i2", Name: "Init 2"})},
	}

	apiInits, err := DBInitiativesToAPIInitiatives(inits)
	if err != nil {
		t.Fatalf("DBInitiativesToAPIInitiatives failed: %v", err)
	}

	if len(apiInits) != 2 {
		t.Fatalf("Expected 2 initiatives, got %d", len(apiInits))
	}
}

func TestAPIProjectMilestoneToDBMilestone(t *testing.T) {
	t.Parallel()
	targetDate := "2024-06-30"
	milestone := api.ProjectMilestone{
		ID:          "ms-1",
		Name:        "Alpha Release",
		Description: "First alpha",
		TargetDate:  &targetDate,
		SortOrder:   1.5,
	}

	params, err := APIProjectMilestoneToDBMilestone(milestone, "project-1")
	if err != nil {
		t.Fatalf("APIProjectMilestoneToDBMilestone failed: %v", err)
	}

	if params.ID != milestone.ID {
		t.Errorf("ID mismatch")
	}
	if params.Name != milestone.Name {
		t.Errorf("Name mismatch")
	}
	if params.ProjectID != "project-1" {
		t.Errorf("ProjectID mismatch")
	}
}

func TestDBMilestoneToAPIProjectMilestone(t *testing.T) {
	t.Parallel()
	milestone := ProjectMilestone{
		ID:          "ms-1",
		Name:        "Beta Release",
		Description: toNullString(strPtr("Beta version")),
		TargetDate:  toNullString(strPtr("2024-09-30")),
		SortOrder:   sql.NullFloat64{Float64: 2.0, Valid: true},
	}

	apiMilestone := DBMilestoneToAPIProjectMilestone(milestone)

	if apiMilestone.ID != milestone.ID {
		t.Errorf("ID mismatch")
	}
	if apiMilestone.Name != milestone.Name {
		t.Errorf("Name mismatch")
	}
}

func TestDBMilestonesToAPIProjectMilestones(t *testing.T) {
	t.Parallel()
	milestones := []ProjectMilestone{
		{ID: "ms1", Name: "Alpha", SortOrder: sql.NullFloat64{Float64: 1.0, Valid: true}},
		{ID: "ms2", Name: "Beta", SortOrder: sql.NullFloat64{Float64: 2.0, Valid: true}},
	}

	apiMilestones := DBMilestonesToAPIProjectMilestones(milestones)

	if len(apiMilestones) != 2 {
		t.Fatalf("Expected 2 milestones, got %d", len(apiMilestones))
	}
}

func TestAPIProjectUpdateToDBUpdate(t *testing.T) {
	t.Parallel()
	now := time.Now()
	update := api.ProjectUpdate{
		ID:        "update-1",
		Body:      "Sprint completed",
		Health:    "onTrack",
		CreatedAt: now,
		UpdatedAt: now,
		User: &api.User{
			ID:    "user-1",
			Name:  "Author",
			Email: "author@example.com",
		},
	}

	params, err := APIProjectUpdateToDBUpdate(update, "project-1")
	if err != nil {
		t.Fatalf("APIProjectUpdateToDBUpdate failed: %v", err)
	}

	if params.ID != update.ID {
		t.Errorf("ID mismatch")
	}
	if params.Body != update.Body {
		t.Errorf("Body mismatch")
	}
	if params.Health.String != update.Health {
		t.Errorf("Health mismatch")
	}
}

func TestDBProjectUpdateToAPIUpdate(t *testing.T) {
	t.Parallel()
	now := time.Now()
	apiData := api.ProjectUpdate{
		ID:     "update-1",
		Body:   "Sprint completed",
		Health: "onTrack",
	}
	update := ProjectUpdate{
		ID:        "update-1",
		Body:      "Sprint completed",
		Health:    toNullString(strPtr("onTrack")),
		ProjectID: "project-1",
		UserID:    toNullString(strPtr("user-1")),
		UserName:  toNullString(strPtr("Author")),
		CreatedAt: now,
		UpdatedAt: now,
		Data:      mustMarshal(apiData),
	}

	apiUpdate, err := DBProjectUpdateToAPIUpdate(update)
	if err != nil {
		t.Fatalf("DBProjectUpdateToAPIUpdate failed: %v", err)
	}

	if apiUpdate.ID != update.ID {
		t.Errorf("ID mismatch")
	}
	if apiUpdate.Body != update.Body {
		t.Errorf("Body mismatch")
	}
	if apiUpdate.Health != update.Health.String {
		t.Errorf("Health mismatch")
	}
}

func TestDBProjectUpdatesToAPIUpdates(t *testing.T) {
	t.Parallel()
	now := time.Now()
	updates := []ProjectUpdate{
		{ID: "u1", Body: "Update 1", ProjectID: "p1", CreatedAt: now, UpdatedAt: now, Data: mustMarshal(api.ProjectUpdate{ID: "u1", Body: "Update 1"})},
		{ID: "u2", Body: "Update 2", ProjectID: "p1", CreatedAt: now, UpdatedAt: now, Data: mustMarshal(api.ProjectUpdate{ID: "u2", Body: "Update 2"})},
	}

	apiUpdates, err := DBProjectUpdatesToAPIUpdates(updates)
	if err != nil {
		t.Fatalf("DBProjectUpdatesToAPIUpdates failed: %v", err)
	}

	if len(apiUpdates) != 2 {
		t.Fatalf("Expected 2 updates, got %d", len(apiUpdates))
	}
}

func TestAPIInitiativeUpdateToDBUpdate(t *testing.T) {
	t.Parallel()
	now := time.Now()
	update := api.InitiativeUpdate{
		ID:        "update-1",
		Body:      "Initiative on track",
		Health:    "onTrack",
		CreatedAt: now,
		UpdatedAt: now,
		User: &api.User{
			ID:    "user-1",
			Name:  "Author",
			Email: "author@example.com",
		},
	}

	params, err := APIInitiativeUpdateToDBUpdate(update, "init-1")
	if err != nil {
		t.Fatalf("APIInitiativeUpdateToDBUpdate failed: %v", err)
	}

	if params.ID != update.ID {
		t.Errorf("ID mismatch")
	}
	if params.Body != update.Body {
		t.Errorf("Body mismatch")
	}
}

func TestDBInitiativeUpdateToAPIUpdate(t *testing.T) {
	t.Parallel()
	now := time.Now()
	apiData := api.InitiativeUpdate{
		ID:     "update-1",
		Body:   "On track",
		Health: "onTrack",
	}
	update := InitiativeUpdate{
		ID:           "update-1",
		Body:         "On track",
		Health:       toNullString(strPtr("onTrack")),
		InitiativeID: "init-1",
		UserID:       toNullString(strPtr("user-1")),
		UserName:     toNullString(strPtr("Author")),
		CreatedAt:    now,
		UpdatedAt:    now,
		Data:         mustMarshal(apiData),
	}

	apiUpdate, err := DBInitiativeUpdateToAPIUpdate(update)
	if err != nil {
		t.Fatalf("DBInitiativeUpdateToAPIUpdate failed: %v", err)
	}

	if apiUpdate.ID != update.ID {
		t.Errorf("ID mismatch")
	}
	if apiUpdate.Body != update.Body {
		t.Errorf("Body mismatch")
	}
}

func TestDBInitiativeUpdatesToAPIUpdates(t *testing.T) {
	t.Parallel()
	now := time.Now()
	updates := []InitiativeUpdate{
		{ID: "u1", Body: "Update 1", InitiativeID: "i1", CreatedAt: now, UpdatedAt: now, Data: mustMarshal(api.InitiativeUpdate{ID: "u1", Body: "Update 1"})},
		{ID: "u2", Body: "Update 2", InitiativeID: "i1", CreatedAt: now, UpdatedAt: now, Data: mustMarshal(api.InitiativeUpdate{ID: "u2", Body: "Update 2"})},
	}

	apiUpdates, err := DBInitiativeUpdatesToAPIUpdates(updates)
	if err != nil {
		t.Fatalf("DBInitiativeUpdatesToAPIUpdates failed: %v", err)
	}

	if len(apiUpdates) != 2 {
		t.Fatalf("Expected 2 updates, got %d", len(apiUpdates))
	}
}

// =============================================================================
// Attachment Conversion Tests
// =============================================================================

func TestAPIAttachmentToDBAttachment(t *testing.T) {
	t.Parallel()
	now := time.Now()
	attachment := api.Attachment{
		ID:         "attach-1",
		Title:      "GitHub PR",
		Subtitle:   "feat: Add new feature",
		URL:        "https://github.com/org/repo/pull/123",
		SourceType: "github-pr",
		CreatedAt:  now,
		UpdatedAt:  now,
		Creator: &api.User{
			ID:    "user-1",
			Name:  "Developer",
			Email: "dev@example.com",
		},
	}

	params, err := APIAttachmentToDBAttachment(attachment, "issue-1")
	if err != nil {
		t.Fatalf("APIAttachmentToDBAttachment failed: %v", err)
	}

	if params.ID != attachment.ID {
		t.Errorf("ID mismatch: got %s, want %s", params.ID, attachment.ID)
	}
	if params.IssueID != "issue-1" {
		t.Errorf("IssueID mismatch: got %s, want issue-1", params.IssueID)
	}
	if params.Title != attachment.Title {
		t.Errorf("Title mismatch: got %s, want %s", params.Title, attachment.Title)
	}
	if params.Url != attachment.URL {
		t.Errorf("URL mismatch: got %s, want %s", params.Url, attachment.URL)
	}
	if params.SourceType.String != attachment.SourceType {
		t.Errorf("SourceType mismatch: got %s, want %s", params.SourceType.String, attachment.SourceType)
	}
}

func TestDBAttachmentToAPIAttachment(t *testing.T) {
	t.Parallel()
	now := time.Now()
	apiData := api.Attachment{
		ID:         "attach-1",
		Title:      "Slack Thread",
		Subtitle:   "Discussion",
		URL:        "https://slack.com/archives/C123/p456",
		SourceType: "slack",
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	attachment := Attachment{
		ID:         "attach-1",
		IssueID:    "issue-1",
		Title:      "Slack Thread",
		Url:        "https://slack.com/archives/C123/p456",
		SourceType: toNullString(strPtr("slack")),
		SyncedAt:   now,
		Data:       mustMarshal(apiData),
	}

	apiAttachment, err := DBAttachmentToAPIAttachment(attachment)
	if err != nil {
		t.Fatalf("DBAttachmentToAPIAttachment failed: %v", err)
	}

	if apiAttachment.ID != attachment.ID {
		t.Errorf("ID mismatch: got %s, want %s", apiAttachment.ID, attachment.ID)
	}
	if apiAttachment.Title != "Slack Thread" {
		t.Errorf("Title mismatch: got %s, want Slack Thread", apiAttachment.Title)
	}
	if apiAttachment.SourceType != "slack" {
		t.Errorf("SourceType mismatch: got %s, want slack", apiAttachment.SourceType)
	}
}

func TestDBAttachmentsToAPIAttachments(t *testing.T) {
	t.Parallel()
	now := time.Now()
	attachments := []Attachment{
		{
			ID:         "a1",
			IssueID:    "issue-1",
			Title:      "PR 1",
			Url:        "https://github.com/pr/1",
			SourceType: toNullString(strPtr("github-pr")),
			SyncedAt:   now,
			Data:       mustMarshal(api.Attachment{ID: "a1", Title: "PR 1", SourceType: "github-pr"}),
		},
		{
			ID:         "a2",
			IssueID:    "issue-1",
			Title:      "Slack",
			Url:        "https://slack.com/thread",
			SourceType: toNullString(strPtr("slack")),
			SyncedAt:   now,
			Data:       mustMarshal(api.Attachment{ID: "a2", Title: "Slack", SourceType: "slack"}),
		},
	}

	apiAttachments, err := DBAttachmentsToAPIAttachments(attachments)
	if err != nil {
		t.Fatalf("DBAttachmentsToAPIAttachments failed: %v", err)
	}

	if len(apiAttachments) != 2 {
		t.Fatalf("Expected 2 attachments, got %d", len(apiAttachments))
	}
	if apiAttachments[0].ID != "a1" {
		t.Errorf("First attachment ID mismatch")
	}
	if apiAttachments[1].ID != "a2" {
		t.Errorf("Second attachment ID mismatch")
	}
}

// =============================================================================
// Embedded File Conversion Tests
// =============================================================================

func TestAPIEmbeddedFileToDBFile(t *testing.T) {
	t.Parallel()
	file := api.EmbeddedFile{
		ID:        "file-1",
		IssueID:   "issue-1",
		URL:       "https://uploads.linear.app/workspace/file1/screenshot.png",
		Filename:  "screenshot.png",
		MimeType:  "image/png",
		FileSize:  12345,
		CachePath: "/tmp/cache/file-1",
		Source:    "description",
	}

	params := APIEmbeddedFileToDBFile(file)

	if params.ID != file.ID {
		t.Errorf("ID mismatch: got %s, want %s", params.ID, file.ID)
	}
	if params.IssueID != file.IssueID {
		t.Errorf("IssueID mismatch: got %s, want %s", params.IssueID, file.IssueID)
	}
	if params.Url != file.URL {
		t.Errorf("URL mismatch: got %s, want %s", params.Url, file.URL)
	}
	if params.Filename != file.Filename {
		t.Errorf("Filename mismatch: got %s, want %s", params.Filename, file.Filename)
	}
	if params.MimeType.String != file.MimeType {
		t.Errorf("MimeType mismatch: got %s, want %s", params.MimeType.String, file.MimeType)
	}
	if params.Source != file.Source {
		t.Errorf("Source mismatch: got %s, want %s", params.Source, file.Source)
	}
}

func TestAPIEmbeddedFileToDBFileWithEmptyMimeType(t *testing.T) {
	t.Parallel()
	file := api.EmbeddedFile{
		ID:       "file-1",
		IssueID:  "issue-1",
		URL:      "https://uploads.linear.app/workspace/file1/data.bin",
		Filename: "data.bin",
		MimeType: "", // Empty MIME type
		Source:   "description",
	}

	params := APIEmbeddedFileToDBFile(file)

	if params.MimeType.Valid {
		t.Error("MimeType should be invalid for empty string")
	}
}

func TestDBEmbeddedFileToAPIFile(t *testing.T) {
	t.Parallel()
	now := time.Now()
	file := EmbeddedFile{
		ID:        "file-1",
		IssueID:   "issue-1",
		Url:       "https://uploads.linear.app/workspace/file1/design.pdf",
		Filename:  "design.pdf",
		MimeType:  sql.NullString{String: "application/pdf", Valid: true},
		FileSize:  sql.NullInt64{Int64: 54321, Valid: true},
		CachePath: sql.NullString{String: "/tmp/cache/file-1", Valid: true},
		Source:    "comment:abc123",
		CreatedAt: now,
		SyncedAt:  now,
	}

	apiFile := DBEmbeddedFileToAPIFile(file)

	if apiFile.ID != file.ID {
		t.Errorf("ID mismatch: got %s, want %s", apiFile.ID, file.ID)
	}
	if apiFile.IssueID != file.IssueID {
		t.Errorf("IssueID mismatch: got %s, want %s", apiFile.IssueID, file.IssueID)
	}
	if apiFile.URL != file.Url {
		t.Errorf("URL mismatch: got %s, want %s", apiFile.URL, file.Url)
	}
	if apiFile.Filename != file.Filename {
		t.Errorf("Filename mismatch: got %s, want %s", apiFile.Filename, file.Filename)
	}
	if apiFile.MimeType != "application/pdf" {
		t.Errorf("MimeType mismatch: got %s, want application/pdf", apiFile.MimeType)
	}
	if apiFile.FileSize != 54321 {
		t.Errorf("FileSize mismatch: got %d, want 54321", apiFile.FileSize)
	}
	if apiFile.CachePath != "/tmp/cache/file-1" {
		t.Errorf("CachePath mismatch: got %s, want /tmp/cache/file-1", apiFile.CachePath)
	}
	if apiFile.Source != "comment:abc123" {
		t.Errorf("Source mismatch: got %s, want comment:abc123", apiFile.Source)
	}
}

func TestDBEmbeddedFileToAPIFileWithNulls(t *testing.T) {
	t.Parallel()
	now := time.Now()
	file := EmbeddedFile{
		ID:        "file-1",
		IssueID:   "issue-1",
		Url:       "https://uploads.linear.app/file.bin",
		Filename:  "file.bin",
		MimeType:  sql.NullString{Valid: false}, // Not valid
		FileSize:  sql.NullInt64{Valid: false},  // Not valid
		CachePath: sql.NullString{Valid: false}, // Not valid
		Source:    "description",
		CreatedAt: now,
		SyncedAt:  now,
	}

	apiFile := DBEmbeddedFileToAPIFile(file)

	if apiFile.MimeType != "" {
		t.Errorf("MimeType should be empty, got %s", apiFile.MimeType)
	}
	if apiFile.FileSize != 0 {
		t.Errorf("FileSize should be 0, got %d", apiFile.FileSize)
	}
	if apiFile.CachePath != "" {
		t.Errorf("CachePath should be empty, got %s", apiFile.CachePath)
	}
}

func TestDBEmbeddedFilesToAPIFiles(t *testing.T) {
	t.Parallel()
	now := time.Now()
	files := []EmbeddedFile{
		{
			ID:        "f1",
			IssueID:   "issue-1",
			Url:       "https://uploads.linear.app/file1.png",
			Filename:  "file1.png",
			MimeType:  sql.NullString{String: "image/png", Valid: true},
			Source:    "description",
			CreatedAt: now,
			SyncedAt:  now,
		},
		{
			ID:        "f2",
			IssueID:   "issue-1",
			Url:       "https://uploads.linear.app/file2.pdf",
			Filename:  "file2.pdf",
			MimeType:  sql.NullString{String: "application/pdf", Valid: true},
			Source:    "comment:xyz",
			CreatedAt: now,
			SyncedAt:  now,
		},
	}

	apiFiles := DBEmbeddedFilesToAPIFiles(files)

	if len(apiFiles) != 2 {
		t.Fatalf("Expected 2 files, got %d", len(apiFiles))
	}
	if apiFiles[0].ID != "f1" {
		t.Errorf("First file ID mismatch")
	}
	if apiFiles[0].Filename != "file1.png" {
		t.Errorf("First file Filename mismatch")
	}
	if apiFiles[1].ID != "f2" {
		t.Errorf("Second file ID mismatch")
	}
	if apiFiles[1].Source != "comment:xyz" {
		t.Errorf("Second file Source mismatch")
	}
}

func TestDBEmbeddedFilesToAPIFilesEmpty(t *testing.T) {
	t.Parallel()
	files := []EmbeddedFile{}

	apiFiles := DBEmbeddedFilesToAPIFiles(files)

	if len(apiFiles) != 0 {
		t.Errorf("Expected empty slice, got %d files", len(apiFiles))
	}
}
