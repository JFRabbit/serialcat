# Contributing to serialcat

Thanks for taking the time to contribute! This project is an internal tool that has
been open-sourced, so the bar is intentionally light — but please read this first.

## Code of Conduct

All participants are expected to follow our [Code of Conduct](CODE_OF_CONDUCT.md).
Please report unacceptable behavior by filing an issue on GitHub.

## Reporting issues

Before opening an issue, search the existing issues to avoid duplicates.

### Bug reports

A good bug report helps us fix things fast. Please include:

- The serialcat version (`serialcat version --short`) and the OS/platform.
- The exact command you ran and the config file (or a minimal reproducer).
- What you expected to happen vs. what actually happened.
- Any relevant log lines from `logs/serialcat.log` or the console output.

### Feature requests

Describe the problem you are trying to solve and how you would use the feature.
Keep in mind the project deliberately does **not** build LLM nodes, alerting,
analysis/rule engines, charts, or a web UI — proposals outside that boundary are
welcome, but scope creep will be pushed back.

### Security vulnerabilities

Do **not** open a public issue for security problems. See [SECURITY.md](SECURITY.md).

## Submitting changes

1. Fork the repository and create a topic branch from `main`:
   `git checkout -b feat/my-change`.
2. Make your changes, following the existing code style and structure
   (thin `cmd/` layer, logic in `internal/` or reusable libs in `pkg/`).
3. Add or update tests where behavior changes.
4. Run the full test suite locally:

   ```bash
   go build ./... && go test ./...
   ```

   The project targets Go 1.26.5 (see `go.mod`).
5. Commit with a clear message (see below), push, and open a pull request
   against `main`. Describe what the change does and why, and reference any
   related issue.

### Commit messages

Use the [Conventional Commits](https://www.conventionalcommits.org/) style so
history stays readable:

```
<type>(<scope>): <short summary>
```

Examples:

- `feat(send): add --raw flag for exact byte sending`
- `fix(agent): reconnect after serial port dropout`
- `docs(readme): document the GitHub Actions release flow`
- `test(dag): cover fan-out backpressure`

Common types: `feat`, `fix`, `docs`, `test`, `refactor`, `perf`, `chore`.

## Building and releasing

Building locally for development:

```bash
go build -o serialcat .
```

Releases are cut from Git tags (`v*`) by GitHub Actions; see the "CI / Release"
section of the [README](README.md) for the `ldflags` used to inject the version.

## Project boundaries

- `plugins/`, `scripts/`, `examples/` and `raw/` are **not** part of the published
  repository — keep engine code self-contained.
- The single source of truth for configuration is the Go struct in `pkg/config`.
  Do not introduce a parallel config source.
- Keep `pkg/` free of imports from `internal/` (Go rules); shared drivers such as
  the serial layer live in `pkg/`.

Again — thank you for contributing!
