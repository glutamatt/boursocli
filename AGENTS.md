# Repository Guidelines — boursocli

Agent-first Go CLI for a personal BoursoBank account. Read-only (this
hardened fork): no write to the bank, by design.

## Project Structure

- `cmd/boursocli/`: entrypoint (`main.go`), `signal.NotifyContext`.
- `internal/cli/`: Cobra commands (1 file = 1 command), `session()` orchestration.
- `internal/auth/`: Chrome cookie extraction (embedded `load.mjs` + vendored
  sweet-cookie in `sweetcookie/`, bi-domain).
- `internal/client/`: audited HTTP transport, Bearer + Cookie data planes;
  `egress.go` = the only way out (hosts, HTTPS, GET-only, path checks).
- `internal/config/`: `config.json` (bearer only, private folder/file,
  atomic writes, redacted output).
- `internal/out/`: agent-first output (JSON stdout, logs stderr, table).
- `internal/htmlx/`: strict HTML parsing (schema drift = loud error).
- `internal/version/`: build metadata (ldflags-injectable).
- Tests: `*_test.go` alongside code.

## Build, Test, Dev

```
make build   # go build ./...
make test    # go test ./... -race
make lint    # golangci-lint (incl. gosec)
make check   # fmt vet test lint vulncheck verify-vendor — full pre-commit gate
```

- Conventional Commits (`feat:`, `fix:`, `docs:`, `chore:`).
- User-facing strings in **French**; command names, flags, identifiers,
  and code comments in **English** (Go convention).

## Safety

- Read-only. Do not add any non-GET request, host, or write command. The
  egress guard (`internal/client/egress.go`) must stay the only way out.
- Every value that goes into a URL path is validated (`internal/cli/validate.go`).
- Never write the Chrome cookie jars to disk. Never add a runtime package
  install: update the vendored sweet-cookie as `sweetcookie/VENDOR.md` says.
- Personal account: serialize requests at human pace. On throttle, back
  off — never re-authenticate in a loop.
- Never commit secrets (cookies, tokens, IBANs, account keys).

## Status

21 read commands built and validated on a live account (2026-05-20).
Production tooling in place: CI, golangci-lint+gosec, govulncheck,
vendor verification, goreleaser (6 platforms), Dockerfile, unit tests with
per-package coverage floors. Hardened for read-only agent use (fork
branch `hardening`); no write command, by design.
