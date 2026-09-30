# Vendored: @steipete/sweet-cookie 0.2.0

This folder is a byte-for-byte copy of the runtime files of the npm package
`@steipete/sweet-cookie@0.2.0` (MIT, see `LICENSE`). It decrypts the local
Chrome cookie store, so it runs with access to the OS keychain entry
"Chrome Safe Storage". That is why it is vendored instead of installed:

- No `npm install` at runtime: no registry, no `~/.npmrc`, no install scripts,
  no cache folder that another process could plant code in.
- The code that runs is the code that was reviewed, and it is part of the
  signed release binary (`go:embed`).

## Provenance

| Field | Value |
|---|---|
| Package | `@steipete/sweet-cookie` |
| Version | `0.2.0` (exact) |
| Tarball | `https://registry.npmjs.org/@steipete/sweet-cookie/-/sweet-cookie-0.2.0.tgz` |
| npm integrity | `sha512-R6kkmWZe36gysj0oET8v7ZQ3aZ6GxLU+ssD1BMomy53Au1Ax9LZ4g1pWE5FLgAD9nX6Y5VYjbn9kc5UZpG776Q==` |
| Runtime deps | none (`dependencies`, `optionalDependencies`, `peerDependencies` are empty) |
| Install scripts | none |

Kept: `LICENSE`, `package.json`, `dist/**/*.js`. Dropped: `README.md`,
`*.d.ts`, `*.js.map` (not needed at runtime).

## Review notes (0.2.0)

- No network code (`http`, `https`, `net`, `fetch`, …).
- No `eval` / `new Function`.
- Child processes (`secret-tool`, `kwallet-query`, `dbus-send` on Linux;
  `security` on macOS; PowerShell DPAPI on Windows) are spawned without a
  shell, with fixed arguments.
- It reads `SWEET_COOKIE_*` environment variables (for example
  `SWEET_COOKIE_CHROME_SAFE_STORAGE_PASSWORD`, `SWEET_COOKIE_BROWSERS`).
  `boursocli` starts node with a minimal, explicit environment, so these
  variables never reach it — except `SWEET_COOKIE_LINUX_KEYRING`, the
  keyring backend selector (`gnome` | `kwallet` | `basic`).
- It copies the cookie database to `os.tmpdir()`. `boursocli` points
  `TMPDIR` to a private folder that it deletes itself, even if node is killed.

## Integrity

`SHA256SUMS` lists every vendored file. `go test ./internal/auth/` fails if
the embedded files differ from it. To check this folder against the npm
registry (integrity hash + byte-for-byte diff), run:

```
make verify-vendor
```

## Updating

1. Read the diff of the new version (`npm diff` or the tarballs).
2. Update `VERSION`/`INTEGRITY` in `scripts/verify-sweetcookie.sh` and the
   table above.
3. Replace the files, regenerate `SHA256SUMS`
   (`find . -type f ! -name SHA256SUMS ! -name VENDOR.md | sed 's|^\./||' | LC_ALL=C sort | xargs sha256sum > SHA256SUMS`),
   then run `make verify-vendor` and `go test ./...`.
