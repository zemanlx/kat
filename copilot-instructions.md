# GitHub Copilot Workspace Instructions

These rules guide Copilot behavior in this workspace. Copilot should prioritize clarity, testability, and your review checkpoints.

## Review Gates

- After tests are written: pause and ask for your review before running.
- After code is written and tests pass: pause and ask for your review before build/merge.
- Less code is better: prefer minimal, straightforward solutions.
- Clear over clever: avoid tricky code; choose explicit, readable implementations.

## Go General

- Always TDD: write tests first; use them to drive implementation.
- Small interfaces: keep interfaces minimal and focused.
- Make zero value useful: types should work without explicit init.
- Errors are values: return and handle errors; do not `panic`.
- Table-driven tests: prefer table-driven tests for multiple cases.
- Concurrency: prefer channels for orchestration; mutexes for serialization.
- `func main()` only calls `run()`: `run(ctx, args, getenv, stdin, stdout) error` for testability.
- Error messages do not start with "failed to" or similar; it is obvious when erroring, but add context to those errors, what were you trying to do when eror happend. Errors tell stories.

## Go HTTP

- `NewServer(deps) http.Handler`: return an `http.Handler`; accept dependencies via arguments; configure mux and middleware.
- Map routes in `routes.go`: one file lists the full API surface.
- `main` only calls `run()`: `run(ctx, args, getenv, stdin/stdout) error`.
- Handlers return `http.Handler`: `func handleX(deps) http.Handler` closure per handler.
- JSON helpers: encode/decode in one place (generic helpers) for consistent behavior.
- Middleware pattern: `func(h http.Handler) http.Handler`; list middlewares in `routes.go`.
- `sync.Once` for expensive setup: defer heavy init until first request.
- Test via `run()` end‑to‑end: perform real HTTP calls; pass `nil` for unused deps in tests.
- Expose `/healthz` or `/readyz` for readiness and health checks.

## Copilot Execution Preferences

- Honor review gates: explicitly ask for confirmation before running tests/build when tasks or commands are involved.
- Suggest minimal diffs and simple designs; avoid unnecessary abstractions.
- Default to writing tests first; if tests are missing, propose and create them before implementation.
- Use table-driven test patterns by default for Go tests.
- Favor dependency injection and pure functions for testability.
