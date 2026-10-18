# Build: pure-Go (modernc sqlite), so CGO off → static binary.
# Build context is this repo; the sibling modules (proto, db-adapters, auth)
# come from the Go module proxy at the versions pinned in go.mod.
#   docker build -t priompt .
# Multi-arch: the Go stage runs natively on the build host and cross-compiles
# (no QEMU for the compile); only the tiny runtime stage is per-platform.
FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS build
ARG TARGETOS TARGETARCH
ARG VERSION=dev
ARG COMMIT=unknown
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath \
      -ldflags "-s -w -X main.version=${VERSION} -X main.commit=${COMMIT}" \
      -o /priompt ./cmd/priompt

FROM alpine:3.20
RUN apk add --no-cache ca-certificates           # for outbound TLS (embed API, redis)
COPY --from=build /priompt /usr/local/bin/priompt
# Run unprivileged. /data is owned by that user so a fresh named volume mounted
# there inherits the ownership and is writable. A volume written by an older
# image (which ran as root) needs a one-time
#   docker run --rm -v <volume>:/data alpine chown -R 10001:10001 /data
RUN addgroup -S -g 10001 priompt && adduser -S -D -H -u 10001 -G priompt priompt \
 && mkdir /data && chown priompt:priompt /data
USER priompt
# priompt.db lives here (the default -db is relative); mount a volume.
# Dockerfile has no trailing comments: keep comments on their own lines.
WORKDIR /data
EXPOSE 8443 2112 4222
ENTRYPOINT ["priompt"]
CMD ["serve"]
