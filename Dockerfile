# Build: pure-Go (modernc sqlite), so CGO off → static binary.
# Build context is the PARENT directory (the family checkout): go.mod points at
# the sibling `db-adapters` and `proto` modules via replace directives until
# they are published. `docker compose up --build` handles this; by hand:
#   docker build -f priompt/Dockerfile ..
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY db-adapters/ db-adapters/
COPY proto/ proto/
COPY priompt/ priompt/
WORKDIR /src/priompt
RUN go mod download
RUN CGO_ENABLED=0 go build -o /priompt ./cmd/priompt

FROM alpine:3.20
RUN apk add --no-cache ca-certificates           # for outbound TLS (embed API, redis)
COPY --from=build /priompt /usr/local/bin/priompt
WORKDIR /data                                     # priompt.db lives here; mount a volume
EXPOSE 8443 2112 4222
ENTRYPOINT ["priompt"]
CMD ["serve"]
