# syntax=docker/dockerfile:1
# The build stages run on the builder's platform: the web UI is the same for
# every target, and the Go build cross-compiles, so multi-arch images need no
# emulation.
FROM --platform=$BUILDPLATFORM node:22-alpine AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci --ignore-scripts --no-audit --no-fund
COPY web/ ./
# Writes the UI into /src/internal/ui/dist, embedded by the Go build.
RUN mkdir -p /src/internal/ui/dist && npm run build

FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS build
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /src/internal/ui/dist ./internal/ui/dist
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/media-maid ./cmd/media-maid

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/media-maid /usr/local/bin/media-maid
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/media-maid"]
