# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added
- Initial implementation of Linear FUSE filesystem
- Read-only file system support for viewing issues
- Write support for editing issue title, description, and priority
- Issue creation by creating new markdown files
- Linear GraphQL API client with query and mutation support
- In-memory caching with TTL for API responses
- YAML frontmatter for issue metadata
- CLI with Cobra and Viper for configuration
- Multiple directory layouts:
  - Flat: All issues in root directory
  - By-state: Issues organized by workflow state
  - By-team: Issues organized by team
- Comprehensive documentation
- Unit tests for cache and markdown parsing
- Makefile for common development tasks
- Configuration file support (~/.linear-fuse.yaml)
- Environment variable support (LINEAR_API_KEY)

### Features by Phase

#### Phase 1: Foundation
- [x] Read-only mount
- [x] API client
- [x] Caching
- [x] YAML frontmatter
- [x] CLI with Cobra/Viper

#### Phase 2: Write Support
- [x] Edit frontmatter to update issues
- [x] Sync changes to Linear

#### Phase 3: Issue Creation
- [x] Create files to create new issues
- [x] Auto-assign to default team

#### Phase 4: Projects & Views
- [x] Directory structure options
- [x] Filter by state
- [x] Filter by team

#### Phase 5: Polish
- [x] Documentation
- [x] Tests
- [ ] Comprehensive error handling
- [ ] Performance optimizations

## [0.1.0] - TBD

First public release (planned)

### Known Limitations
- No support for deleting issues
- No support for comments or attachments
- No support for sub-issues
- Cache TTL is fixed at 5 minutes
- No offline mode
- New issues created in first available team only
