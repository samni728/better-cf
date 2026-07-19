FROM golang:1.22-alpine AS builder

WORKDIR /src

COPY go.mod ./
COPY main.go ./
COPY cmd ./cmd
COPY internal ./internal
COPY database ./database

RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/better-cloudflare-ip ./main.go \
    && CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/cf-betterip-web ./cmd/cf-betterip-web

FROM alpine:3.20

RUN apk add --no-cache ca-certificates tzdata

WORKDIR /app

COPY --from=builder /out/better-cloudflare-ip /app/better-cloudflare-ip
COPY --from=builder /out/cf-betterip-web /app/cf-betterip-web
COPY database /app/database

ENV LISTEN_ADDR=0.0.0.0:18080 \
    DATA_DIR=/app/data \
    SCANNER_BIN=/app/better-cloudflare-ip \
    BETTER_CF_DATA_DIR=/app/data \
    BETTER_CF_RUN_TIMEOUT_HOURS=3 \
    BETTER_CF_FAMILY_TIMEOUT_MINUTES=30 \
    BETTER_CF_LOCATION_PREFER_MINUTES=10 \
    TZ=Asia/Shanghai

RUN mkdir -p /app/data

EXPOSE 18080

VOLUME ["/app/data"]

CMD ["/app/cf-betterip-web"]
