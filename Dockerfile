# Build: pure-Go (modernc sqlite), so CGO off → static binary.
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /priompt ./cmd/priompt

FROM alpine:3.20
RUN apk add --no-cache ca-certificates           # for outbound TLS (embed API, redis)
COPY --from=build /priompt /usr/local/bin/priompt
WORKDIR /data                                     # priompt.db lives here; mount a volume
EXPOSE 8443 2112 4222
ENTRYPOINT ["priompt"]
CMD ["serve"]
