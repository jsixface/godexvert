# syntax=docker/dockerfile:1
FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build
ARG TARGETOS TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags "-s -w" -o /out/godexvert ./cmd/godexvert

FROM alpine:3.22
RUN apk add --no-cache ffmpeg && \
    adduser -D -u 1000 -h /app godexvert && \
    mkdir -p /app/data && chown -R 1000 /app
COPY --from=build /out/godexvert /usr/local/bin/godexvert
# Same layout as the Kotlin image: settings in $HOME/.godexvert.json (falling back to an existing
# .codexvert.json), database in /app/data.
ENV HOME=/app BIND=0.0.0.0 PORT=8080 DB_PATH=/app/data/godexvert.db
WORKDIR /app
USER 1000
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=3s CMD wget -qO- http://localhost:8080/healthz || exit 1
CMD ["godexvert"]
