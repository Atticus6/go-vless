# go-vless

一个使用 Go 实现的轻量级 VLESS over WebSocket 服务端，支持 TCP/UDP 转发，适合部署在云主机、容器环境或作为自建代理服务的最小实现。

当前实现聚焦于“能直接跑起来”的基础能力：

- WebSocket 入站
- VLESS 请求头解析与 UUID 校验
- TCP 转发
- UDP 转发
- `/health` 健康检查
- 优雅关闭
- Docker 构建与运行

## 项目结构

```text
.
├── Dockerfile
├── go.mod
├── go.sum
└── main.go
```

## 运行要求

- Go 1.22+
- 或 Docker

## 快速开始

### 1. 直接运行

```bash
go run . \
  -uuid 11111111-1111-1111-1111-111111111111 \
  -port 3325
```

### 2. 编译后运行

```bash
go build -o go-vless .
./go-vless -uuid 11111111-1111-1111-1111-111111111111 -port 3325
```

服务启动后默认监听：

```text
0.0.0.0:3325
```

## 配置方式

程序同时支持命令行参数和环境变量，环境变量会作为默认值，随后可被命令行参数覆盖。

### 命令行参数

```bash
go run . -h
```

```text
-port int
    Server Port (env: PORT) (default 3325)
-uuid string
    VLESS UUID (env: UUID)
```

### 环境变量

| 变量名 | 说明 | 默认值 |
| --- | --- | --- |
| `UUID` | VLESS 用户 UUID | `f0175430-1c54-412b-8183-7f7e5048e8cb` |
| `PORT` | 服务监听端口 | `3325` |

建议在生产环境中显式设置自己的 UUID，不要直接使用仓库中的默认值。

## HTTP / WebSocket 接口

### `GET /health`

健康检查接口，返回 `200 OK`。

示例：

```bash
curl http://127.0.0.1:3325/health
```

### `GET /`

WebSocket 接入点。客户端应通过 WebSocket 连接根路径 `/`，并在首个二进制消息中发送 VLESS 请求头。

如果直接通过普通 HTTP 请求访问 `/`，服务会返回：

```text
Bad Request
```

## Docker

### 构建镜像

```bash
docker build -t go-vless .
```

### 运行容器

```bash
docker run -d \
  --name go-vless \
  -p 3325:3325 \
  -e UUID=11111111-1111-1111-1111-111111111111 \
  -e PORT=3325 \
  go-vless
```

## 工作机制

服务端处理流程如下：

1. 客户端通过 WebSocket 连接 `/`
2. 服务端读取首个二进制消息
3. 解析 VLESS 版本、UUID、命令、目标地址和端口
4. 校验 UUID 是否与服务端配置一致
5. 根据命令建立 TCP 或 UDP 到目标地址的连接
6. 返回 VLESS 响应头
7. 在 WebSocket 和远端目标之间进行双向转发

## 当前支持情况

### 已支持

- VLESS version `0`
- 命令 `TCP` (`0x01`)
- 命令 `UDP` (`0x02`)
- 地址类型 IPv4 / 域名 / IPv6
- WebSocket ping/pong 保活

### 暂不支持

- VLESS `MUX` (`0x03`)
- TLS 终止
- 认证用户管理
- 配置文件
- 流量统计与监控
- 更细粒度访问控制

## 部署建议

- 将 `UUID` 替换为你自己的值
- 在反向代理或 CDN 后面使用 WebSocket 转发时，确保 Upgrade 头被正确透传
- 使用 `systemd`、Docker 或容器编排工具托管进程
- 为 `/health` 配置探活检查
- 生产环境建议配合 HTTPS/WSS 入口使用

## 示例反向代理注意事项

如果你在 Nginx、Caddy 或其他网关后部署，需要确认以下几点：

- 允许 WebSocket Upgrade
- 保留长连接
- 超时时间不要过短
- 上游指向本服务监听端口

## 已知限制

- WebSocket 接入路径固定为 `/`
- 默认允许任意来源发起 WebSocket 连接
- 首包必须是合法的 VLESS 请求头，否则连接会被关闭
- UDP 按长度前缀帧处理，不包含更复杂的增强能力

## 开发

### 本地调试

```bash
UUID=11111111-1111-1111-1111-111111111111 PORT=3325 go run .
```

### 代码格式化

```bash
gofmt -w main.go
```

## 许可证

本项目采用 MIT License，详见 [LICENSE](/home/atticus/myProject/go-vless/LICENSE)。
