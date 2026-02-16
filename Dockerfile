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
# 2. Use shell logic to detect arch if TARGETARCH is empty
RUN if [ -z "$TARGETARCH" ]; then \
      case $(uname -m) in \
        x86_64) TARGETARCH="amd64" ;; \
        aarch64) TARGETARCH="arm64" ;; \
        *) echo "Unsupported architecture: $(uname -m)" && exit 1 ;; \
      esac; \
    fi && \
    # 3. Use the detected or provided arch to define the filename
    case ${TARGETARCH} in \
      amd64) F="cloudflared-linux-amd64" ;; \
      arm64) F="cloudflared-linux-arm64" ;; \
      *) echo "Custom arch '${TARGETARCH}' not supported" && exit 1 ;; \
    esac && \
    # 4. Download and install
    echo "Downloading $F..." && \
    wget -q "https://github.com/cloudflare/cloudflared/releases/latest/download/$F" -O /usr/local/bin/cloudflared && \
    chmod +x /usr/local/bin/cloudflared

# Simple check to verify it runs
RUN cloudflared --version

WORKDIR /app
COPY --from=builder /iftach .
COPY entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

EXPOSE 8080
ENTRYPOINT ["/entrypoint.sh"]
