# Pinned tool versions: `@latest` would run whatever was published last.
GOLANGCI    := go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.14.0
GOVULNCHECK := go run golang.org/x/vuln/cmd/govulncheck@v1.8.0
GORELEASER  := go run github.com/goreleaser/goreleaser/v2@v2.18.2

.PHONY: build test vet fmt lint vulncheck verify-vendor sec check release-check snapshot docker

build:
	go build -o boursocli ./cmd/boursocli

test:
	go test ./... -race

vet:
	go vet ./...

fmt:
	gofmt -w internal cmd

lint:
	$(GOLANGCI) run ./...

# Dependency + stdlib CVE scan (Go team's official tool; reachability-aware:
# only flags vulnerabilities our code actually transitively calls).
vulncheck:
	$(GOVULNCHECK) ./...

# The embedded sweet-cookie == the npm tarball (integrity + byte diff).
verify-vendor:
	bash scripts/verify-sweetcookie.sh

# Static security (gosec, via golangci-lint) + known-CVE scan + vendor check.
sec: lint vulncheck verify-vendor

# goreleaser config validation (the "no remote" runtime check only passes
# once a GitHub origin exists — i.e. in CI; the config itself is valid).
release-check:
	$(GORELEASER) check || true

# Build all release artifacts locally without publishing (sanity check).
snapshot:
	$(GORELEASER) release --snapshot --clean --skip=publish

docker:
	docker build -t boursocli:dev .

# Full pre-commit gate.
check: fmt vet test lint vulncheck verify-vendor
