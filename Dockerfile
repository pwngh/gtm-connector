# The connector image: one static Go binary with the web app's build inside it.
# It builds with the classic builder too (DOCKER_BUILDKIT=0), which is what
# Colima with a standalone docker-compose uses. Every FROM is pinned in
# pins.json; GTM_REVISION is the commit, which `make up` passes.

FROM node:22.23.4-alpine3.24@sha256:0e1754758c2df4cfa3e68d59e2e1b7374bb7fd9ea6328eaaacf15cc934af8202 AS web
WORKDIR /web
COPY web/package.json web/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY web/ ./
# The guide links in the app open at this commit.
ARG GTM_REVISION
RUN npm run build

FROM golang:1.27.4-alpine3.24@sha256:70bd7ddea8b6cbe5d6943cde279d6b67feb10cc924fb9100d1d8757c1575b280 AS build
ARG GTM_REVISION
RUN test -n "$GTM_REVISION" || { echo "build with --build-arg GTM_REVISION=COMMIT, as make up does" >&2; exit 1; }
ENV GOTOOLCHAIN=local CGO_ENABLED=0
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ cmd/
COPY internal/ internal/
# web/embed.go compiles the build into the binary.
COPY web/*.go web/
COPY --from=web /web/dist web/dist
RUN go build -trimpath -ldflags="-s -w -X main.revision=$GTM_REVISION" -o /out/gtm-connector ./cmd/gtm-connector

FROM alpine:3.24@sha256:f59abe656b692d901e184be3445e084786602108599d2a51d229c0f5a48c71bf
ARG GTM_REVISION
LABEL org.opencontainers.image.revision=$GTM_REVISION
COPY --from=build /out/gtm-connector /app/gtm-connector
# Never root, also outside Compose (which runs it as your own user).
USER 65532:65532
ENTRYPOINT ["/app/gtm-connector"]
