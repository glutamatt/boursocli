# Security Policy

## Reporting a Vulnerability

If you discover a security vulnerability in this project, please report it
responsibly via **GitHub Security Advisories** (preferred) or by emailing
**thomastyzer@outlook.fr**.

**Do not** open a public issue for security vulnerabilities.

When reporting, please include:
- A clear description of the vulnerability
- Steps to reproduce the issue
- Your assessment of the potential impact

## Response Timeline

- **Acknowledgment**: within 3 business days
- **Investigation**: within 7 business days
- **Fix**: as soon as practical after confirmation

## Scope

This CLI reads data from a personal BoursoBank account. It reads the
existing session from the local Chrome profile and gets a short-lived API
bearer from the dashboard. Security-relevant areas:

- Cookie extraction: the vendored sweet-cookie (`internal/auth/sweetcookie`,
  checked against npm by `make verify-vendor`), run by node in a private
  temp folder with a minimal environment.
- Secrets at rest: only the bearer (≤24h) and the user hash, in a private
  `config.json` (file 0600, owned by the user, not a symlink; its folder
  must not be writable by others unless sticky). The Chrome cookie jars are
  never written to disk.
- Egress: `internal/client/egress.go` is the only way out. HTTPS, exact
  hosts, GET only (plus the session-refresh POST when the owner allows it),
  no dot segments or encoded bytes in paths, redirects checked hop by hop.
- Input validation: every value that goes into a URL path.
- Local files: the CLI writes only its own config; data goes to stdout.
- Build and release: actions pinned by SHA, tools by version, images by
  digest; no Homebrew cask.

## Threat model

In scope:

- A malicious or compromised npm package or registry (no npm at runtime).
- A hostile redirect, or a hostile value in an argument (for example from
  an agent under prompt injection) that tries to reach another endpoint,
  another host, or a non-GET method.
- Another local user who can write to a shared folder (`/tmp`, a mounted
  config folder).

Out of scope — the code cannot prevent it:

- A process running as the same user. It can read `config.json` and
  decrypt the Chrome cookie store itself.
- The bank API: with a valid session it also accepts orders and transfers.
  Read-only is a property of this binary, not of the bank.

## Supported Versions

Only the latest release is supported with security updates.
