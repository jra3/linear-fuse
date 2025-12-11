# Contributing to linear-fuse

Thank you for your interest in contributing to linear-fuse! This document provides guidelines and instructions for contributing.

## Development Setup

### Prerequisites

- Go 1.20 or later
- FUSE support on your system
- A Linear account with API access

### Getting Started

1. Fork the repository
2. Clone your fork:
   ```bash
   git clone https://github.com/YOUR_USERNAME/linear-fuse.git
   cd linear-fuse
   ```

3. Install dependencies:
   ```bash
   go mod download
   ```

4. Build the project:
   ```bash
   make build
   # or
   go build -o linear-fuse ./cmd/linear-fuse
   ```

## Project Structure

```
linear-fuse/
├── cmd/
│   └── linear-fuse/         # Main application and CLI
│       ├── main.go
│       └── commands/        # Cobra commands
├── pkg/
│   ├── linear/              # Linear API client
│   └── fuse/                # FUSE filesystem implementation
└── internal/
    └── cache/               # Caching layer
```

## Development Workflow

### Building

```bash
make build
```

### Testing

```bash
# Run all tests
make test

# Run tests with coverage
go test -cover ./...

# Run tests for a specific package
go test -v ./internal/cache
```

### Code Quality

```bash
# Format code
make fmt

# Run go vet
make vet

# Run linter (requires golangci-lint)
make lint
```

### Running Locally

1. Get a Linear API key from https://linear.app/settings/api
2. Set up your configuration:
   ```bash
   export LINEAR_API_KEY="your-api-key"
   ```
3. Create a mount point and run:
   ```bash
   mkdir /tmp/linear
   ./linear-fuse mount --debug /tmp/linear
   ```

## Making Changes

1. Create a new branch:
   ```bash
   git checkout -b feature/your-feature-name
   ```

2. Make your changes
3. Add tests for new functionality
4. Ensure all tests pass:
   ```bash
   make check
   ```

5. Commit your changes with a descriptive message:
   ```bash
   git commit -m "Add feature: description of your changes"
   ```

6. Push to your fork:
   ```bash
   git push origin feature/your-feature-name
   ```

7. Open a Pull Request

## Pull Request Guidelines

- Write clear, descriptive commit messages
- Include tests for new features
- Update documentation as needed
- Ensure all tests pass
- Keep PRs focused on a single feature or fix
- Reference related issues in the PR description

## Code Style

- Follow standard Go conventions
- Use `go fmt` to format code
- Run `go vet` to catch common issues
- Write clear, descriptive variable and function names
- Add comments for complex logic
- Keep functions focused and small

## Testing Guidelines

- Write unit tests for new functions
- Test edge cases and error conditions
- Use table-driven tests when appropriate
- Mock external dependencies (Linear API) when possible
- Aim for good test coverage

## Documentation

- Update README.md for user-facing changes
- Add code comments for complex logic
- Update command help text when adding flags or commands
- Include examples in documentation

## Areas for Contribution

- **Features**: Implement items from the roadmap
- **Tests**: Add more test coverage
- **Documentation**: Improve docs and examples
- **Bug Fixes**: Fix reported issues
- **Performance**: Optimize caching and API calls
- **Error Handling**: Improve error messages and recovery

## Getting Help

- Open an issue for bugs or feature requests
- Use discussions for questions and ideas
- Check existing issues before creating new ones

## License

By contributing, you agree that your contributions will be licensed under the same license as the project.
