#!/usr/bin/env bash
# Check internal/auth/sweetcookie against the npm registry:
#   1. the tarball matches the pinned npm integrity hash (sha512);
#   2. every vendored file is byte-for-byte the file from the tarball;
#   3. the vendored folder holds no extra file;
#   4. SHA256SUMS matches the vendored files.
# Needs: bash, curl, tar, openssl, sha256sum. No npm.
set -euo pipefail

VERSION="0.2.0"
INTEGRITY="sha512-R6kkmWZe36gysj0oET8v7ZQ3aZ6GxLU+ssD1BMomy53Au1Ax9LZ4g1pWE5FLgAD9nX6Y5VYjbn9kc5UZpG776Q=="
TARBALL="https://registry.npmjs.org/@steipete/sweet-cookie/-/sweet-cookie-${VERSION}.tgz"

root="$(cd "$(dirname "$0")/.." && pwd)"
vendor="$root/internal/auth/sweetcookie"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

curl -fsSL --proto '=https' --tlsv1.2 "$TARBALL" -o "$work/pkg.tgz"
got="sha512-$(openssl dgst -sha512 -binary "$work/pkg.tgz" | openssl base64 -A)"
if [[ "$got" != "$INTEGRITY" ]]; then
	echo "FAIL: tarball integrity mismatch" >&2
	echo "  want $INTEGRITY" >&2
	echo "  got  $got" >&2
	exit 1
fi
tar -xzf "$work/pkg.tgz" -C "$work"

fail=0
while IFS= read -r -d '' f; do
	rel="${f#"$vendor"/}"
	case "$rel" in SHA256SUMS | VENDOR.md) continue ;; esac
	if ! cmp -s "$f" "$work/package/$rel"; then
		echo "FAIL: $rel differs from the npm tarball (or is not in it)" >&2
		fail=1
	fi
done < <(find "$vendor" -type f -print0)

# Every runtime file of the tarball must be vendored (no silent drop).
while IFS= read -r -d '' f; do
	rel="${f#"$work/package"/}"
	if [[ ! -f "$vendor/$rel" ]]; then
		echo "FAIL: $rel is in the tarball but not vendored" >&2
		fail=1
	fi
done < <(find "$work/package" -type f \( -name '*.js' -o -name LICENSE -o -name package.json \) -print0)

(cd "$vendor" && sha256sum --quiet -c SHA256SUMS) || fail=1

if [[ $fail -ne 0 ]]; then
	exit 1
fi
echo "OK: internal/auth/sweetcookie == @steipete/sweet-cookie@${VERSION} (${INTEGRITY:0:20}…)"
