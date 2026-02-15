# Build iftach binary
FROM golang:1.26 AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /iftach .

# Single runtime image: Alpine + cloudflared + iftach
FROM alpine:3.23
RUN apk add --no-cache ca-certificates wget
ARG TARGETARCH
RUN case ${TARGETARCH} in amd64) F=cloudflared-linux-amd64;; arm64) F=cloudflared-linux-arm64;; *) F=cloudflared-linux-amd64;; esac && \
  wget -q "https://github.com/cloudflare/cloudflared/releases/latest/download/$F" -O /usr/local/bin/cloudflared && \
  chmod +x /usr/local/bin/cloudflared

WORKDIR /app
COPY --from=builder /iftach .
COPY entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

EXPOSE 8080
ENTRYPOINT ["/entrypoint.sh"]
