# Feasibility study: real-time cache invalidation via webhooks (#27)

Date: 2026-07-08
Status: feasibility assessed — API supports it, but **admin-gated**; see verdict.

## The core question

> Is there a way to register or de-register webhooks via the API?

**Yes — full lifecycle.** Verified against the current schema
(`docs/linear-schema.graphql`):

| Operation | Mutation / query | Notes |
|---|---|---|
| Register | `webhookCreate(input: WebhookCreateInput!)` | `url` + `resourceTypes` required; `teamId` **or** `allPublicTeams`; optional `label`, `secret`, `enabled` |
| Re-point | `webhookUpdate(id, input)` | **Can change `url` in place** — no delete/recreate churn |
| De-register | `webhookDelete(id)` | |
| Rotate secret | `webhookRotateSecret(id)` | Returns new HMAC-SHA256 key, old one immediately invalid |
| List / inspect | `webhooks(first:…)`, `webhook(id)` | Workspace-scoped; `Webhook` type includes `WebhookFailureEvent` history for delivery debugging |

`webhookUpdate` changing the URL in place is the load-bearing fact for the
tunnel decision: a **quick tunnel with a random URL per restart is perfectly
workable** — keep exactly one webhook labeled `linearfs`, and on each mount
find-by-label and `webhookUpdate` its URL. No stable DNS required, no webhook
accumulation. ngrok vs cloudflared quick tunnel becomes a pure
operational-preference choice, not an API constraint.

## The blocker: workspace-admin gate (verified live)

Probed live on 2026-07-08 with the mount's API key:

```
{"errors":[{... "userPresentableMessage":
  "You must be a workspace admin to interact with webhooks."}]}
```

- `viewer` = John Allen, `john@antimetal.com`, workspace **Antimetal**, `admin: false`.
- The gate covers **all interaction** — even the read-only `webhooks` query
  403s. Team-scoped webhooks (`teamId` in the create input) do not relax it;
  the role check is workspace-level.
- The Linear settings UI has the same gate, so "just create it manually in
  the UI" also requires an admin.

So auto-registration is technically feasible but **not with this credential**.

## Resource-type coverage (webhooks can't fully replace the sync worker)

`WebhookResourceType` enum covers: Issue, Comment, Document, Project,
ProjectUpdate, ProjectLabel, IssueLabel, Initiative, InitiativeUpdate, Cycle,
Attachment, Reaction, User (plus agent/customer/release types we don't use).

**Not in the enum:** Team, WorkflowState, **ProjectMilestone**,
IssueRelation, ProjectStatus. Changes to states, teams, and milestones will
never push. Relations *may* arrive as part of an Issue update payload
(unverified). Conclusion: webhooks are a **latency optimization for the hot
entities**, layered on top of the existing sync worker — never a replacement.
That matches the invalidation-only architecture in #27 anyway.

## Deployment modes (design consequence)

The admin gate splits the feature into two independently useful halves:

1. **Webhook receiver (no admin needed at runtime).** Embedded HTTP server:
   verify `Linear-Signature` (HMAC-SHA256 of raw body), map event → SQLite
   upsert/invalidate → `InodeNotify`/`EntryNotify`. Config carries the shared
   secret + port. Registration happened out-of-band (an admin, once, via UI
   or a one-shot admin-key CLI). Requires a **stable URL** → named Cloudflare
   tunnel (or any static HTTPS ingress).
2. **Auto-registration (admin key only).** `linearfs webhook create/list/delete`
   CLI + on-mount find-or-create-by-label + `webhookUpdate` URL re-point.
   This is what makes quick tunnels viable. Should degrade gracefully: on
   403, log "webhook auto-registration requires workspace admin" and fall
   back to receiver-only or plain polling.

For John's Antimetal deployment specifically:
- **Path A (recommended):** ask an Antimetal admin to create one webhook
  (one-time, ~2 min) pointed at a named Cloudflare tunnel URL; John holds the
  signing secret in `config.yaml`. Only half 1 needs to be built.
- **Path B:** get admin role → both halves work, quick tunnels included.
- **Path C (dev/testing):** a personal Linear workspace where John *is*
  admin exercises the full auto-registration path end-to-end.
- OAuth-app webhooks exist as a third registration route, but Linear
  app creation is also admin-gated in workspace settings — no advantage here.

## Can the org be configured to allow non-admin webhook access? (researched 2026-07-08)

**No.** The gate is hard-coded, not a workspace setting. Linear's docs:
"Only workspace admins, or OAuth applications with the `admin` scope, can
create or read webhooks." The only API-related permission an admin can
delegate is **Member API keys** (Settings > Administration > API > Member
API keys — whether members may create personal keys); nothing equivalent
exists for webhooks or OAuth-app management. The Dec-2025 "team owners"
role (Business/Enterprise) grants team settings/labels/templates/membership
— no webhook or API rights.

**But there is a documented non-admin side door: OAuth-application webhooks.**
Webhooks can be configured *on an OAuth app itself* (URL + resource types in
the app's settings), and then "each time a new organization authorizes the
given application, a webhook will be created for that organization" —
Linear creates the webhook as part of the authorization flow, not via the
admin-gated mutation. The recipe for John:

1. Create a free personal Linear workspace (he's admin there) and create an
   OAuth application in it — Linear itself recommends a dedicated workspace
   for app management.
2. Configure the app's webhook URL (named Cloudflare tunnel) + resource types.
3. Authorize the app into Antimetal via the normal OAuth flow as a regular
   member (`actor=user`, plain `read` scope — NOT `admin` scope, NOT
   `actor=app`; both of those require an Antimetal admin).
4. Linear auto-creates the org webhook pointed at the configured URL.

Caveats / unverified edges:
- **Third-party app approvals** is an Enterprise opt-in (Settings >
  Administration > Security). If Antimetal has it enabled, member
  authorization becomes a request-for-admin-approval flow — back to needing
  an admin once.
- The docs never state the authorizing user's role explicitly; "org
  authorizes → webhook created" with no admin caveat is strong but a live
  test (app in personal workspace → authorize into Antimetal → watch for
  events) is cheap and definitive.
- The app-webhook URL lives in the app console (John's own workspace), so he
  can re-point it himself — but manually, not via `webhookUpdate` (still
  admin-gated in Antimetal). So this path wants a **stable URL** (named
  tunnel), same as receiver-only mode.
- `admin` scope on a token still acts on behalf of the authorizing user;
  a non-admin authorizing with `admin` scope does not gain webhook CRUD.

This changes the deployment picture: **Path D (OAuth-app webhook) needs no
Antimetal admin at all** (unless app approvals are enabled), at the cost of
a one-time app setup and a static URL. The receiver half of the design is
identical; only registration differs. Note the payload arrives signed with
the app's webhook secret exactly like a workspace webhook.

## Verdict

**Feasible, with one external dependency.** The API surface is complete and
better than the issue assumed (in-place URL update kills the
dynamic-URL problem). The only hard blocker is organizational, not
technical: one-time admin involvement in the Antimetal workspace. Build the
receiver half first — it works today with a manually-registered webhook and a
named tunnel, and the auto-registration half layers on cleanly whenever an
admin credential is available.
