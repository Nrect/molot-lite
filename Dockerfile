# syntax=docker/dockerfile:1

FROM golang:1.26 AS build
WORKDIR /src

# Module download as its own cached layer: re-runs only when go.mod/go.sum change.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY . .
# Two binaries from one build stage: the server and the demo-data
# seeder (run in prod as `docker compose run --rm --entrypoint /seed app`).
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/server ./cmd/server && \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/seed ./cmd/seed

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/server /server
COPY --from=build /out/seed /seed
EXPOSE 8080
ENTRYPOINT ["/server"]
