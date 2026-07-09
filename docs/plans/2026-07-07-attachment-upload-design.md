# Attachment file uploads through the mount

**Status: spec — not built.** Deferred intention recorded 2026-07-06
(memory: attachments-future-work). Today `attachments/` supports URL-linking
(`echo "url title" > _create`) and reading embedded/CDN bytes; you cannot put
a real file in.

## Goal

```
cp ./screenshot.png ~/am/linear/teams/ENG/issues/ENG-123/attachments/screenshot.png
```
uploads the bytes to Linear's file storage and attaches the resulting asset to
the issue. `.last` records the created attachment; failures land in `.error`.

## Linear API (verified against docs/linear-schema.graphql)

Three-step flow, standard sign-then-PUT:

1. **`fileUpload(contentType:, filename:, size:, makePublic:, metaData:)
   → UploadPayload`** (schema :22911). `UploadPayload.uploadFile: UploadFile`
   carries `uploadUrl` (pre-signed PUT target), `headers: [UploadFileHeader]`
   (must be echoed on the PUT), and `assetUrl` (the permanent URL the bytes
   will live at) (schema :45374–45429).
2. **HTTP PUT** the raw bytes to `uploadUrl` with the returned headers +
   `Content-Type`. Not GraphQL — a plain HTTP call.
3. **`attachmentCreate(input: { issueId, url: assetUrl, title })`** — the
   asset URL is a perfectly ordinary attachment URL (schema :3661); no
   body-markdown embedding required. Project through `AttachmentFields` per
   the fragment rule.

## Design

### FUSE surface: named-file `Create` on `AttachmentsNode`

Precedent: `DocsNode.Create` (documents.go:221) — a named create
(`docs/"Title.md"`) returns a `createFileNode` whose per-open buffer hands the
complete bytes to an `onFlush` closure. `AttachmentsNode` gains the same
`Create(name)`:

- Reject names colliding with the trio (`_create`, `.error`, `.last`) and
  `.link` names (those are the URL-link surface) → `EINVAL` with reason in
  `.error`.
- `onFlush(ctx, content)` runs the create tail (`commitCreate`) with a
  `mutate` closure doing: sniff `contentType` (`http.DetectContentType`,
  overridable by extension), `fileUpload` → PUT → `attachmentCreate`.
  Any step's failure classifies through `classifyMutationErr` (rate-limited
  → `EAGAIN`, etc.); a failed PUT after a successful sign is just an error —
  nothing was created, retry is safe.
- `persist`: `UpsertAttachment` (exists). `.last` projection: `WriteResult{
  URL: assetUrl, Path: <listing name>, Title: filename}`.
- `entryName`: the attachment lands in the listing as an *external*
  attachment today (`<title>.link`). See decision 2.

### Client seam

- `api.Client.UploadFile(ctx, filename, contentType string, size int,
  content []byte) (assetURL string, err error)` — owns steps 1+2 (the
  GraphQL sign via `execMutation`, then the PUT via an injectable
  `*http.Client`, the `embeddedFileCache` precedent). Two-step is hidden
  behind one method: callers can't forget the headers or the PUT.
- `MutationClient` gains `UploadFile` + reuses existing `CreateAttachment`/
  `LinkURL` shape for step 3 (check whether `attachmentCreate` with a title
  needs a new mutation constant vs. reusing `LinkURL` — `attachmentLinkURL`
  also accepts arbitrary URLs and returns the same fragment; if Linear treats
  uploads.linear.app URLs identically via linkURL, step 3 collapses into the
  existing mutation. Verify live.)
- `fileUpload` runs at **write tier** (it's a mutation) — no budget work.

### Read-your-upload

After create, seed the bytes into `embeddedFileCache` keyed by the new
attachment/asset so an immediate `cat` doesn't refetch from the CDN
(`store` + `persist` seams exist). Best-effort.

### Memory bound

`createFileNode` buffers the whole file per open handle (documents already
accept this). Uploads are bigger: cap at a `maxUploadBytes` (default 50 MiB,
config-overridable); over-cap writes fail the flush with `EFBIG` + `.error`
reason. Streaming upload (splice to PUT during Write) is out of scope — FUSE
gives no reliable "final size" before flush.

## Tests

- Unit: `UploadFile` against `httptest` (sign → PUT echo headers → assetURL),
  the mockmutation client gains `UploadFile`; onFlush tested with recording
  closures (name rejection, contentType sniff, cap).
- Integration (fixture): `cp` a small file, poll `.last`, `cat` it back.
- Live (write-tests flag): one real small upload against TST.

## Effort

Medium: new client method + mutation constant, `AttachmentsNode.Create`,
listing integration, cache seed, tests. One PR.

## Decisions to grill

1. **Listing identity of an uploaded file**: it comes back as an external
   attachment (`foo.png.link`)? That's surprising — the user wrote
   `foo.png` and should read back `foo.png` bytes. Options: (a) accept the
   `.link` rename; (b) teach `attachmentListing` that an attachment whose URL
   is an `uploads.linear.app` asset renders as a byte-file (an
   `EmbeddedFileNode`-alike) named by its filename, not a `.link`. (b) is the
   honest surface but touches the listing module. Recommend (b).
2. `makePublic`: default false (private assets require auth to fetch — the
   embedded-file reader already sends the auth header). Confirm private
   assets are fetchable via API key (the CDN reader works for issue-embedded
   files today — same domain).
3. Overwrite semantics: `cp` onto an existing uploaded name — reject
   (`EEXIST`) or new-version upload? Recommend reject in v1.
