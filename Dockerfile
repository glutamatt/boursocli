# Multi-stage: build the static Go binary, ship it on a Node base (the
# chromecookies auth helper runs the embedded sweet-cookie with `node`; no
# npm). Base images are pinned by digest: a re-pushed tag cannot change them.
#
# ⚠️ AUTH LIMITATION (documented, not a bug): chromecookies decrypts the
# *host* Chrome cookie store via the host OS keychain. Inside a container
# there is no macOS Keychain / login keyring, so the auto-auth path will NOT
# work for a macOS host. Practical container use:
#   - mount a valid config.json: it holds only the bearer (≤24h), so the
#     Bearer-plane commands (accounts, operations, …) work until it expires:
#       docker run --rm -v "$HOME/Library/Application Support/boursocli:/cfg" \
#         boursocli --config /cfg/config.json accounts
#     The folder must stay private (0700, owned by the container user).
#   - cookie-plane commands (positions, export, …) read Chrome each time:
#     mount a Linux Chrome profile and use --chrome-profile.
# No secret is baked into the image; credentials are always mounted at runtime.

FROM golang:1.25.10@sha256:c138bff780910acf4254ab3a6f7ff0f64bbd841f27bd82bfa986fe122c109538 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=docker
ARG COMMIT=none
ARG DATE=unknown
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags "-s -w \
      -X github.com/thomasmarcelin754/boursocli/internal/version.Version=${VERSION} \
      -X github.com/thomasmarcelin754/boursocli/internal/version.Commit=${COMMIT} \
      -X github.com/thomasmarcelin754/boursocli/internal/version.Date=${DATE}" \
    -o /out/boursocli ./cmd/boursocli

FROM node:22-slim@sha256:43ac6c60b8f89723f746e8a92ce91abd5017e627ce1ddfe4238355d3a30b772c
RUN useradd -m app
COPY --from=build /out/boursocli /usr/local/bin/boursocli
USER app
WORKDIR /home/app
ENTRYPOINT ["boursocli"]
CMD ["--help"]
