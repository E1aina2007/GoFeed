# syntax=docker/dockerfile:1.7
#
# Multi-stage build for backend API, worker and sweeper.
#
# Build:
#   docker build -f backend/Dockerfile -t gofeed-backend:api --target api .
#   docker build -f backend/Dockerfile -t gofeed-backend:worker --target worker .
#   docker build -f backend/Dockerfile -t gofeed-backend:sweeper --target sweeper .
#

ARG GO_VERSION=1.26.5
ARG GOPROXY=https://goproxy.cn,direct

FROM golang:${GO_VERSION} AS deps
ARG GOPROXY
ENV GOPROXY=${GOPROXY}

WORKDIR /src/backend

COPY backend/go.mod backend/go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go mod download

FROM deps AS source
COPY backend/ ./

ENV CGO_ENABLED=0

FROM source AS api-build
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go build -trimpath -ldflags="-s -w" -o /out/api ./cmd

FROM source AS worker-build
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go build -trimpath -ldflags="-s -w" -o /out/worker ./cmd/worker

FROM source AS sweeper-build
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go build -trimpath -ldflags="-s -w" -o /out/sweeper ./cmd/sweeper

FROM alpine:3.21 AS base
RUN apk add --no-cache ca-certificates tzdata wget && adduser -D -H -s /sbin/nologin app
WORKDIR /app
COPY --from=source /src/backend/configs ./configs
RUN mkdir -p ./.run/uploads && chown -R app:app /app
USER app

FROM base AS api
COPY --from=api-build /out/api /app/api
EXPOSE 8080
ENTRYPOINT ["/app/api"]

FROM base AS worker
COPY --from=worker-build /out/worker /app/worker
ENTRYPOINT ["/app/worker"]

FROM base AS sweeper
COPY --from=sweeper-build /out/sweeper /app/sweeper
ENTRYPOINT ["/app/sweeper"]
