FROM golang:1.26.7-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X github.com/atticus6/go-vless/internal/status.buildTime=$(date -u +%Y-%m-%dT%H:%M:%SZ)" -o server . \
 && apk add --no-cache upx \
 && upx --best --lzma server

FROM alpine:latest
RUN apk add --no-cache ca-certificates
WORKDIR /app
COPY --from=builder /app/server .
EXPOSE 8080
CMD ["./server"]
