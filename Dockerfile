# syntax=docker/dockerfile:1
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/media-maid ./cmd/media-maid

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/media-maid /usr/local/bin/media-maid
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/media-maid"]
