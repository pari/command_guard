# syntax=docker/dockerfile:1
FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/cguard . \
 && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/cg_sessiond ./sessiond

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata \
 && adduser -D -u 10001 commandguard
WORKDIR /app
COPY --from=build /out/cguard /out/cg_sessiond /app/
COPY ui /app/ui
COPY email.tpl config.json.sample /app/
COPY docker-entrypoint.sh /app/
RUN chmod +x /app/docker-entrypoint.sh && mkdir -p /app/data && chown commandguard /app/data
USER commandguard
# All state (config.json, pem_files/, logs/, run/, mail_queue/) lives in /app/data
VOLUME /app/data
WORKDIR /app/data
EXPOSE 8080
ENTRYPOINT ["/app/docker-entrypoint.sh"]
