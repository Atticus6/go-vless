# 多架构构建零模拟：builder 跑在原生平台（$BUILDPLATFORM），靠 Go 原生交叉
# 编译出目标架构（GOOS/GOARCH），全程不走 QEMU；尾段零 RUN，alpine 段同样
# 无需模拟。QEMU 下最慢的两项（go build、upx）已分别用交叉编译与删除解决.
FROM --platform=$BUILDPLATFORM golang:1.26.7-alpine AS builder
ARG TARGETOS
ARG TARGETARCH
# release tag（dashboard 自更新比对与展示用），CI 经 build-args 传入，本地默认 dev.
ARG BUILD_VERSION=dev
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
# 尾段证书从这里拷贝（golang:alpine 自带，显式安装一次兜底，原生执行很快）.
RUN apk add --no-cache ca-certificates
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath \
  -ldflags "-s -w -X github.com/atticus6/go-vless/internal/status.buildTime=$(date -u +%Y-%m-%dT%H:%M:%SZ) -X github.com/atticus6/go-vless/internal/status.buildVersion=${BUILD_VERSION}" \
  -o server .

FROM alpine:latest
# 证书从 builder 拷贝：尾段零 RUN，多架构无需模拟.
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
WORKDIR /app
COPY --from=builder /app/server .
EXPOSE 8080
CMD ["./server"]
