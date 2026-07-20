#!/usr/bin/env bash
# check-safename.sh — a lightweight lint guarding the fs name/target safety
# chokepoint (safeName, see internal/fs/safename.go and issue #345).
#
# SCOPE (read this — the guard is deliberately partial). A static proof that
# every builder routes through safeName is impossible in Go. This grep covers
# only the RETURN-FORM builders: `return <expr>.<Field>` inside internal/fs where
# the returned expression is a raw remote name field (Name / Title / Label /
# Identifier / Key / DisplayName) NOT wrapped in safeName(...). Those are the
# name/dir/file builders (cycleDirName, labelFilename, …); each MUST pass through
# safeName so a hostile remote value cannot escape its directory or shadow a
# control file.
#
# It deliberately does NOT try to catch the ASSIGNMENT form (`x = ent.Name`) or
# Sprintf-interpolated SYMLINK TARGETS: builders routinely do `name := ent.Name`
# as an intermediate step *before* the final safeName, so a grep for those forms
# is ~all false positives (verified: 13 legitimate intermediate assignments).
# Those two surfaces — the by/ value lists in filter.go and every
# `fmt.Sprintf(".../%s", …)` target — are instead guarded by
# TestBuilders_HostileCorpus, which drives assigneeHandle and teamIssueTarget
# through the hostile corpus. WHEN YOU ADD a new by/-value or symlink-target
# surface, add it to that test — the grep will not catch it for you.
#
# False positives (a raw field legitimately returned for a non-path use, e.g. a
# structured TEAM-NNN identifier) are suppressed with a trailing `// safename:ok`.
#
# Exit non-zero (fails CI) if any un-wrapped, un-annotated return is found.

set -euo pipefail

cd "$(dirname "$0")/.."

# Remote string fields that, when returned as a bare `x.Field`, are candidate
# path components. Kept deliberately narrow to the fields that actually feed
# name/target builders.
FIELDS='Name|Title|Label|Identifier|Key|DisplayName'

# Match:  return <ident-chain>.<Field>   (optionally with a trailing string concat
# like `+ ".md"`), i.e. a bare field return with no safeName( on the line.
# We only scan non-test .go files under internal/fs.
matches="$(
  grep -rnE "return [A-Za-z_][A-Za-z0-9_.]*\.($FIELDS)\b" internal/fs \
    --include='*.go' \
  | grep -v '_test.go:' \
  | grep -v 'safeName(' \
  | grep -v 'safename:ok' \
  || true
)"

if [ -n "$matches" ]; then
  echo "check-safename: found name/target builder(s) returning a raw remote name"
  echo "field without routing through safeName(). Wrap the return in safeName(raw,"
  echo "id) — or, if the value is not a path component, annotate the line with a"
  echo "trailing '// safename:ok'."
  echo
  echo "$matches"
  exit 1
fi

echo "check-safename: OK (no un-wrapped raw-name returns in internal/fs)"
