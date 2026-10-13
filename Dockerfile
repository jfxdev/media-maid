# syntax=docker/dockerfile:1
# The build stage runs on the builder's platform and cross-compiles for the
# target one, so multi-arch images need no emulation for the Go build.
FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS build
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/media-maid ./cmd/media-maid

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/media-maid /usr/local/bin/media-maid
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/media-maid"]
