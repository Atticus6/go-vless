# go-vless

[English](./README_EN.md)

[![Release](https://github.com/Atticus6/go-vless/actions/workflows/release.yml/badge.svg)](https://github.com/Atticus6/go-vless/releases)

VLESS over WebSocket 代理服务：多用户、流量统计、Argo 隧道、网页管理后台，一份配置跑满 VPS / Docker / Vercel / Railway。

## 特性

- 多用户：UUID 逗号分隔，每用户独立统计流量
- Argo 隧道：无公网 IP 也能用；VPS 直连也行
- 管理后台：隧道状态、访问地址、出口 IP、流量、内存，一页看全
- 用户管理：不用重启，在线增删用户
- 一键安装：VPS 脚本、Docker、Vercel 都支持

## 快速开始

### Docker Compose（推荐）

```bash
UUID=<你的uuid> CONFIG_KEY=<管理密钥> docker compose up -d
```

换版本：`IMAGE_TAG=0.1`；换宿主机端口：`HOST_PORT=9090`；看日志：`docker logs -f go-vless`。

### VPS 一键脚本

```bash
curl -fsSL https://raw.githubusercontent.com/Atticus6/go-vless/main/install.sh | sudo bash
```

按提示选二进制（默认）还是 Docker、开不开隧道。之后：

```bash
sudo ./install.sh update    # 升级到最新版
sudo ./install.sh uninstall # 卸载
```

### Vercel

导入仓库直接部署，然后在 dashboard 加环境变量（至少 `UUID` 和 `CONFIG_KEY`），改完 redeploy 生效。

## 配置

环境变量和启动参数等价，启动参数优先。改完配置要重启（Docker：`docker compose up -d`；脚本二进制版：`systemctl restart go-vless`）。

| 环境变量        | 启动参数        | 默认   | 说明                                         |
| --------------- | --------------- | ------ | -------------------------------------------- |
| `UUID`          | `-uuid`         | —      | 用户 UUID，**必填**，逗号分隔多用户          |
| `PORT`          | `-port`         | `8080` | 监听端口（Vercel/Railway 会自动覆盖）        |
| `TUNNEL`        | `-tunnel`       | `true` | Argo 隧道开关；Vercel 下默认关闭             |
| `TUNNEL_PROTO`  | `-tunnel-proto` | `auto` | 隧道协议：`auto` / `quic` / `http2`，连不上再换 |
| `MAX_CONN`      | `-max-conn`     | `4096` | 最大并发连接数，超了返回 503                 |
| `CONFIG_KEY`    | —               | 空     | 管理后台密钥；**不设则管理页打不开（404）**  |
| `DOMAIN`        | —               | 空     | 自绑域名，显示在管理页                       |
| `VERCEL_URL` / `NF_HOSTS` / `RAILWAY_PUBLIC_DOMAIN` | — | 空 | 平台域名/额外主机，显示在管理页 |
| `SKIP_BBR`      | —               | 空     | 填 `1` 跳过 BBR                              |

## 管理后台

浏览器打开（`CONFIG_KEY` 不对或没设会 404，这是正常的）：

```
http://<服务器IP>:<端口>/config?key=<CONFIG_KEY>
```

能看到：隧道开没开、隧道地址、可用访问地址、出口 IPv4/IPv6 通不通、公网出口 IP、**每个用户的已用流量**、内存占用、运行时间。

流量说明：`up` = 用户发出去的，`down` = 用户收到的；**重启后清零**。

### 在线增删用户

```bash
# 新增（UUID 非法会整批拒绝）
curl -X POST 'http://<IP>:<端口>/config/users/add?key=<CONFIG_KEY>' \
  -H 'Content-Type: application/json' \
  -d '{"uuids":["aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"]}'

# 删除（不存在的忽略）
curl -X POST 'http://<IP>:<端口>/config/users/remove?key=<CONFIG_KEY>' \
  -H 'Content-Type: application/json' \
  -d '{"uuids":["aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"]}'
```

注意：接口改的只在内存里生效，**重启后以环境变量里的 `UUID` 为准**；删除用户不会踢掉他正在用的连接（新连接直接拒绝）。

## 客户端怎么连

- 有隧道：地址填隧道地址（管理页 `tunnelURL`），端口 `443`，传输 `ws`，路径 `/`
- VPS 直连：地址填服务器 IP/域名，端口就是 `PORT`，路径 `/`

## 获取更新

- Docker：`docker compose pull && docker compose up -d`（`IMAGE_TAG`  pin 版本就不跟 latest）
- 一键脚本：`sudo ./install.sh update [版本号，不填最新]`
- Vercel：dashboard 点 redeploy
- 想自己编译：`go build -o server .`

## 常见问题

1. **管理页 404？** 先看 `CONFIG_KEY` 对不对；压根没设就是 404，去配上。
2. **隧道地址长时间为空？** 刚启动在建连，刷新重试；一直没有就换 `TUNNEL_PROTO=quic` 或 `http2` 试试。
3. **IPv6 连不上？** 看管理页 `ipv6` 字段，服务器本身没 IPv6 出口的话相关请求会自动快速拒绝，不会卡住。
4. **Docker 里改端口？** 改 `docker-compose.yml` 的 `ports` 映射（如 `"9090:8080"`），`up -d` 重建。
