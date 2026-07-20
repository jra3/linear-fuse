# Local project notes

## Issue tracking: GitHub, not Linear

This project does NOT use Linear for its own development, despite being a
Linear client. All issue tracking — backlog, bugs, wayfinder maps and their
tickets — lives in GitHub issues on `jra3/linear-fuse`. Fix PRs close issues
with `Closes #N`.

This overrides the `linear-tracker` / wayfinder default of "Linear via the
Linear MCP": for this repo, wayfinder maps are GitHub issues (label
`wayfinder:map`), tickets are issues referenced from the map, and blocking
uses body convention (`Blocked by #N`) plus native GitHub relationships where
available via `gh`.
