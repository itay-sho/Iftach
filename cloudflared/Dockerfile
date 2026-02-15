FROM alpine:3.23
RUN apk add --no-cache ca-certificates wget
ARG TARGETARCH
RUN case ${TARGETARCH} in amd64) F=cloudflared-linux-amd64;; arm64) F=cloudflared-linux-arm64;; *) F=cloudflared-linux-amd64;; esac && \
  wget -q "https://github.com/cloudflare/cloudflared/releases/latest/download/$F" -O /usr/local/bin/cloudflared && \
  chmod +x /usr/local/bin/cloudflared
COPY entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh
ENTRYPOINT ["/entrypoint.sh"]
