#!/usr/bin/env bash
# check-safename.sh — the "no bypass" grep-rule guarding the fs name/target
# safety chokepoint (safeName, see internal/fs/safename.go and issue #345).
#
# A static proof that every builder routes through safeName is impossible in Go,
# so this is a lightweight lint: it flags any `return <expr>.<Field>` inside
# internal/fs where the returned expression is a raw remote name field (Name /
# Title / Label / Identifier / Key / DisplayName) NOT wrapped in safeName(...).
# Those are exactly the strings that become directory names, filenames, or
# symlink-target components; each MUST pass through safeName so a hostile remote
# value cannot escape its directory or shadow a control file.
#
# False positives (a raw field legitimately returned for a non-path use) are
# suppressed with a trailing `// safename:ok` comment on the line.
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
