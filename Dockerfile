# syntax=docker/dockerfile:1
#
# Multi-stage build for the suture-server binary.
# Final image is ~15MB (scratch + static binary).

FROM golang:1.22-alpine AS build

WORKDIR /src

# Copy module manifests first for cache friendliness.
COPY go.mod go.sum ./
RUN go mod download

# Then the rest of the source.
COPY . .

# Build a static binary.
RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags='-s -w -extldflags "-static"' \
    -o /out/suture-server ./cmd/suture-server

# --- runtime stage ---
FROM gcr.io/distroless/static:nonroot

COPY --from=build /out/suture-server /suture-server

EXPOSE 8080

# Healthcheck for platforms that respect it (Cloud Run does).
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
    CMD ["/suture-server", "-version"]

USER nonroot:nonroot
ENTRYPOINT ["/suture-server"]
