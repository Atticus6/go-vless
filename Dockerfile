# ---------- 构建阶段 ----------
FROM golang:1.25-alpine AS builder

# 安装必要工具（证书 + git）
RUN apk add --no-cache ca-certificates git

WORKDIR /app

# 先拷贝依赖文件（利用缓存）
COPY go.mod go.sum ./
RUN go mod download

# 再拷贝源码
COPY . .

# 构建（静态编译，平台由 BuildKit 自动注入，默认 amd64）
ARG TARGETOS=linux
ARG TARGETARCH=amd64
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -ldflags="-s -w" -o server .


# ---------- 运行阶段 ----------
FROM alpine:3.20

# 安装 ca-certificates（https 请求必须）
RUN apk add --no-cache ca-certificates

# 创建非 root 用户（更安全）
RUN adduser -D -g '' appuser

WORKDIR /app

# 拷贝二进制
COPY --from=builder /app/server .

# 权限
RUN chown appuser:appuser /app/server
USER appuser

EXPOSE 3325

# 健康检查（可选）
HEALTHCHECK --interval=30s --timeout=3s \
  CMD wget -qO- http://localhost:3325/health || exit 1

CMD ["./server"]