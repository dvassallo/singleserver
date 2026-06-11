FROM golang:1.26-alpine AS build

RUN apk add --no-cache git

WORKDIR /src

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY . .

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    set -eux; \
    mkdir -p /out/www/bin; \
    cp -R www/. /out/www/; \
    commit="$(git rev-parse HEAD 2>/dev/null || true)"; \
    build_date="$(date -u +%Y-%m-%dT%H:%M:%SZ)"; \
    ldflags="-X github.com/dvassallo/singleserver/internal/singleserver.Commit=${commit} -X github.com/dvassallo/singleserver/internal/singleserver.BuildDate=${build_date}"; \
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -buildvcs=false -ldflags "$ldflags" -o /out/www/bin/singleserver-linux-amd64 ./cmd/singleserverd; \
    CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -buildvcs=false -ldflags "$ldflags" -o /out/www/bin/singleserver-linux-arm64 ./cmd/singleserverd

FROM nginx:1.27-alpine

COPY docker/singleserver-site.conf /etc/nginx/conf.d/default.conf
COPY --from=build /out/www /usr/share/nginx/html

EXPOSE 80
