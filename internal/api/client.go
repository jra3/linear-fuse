package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	gosync "sync"
	"sync/atomic"
	"time"

	"golang.org/x/time/rate"
)

var debugRateLimit = os.Getenv("LINEARFS_DEBUG_RATE") != ""
var debugAPI = os.Getenv("LINEARFS_DEBUG_API") != ""

const defaultAPIURL = "https://api.linear.app/graphql"

// Circuit breaker constants: after consecutive failures, stop wasting rate
// limiter tokens on requests that will fail (e.g., DNS outage).
const (
	circuitBreakerThreshold = 5                // consecutive errors to trip
	circuitBreakerCooldown  = 30 * time.Second // how long to stay open
)

// maxWriteWait caps how long a blocked mutation waits for the budget window
// to reset before it is returned as a deferral error instead. Mutations are
// user-facing (a FUSE flush blocks on them), so waiting past the HTTP
// timeout is absurd; reads never wait — a blocked read defers immediately
// and the sync worker's queues retry it.
const maxWriteWait = 30 * time.Second

type Client struct {
	apiKey     string
	apiURL     string
	httpClient *http.Client

	// metrics are the api-layer OTEL instruments (metrics.go): every
	// completed request records a count and a duration, per operation.
	metrics apiMetrics

	// reqLog, when non-nil, receives one JSON line per completed request —
	// the per-request debug log (requestlog.go). nil = disabled (default).
	reqLog io.Writer

	// budget is the hourly rate-limit governor (see ratebudget.go): query
	// admits every request through its priority-reserve ladder and observes
	// every response's headers back into it.
	budget *rateBudget

	// limiter is a thin micro-burst smoother only — the budget prevents
	// hourly overshoot, the limiter prevents instantaneous spikes. It is
	// re-sized from the server-reported request limit on first observation
	// (see syncLimiterSize); the construction-time rate is just a seed.
	limiter         *rate.Limiter
	limiterMu       gosync.Mutex
	limiterSizedFor float64 // last request limit applied to the limiter

	// Circuit breaker: stop burning rate limiter tokens during connectivity loss
	consecutiveErrors atomic.Int32
	circuitOpenUntil  atomic.Int64 // unix timestamp; 0 = closed
}

func NewClient(apiKey string) *Client {
	// The limiter is a micro-burst smoother, not the budget: hourly
	// governance lives in rateBudget (both axes, limits read from response
	// headers). The seed rate here is replaced by the observed request
	// limit on the first response (syncLimiterSize). The burst absorbs one
	// sync cycle's spike; sustained rate stays within budget regardless of
	// burst size.
	limiter := rate.NewLimiter(rate.Limit(float64(seedHourlyRequestLimit)/3600.0), 16)

	return &Client{
		apiKey:     apiKey,
		apiURL:     defaultAPIURL,
		httpClient: &http.Client{Timeout: 30 * time.Second},
		metrics:    newAPIMetrics(),
		budget:     newRateBudget(time.Now),
		limiter:    limiter,
	}
}

// AuthHeader returns the Authorization header value for API requests.
func (c *Client) AuthHeader() string {
	return c.apiKey
}

// SetAPIURL overrides the API URL (for testing).
func (c *Client) SetAPIURL(url string) {
	c.apiURL = url
}

type graphQLRequest struct {
	Query     string         `json:"query"`
	Variables map[string]any `json:"variables,omitempty"`
}

type graphQLResponse struct {
	Data   json.RawMessage `json:"data"`
	Errors []struct {
		Message    string `json:"message"`
		Extensions struct {
			Code                   string `json:"code"`
			UserError              bool   `json:"userError"`
			UserPresentableMessage string `json:"userPresentableMessage"`
		} `json:"extensions"`
	} `json:"errors,omitempty"`
}

// GraphQLError is a structured GraphQL rejection. Linear tags input
// rejections with extensions {code: "INPUT_ERROR", userError: true,
// userPresentableMessage: "..."} — the presentable message is far more
// actionable than the terse internal one (live example: internal "labelIds
// contain parent labels" vs presentable "The label 'X' is a group and cannot
// be assigned to projects directly."). Error() keeps the legacy "GraphQL
// error: <message>" shape so existing string matches keep working.
type GraphQLError struct {
	Message                string
	Code                   string
	UserError              bool
	UserPresentableMessage string
}

func (e *GraphQLError) Error() string { return "GraphQL error: " + e.Message }

func (c *Client) query(ctx context.Context, query string, variables map[string]any, result any) error {
	// Extract operation name for stats and logging
	opName := extractOpName(query)
	if debugAPI {
		log.Printf("[API] Calling %s vars=%v", opName, variables)
	}

	// Circuit breaker: skip requests when connectivity is known to be down.
	// This prevents burning rate limiter tokens on requests that will fail.
	if openUntil := c.circuitOpenUntil.Load(); openUntil > 0 {
		if time.Now().Unix() < openUntil {
			return fmt.Errorf("circuit breaker open: skipping %s (connectivity down)", opName)
		}
		// Cooldown expired — allow one probe request through
		c.circuitOpenUntil.Store(0)
	}

	// Budget gate: the priority-reserve ladder (ratebudget.go). Reads that
	// trip their tier's reserve defer immediately (the sync worker's queues
	// retry them); a blocked mutation waits for the window when the wait is
	// short, because writes are user-facing and must not be silently dropped.
	isMutation := strings.HasPrefix(strings.TrimSpace(query), "mutation")
	tier := tierFor(ctx, opName, isMutation)
	adm, dec := c.budget.admit(opName, tier)
	if adm == nil && tier == pWrite && dec.retryAfter > 0 && dec.retryAfter <= maxWriteWait {
		log.Printf("[ratelimit] mutation %s waiting %s for budget window reset", opName, dec.retryAfter.Round(time.Second))
		c.budget.metrics.recordDecision(tier, "wait")
		waitStart := time.Now()
		timer := time.NewTimer(dec.retryAfter)
		select {
		case <-ctx.Done():
			timer.Stop()
			c.budget.metrics.recordWait(time.Since(waitStart))
			return fmt.Errorf("rate limit wait cancelled: %w", ctx.Err())
		case <-timer.C:
		}
		c.budget.metrics.recordWait(time.Since(waitStart))
		adm, dec = c.budget.admit(opName, tier)
	}
	if adm == nil {
		return fmt.Errorf("rate limit: query %s deferred (%s)", opName, dec.reason)
	}
	// The admission must be settled exactly once. The success and
	// rate-limited paths settle explicitly below (observe/rateLimited);
	// this deferred release is the idempotent catch-all for every early
	// return (marshal error, transport error, cancellation).
	defer adm.release()
	// After the response has been observed, re-size the micro-burst
	// limiter to the server-reported request limit.
	defer c.syncLimiterSize()

	// Log token bucket exhaustion before blocking
	if tokens := c.limiter.Tokens(); tokens <= 0 {
		log.Printf("[ratelimit] token bucket empty, %s will block until tokens replenish", opName)
	}

	// Verbose debug: log every wait >1ms
	if debugRateLimit {
		reservation := c.limiter.Reserve()
		delay := reservation.Delay()
		if delay > time.Millisecond {
			log.Printf("[ratelimit] debug: %s reservation delay %v", opName, delay)
		}
		reservation.Cancel()
	}

	rateLimitStart := time.Now()
	if err := c.limiter.Wait(ctx); err != nil {
		return fmt.Errorf("rate limit wait cancelled: %w", err)
	}
	rateLimitWait := time.Since(rateLimitStart)
	if rateLimitWait > time.Millisecond {
		c.budget.metrics.recordWait(rateLimitWait)
	}
	// Always log noisy rate limit waits (no env var required)
	if rateLimitWait > 100*time.Millisecond {
		hourly, pct := c.BudgetSnapshot()
		log.Printf("[ratelimit] %s waited %s (budget: %d requests this hour, %.0f%% of limit)",
			opName, rateLimitWait.Round(time.Millisecond), hourly, pct)
	}

	// Record the request count (by outcome) and duration once it completes —
	// and, when enabled, the request debug log line (same site, same outcome
	// classification; the admission carries the response's X-Complexity by
	// the time this defer runs, since observe/rateLimited settle inline).
	reqStart := time.Now()
	var queryErr error
	defer func() {
		elapsed := time.Since(reqStart)
		c.metrics.record(ctx, opName, elapsed, queryErr)
		c.logRequest(opName, variables, elapsed, queryErr, adm)
	}()

	reqBody := graphQLRequest{
		Query:     query,
		Variables: variables,
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		queryErr = fmt.Errorf("failed to marshal request: %w", err)
		return queryErr
	}

	req, err := http.NewRequestWithContext(ctx, "POST", c.apiURL, bytes.NewReader(body))
	if err != nil {
		queryErr = fmt.Errorf("failed to create request: %w", err)
		return queryErr
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", c.apiKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		// Network/DNS error — track for circuit breaker
		if n := c.consecutiveErrors.Add(1); n >= circuitBreakerThreshold {
			c.circuitOpenUntil.Store(time.Now().Add(circuitBreakerCooldown).Unix())
			log.Printf("[circuit-breaker] opened after %d consecutive errors, cooling down %s", n, circuitBreakerCooldown)
		}
		queryErr = fmt.Errorf("failed to execute request: %w", err)
		return queryErr
	}
	defer resp.Body.Close()

	// Request succeeded at the network level — reset circuit breaker
	c.consecutiveErrors.Store(0)

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		// Headers arrived even though the body didn't: still observe them.
		adm.observe(resp.Header)
		queryErr = fmt.Errorf("failed to read response: %w", err)
		return queryErr
	}

	if resp.StatusCode == http.StatusTooManyRequests {
		adm.rateLimited(resp.Header)
		queryErr = fmt.Errorf("API error (status %d): %s", resp.StatusCode, string(respBody))
		log.Printf("[ratelimit] ERROR: %s rate limited by Linear API (HTTP 429): %s", opName, string(respBody))
		return queryErr
	}

	if resp.StatusCode != http.StatusOK {
		// Linear reports budget exhaustion as HTTP 400 with a RATELIMITED
		// error code in the body. Non-200 bodies are Linear's own error
		// envelope (never user data), so a substring check cannot false-
		// positive on issue content.
		if strings.Contains(string(respBody), "RATELIMITED") {
			adm.rateLimited(resp.Header)
			log.Printf("[ratelimit] ERROR: %s rate limited by Linear API (HTTP %d): %s", opName, resp.StatusCode, string(respBody))
		} else {
			adm.observe(resp.Header)
		}
		queryErr = fmt.Errorf("API error (status %d): %s", resp.StatusCode, string(respBody))
		return queryErr
	}

	var gqlResp graphQLResponse
	if err := json.Unmarshal(respBody, &gqlResp); err != nil {
		adm.observe(resp.Header)
		queryErr = fmt.Errorf("failed to parse response: %w", err)
		return queryErr
	}

	if len(gqlResp.Errors) > 0 {
		first := gqlResp.Errors[0]
		errMsg := first.Message
		queryErr = &GraphQLError{
			Message:                errMsg,
			Code:                   first.Extensions.Code,
			UserError:              first.Extensions.UserError,
			UserPresentableMessage: first.Extensions.UserPresentableMessage,
		}
		if IsRateLimited(queryErr) {
			adm.rateLimited(resp.Header)
			log.Printf("[ratelimit] ERROR: %s rate limited by Linear API: %s", opName, errMsg)
		} else {
			adm.observe(resp.Header)
		}
		return queryErr
	}

	// Success: settle the reservation and reconcile the budget to the
	// server-reported headers (both axes + this op's actual X-Complexity).
	adm.observe(resp.Header)

	if err := json.Unmarshal(gqlResp.Data, result); err != nil {
		queryErr = fmt.Errorf("failed to parse data: %w", err)
		return queryErr
	}

	return nil
}

// extractOpName extracts the GraphQL operation name from a query string —
// the "op" attribute on every api instrument and the rate budget's cost-
// predictor key (~30 stable values; the cardinality guard for op-attributed
// metrics). It finds the first word before '{' or '(' after
// "query"/"mutation".
func extractOpName(query string) string {
	if len(query) == 0 {
		return "unknown"
	}

	// Simple extraction: find first { or ( and take word before it
	for i, ch := range query {
		if ch == '{' || ch == '(' {
			if i == 0 {
				return "unknown"
			}
			// Walk backwards to find operation name
			end := i - 1
			for end > 0 && (query[end] == ' ' || query[end] == '\n') {
				end--
			}
			if end < 0 {
				return "unknown"
			}
			start := end
			for start > 0 && query[start-1] != ' ' && query[start-1] != '\n' {
				start--
			}
			if start <= end && end >= 0 {
				name := query[start : end+1]
				// Skip "query" or "mutation" keywords
				if name == "query" || name == "mutation" {
					return "unknown"
				}
				return name
			}
			break
		}
	}
	return "unknown"
}

// syncLimiterSize re-sizes the micro-burst limiter to the server-reported
// hourly request limit once the budget has observed it. No-op until the
// first response and after that only on change; the construction-time seed
// is never trusted past first contact.
func (c *Client) syncLimiterSize() {
	lim := c.budget.requestsLimit()
	if lim <= 0 {
		return
	}
	c.limiterMu.Lock()
	defer c.limiterMu.Unlock()
	if lim == c.limiterSizedFor {
		return
	}
	c.limiterSizedFor = lim
	c.limiter.SetLimit(rate.Limit(lim / 3600.0))
	log.Printf("[ratelimit] observed request limit %.0f/hr; limiter re-sized", lim)
}

// RateLimitResetAt returns the server-reported time when the rate limit
// window resets (the later of the two axes' resets, parsed from the
// per-axis millisecond headers). Zero until a response has been observed.
// The sync worker's backoff consults this instead of guessing.
func (c *Client) RateLimitResetAt() time.Time {
	return c.budget.resetAt()
}

// BudgetSnapshot reports hourly request usage as (requests used, percent of
// limit), from the budget's server-reported requests axis — (0, 0) until the
// first response has been observed. It satisfies the sync worker's
// BudgetReporter seam, the role the deleted APIStats' local rolling window
// used to play, now on server truth (so it also counts other consumers of
// the same API key).
func (c *Client) BudgetSnapshot() (count int, pct float64) {
	_, rq := c.budget.snapshot()
	if !rq.seen || rq.limit <= 0 {
		return 0, 0
	}
	used := rq.limit - rq.remaining
	if used < 0 {
		used = 0
	}
	return int(used), used / rq.limit * 100
}

// GetTeams fetches all teams the user has access to, draining the
// connection to completion — this is the sync worker's root fetch, so a
// silent 50-team cap would truncate the whole sync. All-or-nothing like
// every fetchAll caller; a low rate budget defers it (ErrBudget) and the
// worker retries next cycle.
func (c *Client) GetTeams(ctx context.Context) ([]Team, error) {
	return fetchAll[Team](ctx, c, queryTeams, nil, "teams")
}

// GetTeamIssuesPage fetches a single page of issues ordered by updatedAt DESC.
// Returns the issues, page info, and any error.
// Use cursor="" for the first page.
func (c *Client) GetTeamIssuesPage(ctx context.Context, teamID string, cursor string, pageSize int) ([]Issue, PageInfo, error) {
	vars := map[string]any{
		"teamId": teamID,
		"first":  pageSize,
	}
	if cursor != "" {
		vars["after"] = cursor
	}

	cn, err := fetchConn[Issue](ctx, c, queryTeamIssuesByUpdatedAt, vars, "team", "issues")
	if err != nil {
		return nil, PageInfo{}, err
	}
	return cn.Nodes, *cn.PageInfo, nil
}

// GetIssue fetches a single issue by ID
func (c *Client) GetIssue(ctx context.Context, issueID string) (*Issue, error) {
	return fetchOne[Issue](ctx, c, queryIssue, map[string]any{"id": issueID}, "issue")
}

// GetProject fetches a single project by ID
func (c *Client) GetProject(ctx context.Context, projectID string) (*Project, error) {
	return fetchOne[Project](ctx, c, queryProject, map[string]any{"id": projectID}, "project")
}

// UpdateIssue updates an existing issue
func (c *Client) UpdateIssue(ctx context.Context, issueID string, input map[string]any) error {
	return execMutationOK(ctx, c, mutationUpdateIssue, map[string]any{"id": issueID, "input": input}, "issueUpdate")
}

// ArchiveIssue archives an issue (soft delete)
func (c *Client) ArchiveIssue(ctx context.Context, issueID string) error {
	return execMutationOK(ctx, c, mutationArchiveIssue, map[string]any{"id": issueID}, "issueArchive")
}

// GetTeamMetadata fetches all metadata for a team: states, labels (team +
// workspace, deduplicated), cycles, members — one combined query, with any
// connection reporting hasNextPage drained to completion — and projects via
// the paginated GetTeamProjects (too complexity-expensive to share the
// combined query; see queryTeamMetadata). The returned sets are complete:
// the sync worker prunes against them.
//
// The LowBudget preflight refuses BEFORE paying the combined query: the
// query itself admits at a lower reserve than the drains and the projects
// fetchAll behind it, so without the preflight a budget between the two
// floors buys the combined result and then discards it when a follow-up
// refuses (ErrBudget's burn-and-discard, observed live, one level up).
func (c *Client) GetTeamMetadata(ctx context.Context, teamID string) (*TeamMetadata, error) {
	if c.LowBudget() {
		return nil, fmt.Errorf("team metadata %s: %w", teamID, ErrBudget)
	}
	var result struct {
		Team struct {
			States struct {
				Nodes []State `json:"nodes"`
			} `json:"states"`
			Labels  conn[Label] `json:"labels"`
			Cycles  conn[Cycle] `json:"cycles"`
			Members conn[User]  `json:"members"`
		} `json:"team"`
		IssueLabels conn[Label] `json:"issueLabels"`
	}

	vars := map[string]any{
		"teamId": teamID,
	}

	err := c.query(ctx, queryTeamMetadata, vars, &result)
	if err != nil {
		return nil, err
	}

	teamLabels := result.Team.Labels.Nodes
	moreLabels, err := drain[Label](ctx, c, queryTeamLabelsPage, vars, result.Team.Labels.PageInfo, "team", "labels")
	if err != nil {
		return nil, fmt.Errorf("drain team labels: %w", err)
	}
	teamLabels = append(teamLabels, moreLabels...)

	cycles := result.Team.Cycles.Nodes
	moreCycles, err := drain[Cycle](ctx, c, queryTeamCyclesPage, vars, result.Team.Cycles.PageInfo, "team", "cycles")
	if err != nil {
		return nil, fmt.Errorf("drain team cycles: %w", err)
	}
	cycles = append(cycles, moreCycles...)

	members := result.Team.Members.Nodes
	moreMembers, err := drain[User](ctx, c, queryTeamMembersPage, vars, result.Team.Members.PageInfo, "team", "members")
	if err != nil {
		return nil, fmt.Errorf("drain team members: %w", err)
	}
	members = append(members, moreMembers...)

	workspaceLabels := result.IssueLabels.Nodes
	moreWorkspace, err := drain[Label](ctx, c, queryWorkspaceLabelsPage, nil, result.IssueLabels.PageInfo, "issueLabels")
	if err != nil {
		return nil, fmt.Errorf("drain workspace labels: %w", err)
	}
	workspaceLabels = append(workspaceLabels, moreWorkspace...)

	projects, err := c.GetTeamProjects(ctx, teamID)
	if err != nil {
		return nil, fmt.Errorf("fetch team projects: %w", err)
	}

	// Combine team labels and workspace labels, deduplicating by ID
	seen := make(map[string]bool)
	var labels []Label
	for _, l := range teamLabels {
		if !seen[l.ID] {
			seen[l.ID] = true
			labels = append(labels, l)
		}
	}
	for _, l := range workspaceLabels {
		if !seen[l.ID] {
			seen[l.ID] = true
			labels = append(labels, l)
		}
	}

	return &TeamMetadata{
		States:   result.Team.States.Nodes,
		Labels:   labels,
		Cycles:   cycles,
		Projects: projects,
		Members:  members,
	}, nil
}

// GetInitiativesProbe fetches the newest few initiatives by updatedAt,
// scalars only — each returned Initiative carries an empty Projects (the
// probe query selects no nested projects connection). It is the lean
// cycle's change-detection probe (#244): the caller compares the max
// UpdatedAt against a persisted watermark and runs the full GetWorkspace
// sync only on change. See queryInitiativesProbe for the cost rationale
// and the link-timestamp live-check finding (link/unlink bumps neither
// side's updatedAt, so link changes are not probe-visible).
func (c *Client) GetInitiativesProbe(ctx context.Context) ([]Initiative, error) {
	return fetchNodes[Initiative](ctx, c, queryInitiativesProbe, nil, "initiatives")
}

// GetWorkspace fetches workspace-level entities (users and initiatives),
// drained to completion — including each initiative's nested projects
// connection, whose completeness is load-bearing: the sync worker prunes
// initiative_projects junction rows against it, so a truncated list would
// read as removals. Every returned Initiative has a complete Projects.Nodes
// and a nil Projects.PageInfo.
//
// LowBudget preflight for the same reason as GetTeamMetadata: refuse before
// paying the combined query rather than after, when a per-initiative drain
// would refuse and discard it.
func (c *Client) GetWorkspace(ctx context.Context) (*WorkspaceData, error) {
	if c.LowBudget() {
		return nil, fmt.Errorf("workspace: %w", ErrBudget)
	}
	var result struct {
		Users       conn[User]       `json:"users"`
		Initiatives conn[Initiative] `json:"initiatives"`
	}

	err := c.query(ctx, queryWorkspace, nil, &result)
	if err != nil {
		return nil, err
	}

	users := result.Users.Nodes
	moreUsers, err := drain[User](ctx, c, queryWorkspaceUsersPage, nil, result.Users.PageInfo, "users")
	if err != nil {
		return nil, fmt.Errorf("drain users: %w", err)
	}
	users = append(users, moreUsers...)

	initiatives := result.Initiatives.Nodes
	moreInitiatives, err := drain[Initiative](ctx, c, queryWorkspaceInitiativesPage, nil, result.Initiatives.PageInfo, "initiatives")
	if err != nil {
		return nil, fmt.Errorf("drain initiatives: %w", err)
	}
	initiatives = append(initiatives, moreInitiatives...)

	for i := range initiatives {
		init := &initiatives[i]
		moreProjects, err := drain[InitiativeProject](ctx, c, queryInitiativeProjectsPage,
			map[string]any{"id": init.ID}, init.Projects.PageInfo, "initiative", "projects")
		if err != nil {
			return nil, fmt.Errorf("drain initiative %s projects: %w", init.Slug, err)
		}
		init.Projects.Nodes = append(init.Projects.Nodes, moreProjects...)
		init.Projects.PageInfo = nil
	}

	return &WorkspaceData{
		Users:       users,
		Initiatives: initiatives,
	}, nil
}

// GetTeamProjects fetches all projects for a team. Paginated: a team's
// projects connection was the first observed to overflow a page (silently
// truncating teams/ views and dangling initiative symlinks).
func (c *Client) GetTeamProjects(ctx context.Context, teamID string) ([]Project, error) {
	return fetchAll[Project](ctx, c, queryTeamProjects,
		map[string]any{"teamId": teamID}, "team", "projects")
}

// GetTeamProjectsNewestPage fetches a single page of a team's projects
// ordered by updatedAt DESC — the lean cycle's projects change-detection
// probe and its resume pages (#243), the projects sibling of
// GetTeamIssuesPage. Use cursor="" for the first page. The caller chooses the
// page size: the probe page is small (nested selections cost ~187 complexity
// per node, so a handful of nodes keeps the unchanged-world check near 1K),
// resume pages use the full-drain size.
func (c *Client) GetTeamProjectsNewestPage(ctx context.Context, teamID string, cursor string, pageSize int) ([]Project, PageInfo, error) {
	vars := map[string]any{
		"teamId": teamID,
		"first":  pageSize,
	}
	if cursor != "" {
		vars["after"] = cursor
	}

	cn, err := fetchConn[Project](ctx, c, queryTeamProjectsByUpdatedAt, vars, "team", "projects")
	if err != nil {
		return nil, PageInfo{}, err
	}
	return cn.Nodes, *cn.PageInfo, nil
}

// GetProjectLabels drains the workspace project-label catalog to completion.
// No filter deliberately: the drain must include retired and group labels —
// completeness is what licenses the sync pass's full-table prune (see
// queryProjectLabelsPage).
func (c *Client) GetProjectLabels(ctx context.Context) ([]ProjectLabel, error) {
	return fetchAll[ProjectLabel](ctx, c, queryProjectLabelsPage, nil, "projectLabels")
}

// CreateProjectMilestone creates a new milestone for a project
func (c *Client) CreateProjectMilestone(ctx context.Context, projectID, name, description string) (*ProjectMilestone, error) {
	vars := map[string]any{
		"projectId": projectID,
		"name":      name,
	}
	if description != "" {
		vars["description"] = description
	}
	return execMutation[ProjectMilestone](ctx, c, mutationCreateProjectMilestone, vars, "projectMilestoneCreate", "projectMilestone")
}

// UpdateProjectMilestone updates an existing milestone
func (c *Client) UpdateProjectMilestone(ctx context.Context, milestoneID string, input ProjectMilestoneUpdateInput) (*ProjectMilestone, error) {
	return execMutation[ProjectMilestone](ctx, c, mutationUpdateProjectMilestone, map[string]any{"id": milestoneID, "input": input}, "projectMilestoneUpdate", "projectMilestone")
}

// UpdateProject updates a project's mutable fields (name, content).
func (c *Client) UpdateProject(ctx context.Context, projectID string, input ProjectUpdateInput) error {
	return execMutationOK(ctx, c, mutationUpdateProject, map[string]any{"id": projectID, "input": input}, "projectUpdate")
}

// UpdateInitiative updates an initiative's mutable fields (name, content).
func (c *Client) UpdateInitiative(ctx context.Context, initiativeID string, input InitiativeUpdateInput) error {
	return execMutationOK(ctx, c, mutationUpdateInitiative, map[string]any{"id": initiativeID, "input": input}, "initiativeUpdate")
}

// DeleteProjectMilestone deletes a milestone
func (c *Client) DeleteProjectMilestone(ctx context.Context, milestoneID string) error {
	return execMutationOK(ctx, c, mutationDeleteProjectMilestone, map[string]any{"id": milestoneID}, "projectMilestoneDelete")
}

// GetProjectUpdates fetches status updates for a project, drained — updates
// accumulate past a page over a project's lifetime, and the SWR refresh is
// upsert-only, so a capped read silently froze completeness.
func (c *Client) GetProjectUpdates(ctx context.Context, projectID string) ([]ProjectUpdate, error) {
	return fetchAll[ProjectUpdate](ctx, c, queryProjectUpdates,
		map[string]any{"projectId": projectID}, "project", "projectUpdates")
}

// CreateProjectUpdate creates a new status update on a project
func (c *Client) CreateProjectUpdate(ctx context.Context, projectID, body, health string) (*ProjectUpdate, error) {
	vars := map[string]any{
		"projectId": projectID,
		"body":      body,
	}
	if health != "" {
		vars["health"] = health
	}
	return execMutation[ProjectUpdate](ctx, c, mutationCreateProjectUpdate, vars, "projectUpdateCreate", "projectUpdate")
}

// GetInitiativeUpdates fetches status updates for an initiative, drained
// (see GetProjectUpdates).
func (c *Client) GetInitiativeUpdates(ctx context.Context, initiativeID string) ([]InitiativeUpdate, error) {
	return fetchAll[InitiativeUpdate](ctx, c, queryInitiativeUpdates,
		map[string]any{"initiativeId": initiativeID}, "initiative", "initiativeUpdates")
}

// CreateInitiativeUpdate creates a new status update on an initiative
func (c *Client) CreateInitiativeUpdate(ctx context.Context, initiativeID, body, health string) (*InitiativeUpdate, error) {
	vars := map[string]any{
		"initiativeId": initiativeID,
		"body":         body,
	}
	if health != "" {
		vars["health"] = health
	}
	return execMutation[InitiativeUpdate](ctx, c, mutationCreateInitiativeUpdate, vars, "initiativeUpdateCreate", "initiativeUpdate")
}

// CreateProject creates a new project
func (c *Client) CreateProject(ctx context.Context, input map[string]any) (*Project, error) {
	return execMutation[Project](ctx, c, mutationCreateProject, map[string]any{"input": input}, "projectCreate", "project")
}

// ArchiveProject archives a project (soft delete)
func (c *Client) ArchiveProject(ctx context.Context, projectID string) error {
	return execMutationOK(ctx, c, mutationArchiveProject, map[string]any{"id": projectID}, "projectArchive")
}

// AddProjectToInitiative links a project to an initiative
func (c *Client) AddProjectToInitiative(ctx context.Context, projectID, initiativeID string) error {
	return execMutationOK(ctx, c, mutationInitiativeToProjectCreate, map[string]any{"projectId": projectID, "initiativeId": initiativeID}, "initiativeToProjectCreate")
}

// RemoveProjectFromInitiative unlinks a project from an initiative
func (c *Client) RemoveProjectFromInitiative(ctx context.Context, projectID, initiativeID string) error {
	return execMutationOK(ctx, c, mutationInitiativeToProjectDelete, map[string]any{"projectId": projectID, "initiativeId": initiativeID}, "initiativeToProjectDelete")
}

// CreateLabel creates a new label
func (c *Client) CreateLabel(ctx context.Context, input map[string]any) (*Label, error) {
	return execMutation[Label](ctx, c, mutationCreateLabel, map[string]any{"input": input}, "issueLabelCreate", "issueLabel")
}

// UpdateLabel updates an existing label
func (c *Client) UpdateLabel(ctx context.Context, id string, input map[string]any) (*Label, error) {
	return execMutation[Label](ctx, c, mutationUpdateLabel, map[string]any{"id": id, "input": input}, "issueLabelUpdate", "issueLabel")
}

// DeleteLabel deletes a label
func (c *Client) DeleteLabel(ctx context.Context, id string) error {
	return execMutationOK(ctx, c, mutationDeleteLabel, map[string]any{"id": id}, "issueLabelDelete")
}

// GetViewer fetches the currently authenticated user
func (c *Client) GetViewer(ctx context.Context) (*User, error) {
	return fetchOne[User](ctx, c, queryViewer, nil, "viewer")
}

// CreateIssue creates a new issue
func (c *Client) CreateIssue(ctx context.Context, input map[string]any) (*Issue, error) {
	return execMutation[Issue](ctx, c, mutationCreateIssue, map[string]any{"input": input}, "issueCreate", "issue")
}

// IssueDetails contains comments, documents, attachments, and relations for
// an issue. Relations are the outgoing rows this issue owns; InverseRelations
// are incoming rows owned by the other issue (their `Issue` field is set).
type IssueDetails struct {
	Comments         []Comment
	Documents        []Document
	Attachments      []Attachment
	Relations        []IssueRelation
	InverseRelations []IssueRelation
}

// issueDetailsPayload is the wire shape of one issue's IssueDetailsSelection,
// shared by the single-issue query and each alias of the batch query.
type issueDetailsPayload struct {
	Comments struct {
		Nodes []Comment `json:"nodes"`
	} `json:"comments"`
	Documents struct {
		Nodes []Document `json:"nodes"`
	} `json:"documents"`
	Attachments struct {
		Nodes []Attachment `json:"nodes"`
	} `json:"attachments"`
	Relations struct {
		Nodes []IssueRelation `json:"nodes"`
	} `json:"relations"`
	InverseRelations struct {
		Nodes []IssueRelation `json:"nodes"`
	} `json:"inverseRelations"`
}

func (p issueDetailsPayload) toDetails() *IssueDetails {
	return &IssueDetails{
		Comments:         p.Comments.Nodes,
		Documents:        p.Documents.Nodes,
		Attachments:      p.Attachments.Nodes,
		Relations:        p.Relations.Nodes,
		InverseRelations: p.InverseRelations.Nodes,
	}
}

// GetIssueDetails fetches comments, documents, attachments, and relations for
// an issue in a single query. A null issue (not found) is an error, never
// five empty, "complete" collections — the same contract as the batch.
func (c *Client) GetIssueDetails(ctx context.Context, issueID string) (*IssueDetails, error) {
	payload, err := fetchOne[issueDetailsPayload](ctx, c, queryIssueDetails,
		map[string]any{"issueId": issueID}, "issue")
	if err != nil {
		return nil, err
	}
	return payload.toDetails(), nil
}

// GetIssueDetailsBatch fetches comments, documents, attachments, and relations
// for multiple issues in a single query, using GraphQL aliases to batch requests.
//
// The result is all-or-nothing: a nil-error return guarantees the map holds a
// non-nil entry for every requested issue ID. A missing alias, a null alias,
// or a payload that fails to decode fails the whole call with an error naming
// the issue. Callers prune SQLite rows against these details, so a silent gap
// (or a null decoded as five empty, "complete" collections) would prune a live
// issue's details.
func (c *Client) GetIssueDetailsBatch(ctx context.Context, issueIDs []string) (map[string]*IssueDetails, error) {
	if len(issueIDs) == 0 {
		return make(map[string]*IssueDetails), nil
	}

	// Build a batched query using aliases
	// Example: query { i0: issue(id: "id1") { ... } i1: issue(id: "id2") { ... } }
	var queryParts []string
	vars := make(map[string]any)

	for i, id := range issueIDs {
		alias := fmt.Sprintf("i%d", i)
		varName := fmt.Sprintf("id%d", i)
		queryParts = append(queryParts, fmt.Sprintf(`%s: issue(id: $%s) { %s }`,
			alias, varName, IssueDetailsSelection))
		vars[varName] = id
	}

	// Build variable declarations
	var varDecls []string
	for i := range issueIDs {
		varDecls = append(varDecls, fmt.Sprintf("$id%d: String!", i))
	}

	query := fmt.Sprintf(`query IssueDetailsBatch(%s) { %s } %s %s %s %s %s`,
		strings.Join(varDecls, ", "),
		strings.Join(queryParts, " "),
		CommentFieldsFragment,
		DocumentFieldsFragment,
		AttachmentFieldsFragment,
		issueRelationFieldsFragment,
		issueInverseRelationFieldsFragment,
	)

	// Result will be a map of alias -> issue data
	var rawResult map[string]json.RawMessage
	err := c.query(ctx, query, vars, &rawResult)
	if err != nil {
		return nil, err
	}

	// Parse each aliased result. Unmarshalling into a pointer distinguishes a
	// null alias (issue not found) from a present payload — a zero
	// issueDetailsPayload is indistinguishable from a real issue with no
	// details.
	result := make(map[string]*IssueDetails, len(issueIDs))
	for i, id := range issueIDs {
		alias := fmt.Sprintf("i%d", i)
		raw, ok := rawResult[alias]
		if !ok {
			return nil, fmt.Errorf("issue details batch: alias %s (issue %s) missing from response", alias, id)
		}

		var issueData *issueDetailsPayload
		if err := json.Unmarshal(raw, &issueData); err != nil {
			return nil, fmt.Errorf("issue details batch: alias %s (issue %s): %w", alias, id, err)
		}
		if issueData == nil {
			return nil, fmt.Errorf("issue details batch: alias %s (issue %s) is null", alias, id)
		}

		result[id] = issueData.toDetails()
	}

	return result, nil
}

// CreateComment creates a new comment on an issue
func (c *Client) CreateComment(ctx context.Context, issueID string, body string) (*Comment, error) {
	return execMutation[Comment](ctx, c, mutationCreateComment, map[string]any{"issueId": issueID, "body": body}, "commentCreate", "comment")
}

// UpdateComment updates an existing comment
func (c *Client) UpdateComment(ctx context.Context, commentID string, body string) (*Comment, error) {
	return execMutation[Comment](ctx, c, mutationUpdateComment, map[string]any{"id": commentID, "body": body}, "commentUpdate", "comment")
}

// DeleteComment deletes a comment
func (c *Client) DeleteComment(ctx context.Context, commentID string) error {
	return execMutationOK(ctx, c, mutationDeleteComment, map[string]any{"id": commentID}, "commentDelete")
}

// GetIssueAttachments fetches attachments (external links) for an issue
func (c *Client) GetIssueAttachments(ctx context.Context, issueID string) ([]Attachment, error) {
	return fetchNodes[Attachment](ctx, c, queryIssueAttachments,
		map[string]any{"issueId": issueID}, "issue", "attachments")
}

// GetProjectLinks fetches the external links ("Links / Resources") for a project.
func (c *Client) GetProjectLinks(ctx context.Context, projectID string) ([]EntityExternalLink, error) {
	return fetchNodes[EntityExternalLink](ctx, c, queryProjectExternalLinks,
		map[string]any{"projectId": projectID}, "project", "externalLinks")
}

// GetInitiativeLinks fetches the external links for an initiative.
func (c *Client) GetInitiativeLinks(ctx context.Context, initiativeID string) ([]EntityExternalLink, error) {
	return fetchNodes[EntityExternalLink](ctx, c, queryInitiativeExternalLinks,
		map[string]any{"initiativeId": initiativeID}, "initiative", "links")
}

// GetIssueHistory fetches the history/audit trail for an issue, drained —
// it backs history.md live and an old issue's trail outgrows a page.
func (c *Client) GetIssueHistory(ctx context.Context, issueID string) ([]IssueHistoryEntry, error) {
	return fetchAll[IssueHistoryEntry](ctx, c, queryIssueHistory,
		map[string]any{"issueId": issueID}, "issue", "history")
}

// GetTeamDocuments returns an empty list since Linear API doesn't support team-level documents
// Documents can be attached to issues or projects, but not directly to teams
func (c *Client) GetTeamDocuments(ctx context.Context, teamID string) ([]Document, error) {
	return []Document{}, nil
}

// GetProjectDocuments fetches documents for a project, drained.
func (c *Client) GetProjectDocuments(ctx context.Context, projectID string) ([]Document, error) {
	return fetchAll[Document](ctx, c, queryProjectDocuments,
		map[string]any{"projectId": projectID}, "documents")
}

// GetInitiativeDocuments fetches documents for an initiative, drained.
func (c *Client) GetInitiativeDocuments(ctx context.Context, initiativeID string) ([]Document, error) {
	return fetchAll[Document](ctx, c, queryInitiativeDocuments,
		map[string]any{"initiativeId": initiativeID}, "documents")
}

// CreateDocument creates a new document
func (c *Client) CreateDocument(ctx context.Context, input map[string]any) (*Document, error) {
	return execMutation[Document](ctx, c, mutationCreateDocument, map[string]any{"input": input}, "documentCreate", "document")
}

// UpdateDocument updates an existing document
func (c *Client) UpdateDocument(ctx context.Context, documentID string, input map[string]any) (*Document, error) {
	return execMutation[Document](ctx, c, mutationUpdateDocument, map[string]any{"id": documentID, "input": input}, "documentUpdate", "document")
}

// DeleteDocument deletes a document
func (c *Client) DeleteDocument(ctx context.Context, documentID string) error {
	return execMutationOK(ctx, c, mutationDeleteDocument, map[string]any{"id": documentID}, "documentDelete")
}

// GetInitiative fetches a single initiative by ID, with its projects
// connection drained — same contract as GetWorkspace's initiatives: the
// result is persisted whole (the initiative.md edit tail upserts it, and
// the projects list rides in the data blob that backs the FUSE view), so a
// truncated list would clobber a previously complete one. Complete
// Projects.Nodes, nil Projects.PageInfo.
func (c *Client) GetInitiative(ctx context.Context, initiativeID string) (*Initiative, error) {
	init, err := fetchOne[Initiative](ctx, c, queryInitiative, map[string]any{"id": initiativeID}, "initiative")
	if err != nil {
		return nil, err
	}
	moreProjects, err := drain[InitiativeProject](ctx, c, queryInitiativeProjectsPage,
		map[string]any{"id": initiativeID}, init.Projects.PageInfo, "initiative", "projects")
	if err != nil {
		return nil, fmt.Errorf("drain initiative %s projects: %w", init.Slug, err)
	}
	init.Projects.Nodes = append(init.Projects.Nodes, moreProjects...)
	init.Projects.PageInfo = nil
	return init, nil
}

// =============================================================================
// Issue Relations
// =============================================================================

// CreateIssueRelation creates a relation between two issues
// relationType must be one of: blocks, duplicate, related, similar
func (c *Client) CreateIssueRelation(ctx context.Context, issueID, relatedIssueID, relationType string) (*IssueRelation, error) {
	return execMutation[IssueRelation](ctx, c, mutationCreateIssueRelation, map[string]any{"issueId": issueID, "relatedIssueId": relatedIssueID, "type": relationType}, "issueRelationCreate", "issueRelation")
}

// DeleteIssueRelation deletes an issue relation
func (c *Client) DeleteIssueRelation(ctx context.Context, relationID string) error {
	return execMutationOK(ctx, c, mutationDeleteIssueRelation, map[string]any{"id": relationID}, "issueRelationDelete")
}

// =============================================================================
// Attachments Create/Link
// =============================================================================

// CreateAttachment creates a generic attachment (external link) on an issue
func (c *Client) CreateAttachment(ctx context.Context, issueID, title, url, subtitle string) (*Attachment, error) {
	vars := map[string]any{
		"issueId": issueID,
		"title":   title,
		"url":     url,
	}
	if subtitle != "" {
		vars["subtitle"] = subtitle
	}
	return execMutation[Attachment](ctx, c, mutationCreateAttachment, vars, "attachmentCreate", "attachment")
}

// LinkURL creates an attachment by linking a URL (Linear auto-detects type)
func (c *Client) LinkURL(ctx context.Context, issueID, url, title string) (*Attachment, error) {
	vars := map[string]any{
		"issueId": issueID,
		"url":     url,
	}
	if title != "" {
		vars["title"] = title
	}
	return execMutation[Attachment](ctx, c, mutationLinkURL, vars, "attachmentLinkURL", "attachment")
}

// DeleteAttachment deletes an attachment
func (c *Client) DeleteAttachment(ctx context.Context, attachmentID string) error {
	return execMutationOK(ctx, c, mutationDeleteAttachment, map[string]any{"id": attachmentID}, "attachmentDelete")
}

// =============================================================================
// Entity External Links (project/initiative "Links / Resources")
// =============================================================================

// CreateEntityExternalLink creates an external link on a project or initiative.
// The input map carries the EntityExternalLinkCreateInput fields — required
// `url` and `label`, plus exactly one parent id (`projectId` or `initiativeId`).
func (c *Client) CreateEntityExternalLink(ctx context.Context, input map[string]any) (*EntityExternalLink, error) {
	return execMutation[EntityExternalLink](ctx, c, mutationCreateEntityExternalLink,
		map[string]any{"input": input}, "entityExternalLinkCreate", "entityExternalLink")
}

// DeleteEntityExternalLink deletes an external link by ID.
func (c *Client) DeleteEntityExternalLink(ctx context.Context, id string) error {
	return execMutationOK(ctx, c, mutationDeleteEntityExternalLink, map[string]any{"id": id}, "entityExternalLinkDelete")
}

// LowBudget reports whether a conservatively-priced list-tier request would
// currently be refused by the rate budget. The paginate module refuses to
// start a new drain on it, and the reconciliation pass defers its per-team
// sweeps — leaving headroom for user-facing writes and ongoing sync.
func (c *Client) LowBudget() bool {
	return c.budget.low(pList)
}

// GetTeamIssueIDs returns the IDs of every issue in the team, draining the
// connection through the paginate seam. Used by the reconciliation pass —
// much cheaper than fetching full IssueFields. All-or-nothing: the reconcile
// pass diffs-and-deletes against this set, so a partial result must surface
// as an error, never as a short list (fetchAll guarantees it), like
// GetWorkspaceProjectIDs.
func (c *Client) GetTeamIssueIDs(ctx context.Context, teamID string) ([]string, error) {
	nodes, err := fetchAll[idNode](ctx, c, queryTeamIssueIDs, map[string]any{"teamId": teamID}, "team", "issues")
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(nodes))
	for _, n := range nodes {
		ids = append(ids, n.ID)
	}
	return ids, nil
}

// idNode is the projection the reconcile ID sweeps decode.
type idNode struct {
	ID string `json:"id"`
}

// GetWorkspaceProjectIDs returns IDs of every project in the workspace.
// All-or-nothing: the reconcile pass diffs-and-deletes against this set,
// so a partial result must surface as an error, never as a short list
// (fetchAll guarantees it).
func (c *Client) GetWorkspaceProjectIDs(ctx context.Context) ([]string, error) {
	nodes, err := fetchAll[idNode](ctx, c, queryWorkspaceProjectIDs, nil, "projects")
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(nodes))
	for _, n := range nodes {
		ids = append(ids, n.ID)
	}
	return ids, nil
}

// GetWorkspaceInitiativeIDs returns IDs of every initiative in the
// workspace. Complete or error, like GetWorkspaceProjectIDs.
func (c *Client) GetWorkspaceInitiativeIDs(ctx context.Context) ([]string, error) {
	nodes, err := fetchAll[idNode](ctx, c, queryWorkspaceInitiativeIDs, nil, "initiatives")
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(nodes))
	for _, n := range nodes {
		ids = append(ids, n.ID)
	}
	return ids, nil
}
