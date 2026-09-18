# go-vless

[English](./README_EN.md)

[![Release](https://github.com/Atticus6/go-vless/actions/workflows/release.yml/badge.svg)](https://github.com/Atticus6/go-vless/releases)

轻量、快速、为 Serverless（免运维、用多少花多少）而生的 VLESS 代理服务端。

> VLESS 是一种代理协议：手机/电脑上的客户端连上它，就能通过服务器上网。

## 为什么选 go-vless

**轻量** —— Serverless 版本安装包约 **6MB**，跑起来只占约 **5MB** 内存，单个文件，下载就能用。

**快速** —— 启动约 **0.4s** 就能接受连接；连不上的地址秒拒绝，不会一直转圈卡住。

**Serverless 友好** —— 不用存数据，开多少个、重启都没关系；在 Vercel / Railway 上自动适配，开箱即用。

## 快速开始

### Serverless（Vercel，一键，不用买服务器）

导入仓库直接部署，在网页控制台加环境变量（至少 `UUID` 和 `CONFIG_KEY`），重新部署生效。

### Docker（一行命令）

```bash
UUID=<你的uuid> CONFIG_KEY=<管理密钥> docker compose up -d
```

锁定版本：`IMAGE_TAG=0.1`；换端口：`HOST_PORT=9090`；看日志：`docker logs -f go-vless`。

### VPS（一键脚本）

```bash
curl -fsSL https://raw.githubusercontent.com/Atticus6/go-vless/main/install.sh | sudo bash
```

按提示选直装（默认）还是 Docker、开不开隧道。之后：

```bash
sudo ./install.sh update    # 升级到最新版
sudo ./install.sh uninstall # 卸载
```

## 配置

都在环境变量里改，改完重启生效（Docker 重建一下，VPS 重启服务，Serverless 重新部署）。

| 环境变量        | 默认   | 说明                                            |
| --------------- | ------ | ----------------------------------------------- |
| `UUID`          | —      | **必填**，每个用户的钥匙，多个用逗号隔开       |
| `PORT`          | `8080` | 服务端口（Vercel/Railway 会自动改，不用管）     |
| `TUNNEL`        | 开     | 没公网 IP 就靠它连上；Vercel 下自动关           |
| `TUNNEL_PROTO`  | `auto` | 连不上隧道再换 `quic` 或 `http2` 试试           |
| `MAX_CONN`      | `4096` | 同时在线人数上限，人太多时拒绝新连接            |
| `CONFIG_KEY`    | 空     | 管理后台密码；**不设则后台打不开（404）**       |
| `REGISTER_URL` | 空 | dashboard 地址，填了才向 dashboard 注册上报 |
| `REGISTER_NODE_ID` | 空 | dashboard 分配的节点 id |
| `DOMAIN`        | 空     | 自绑的域名，显示在后台                          |
| `VERCEL_URL` / `NF_HOSTS` / `RAILWAY_PUBLIC_DOMAIN` | 空 | 平台自带的地址，显示在后台 |
| `GO_VLESS_LANG` | 空（跟随 `LANG`） | 提示信息语言：`zh` 中文 / `en` 英文（`--lang` flag 优先级更高） |

### 语言

日志、`--help`、HTTP 报错信息与 `install.sh` 交互文案均支持中英双语，
默认英文（`LANG=C` / 为空时），`LANG` 以 `zh` 开头即中文：

```bash
LANG=zh_CN.UTF-8 go run . --port 8080   # 中文日志
go run . --lang zh --port 8080          # 显式指定（优先级最高）
GO_VLESS_LANG=en ./install.sh           # 安装脚本强制英文
```

HTTP 接口（`/config/users/add` 等）的报错按请求 `Accept-Language` 协商，
JSON 字段名保持英文不变。

### 本地联调：`go run .` 注册到 dashboard

```bash
# 三元组拆开传（id/key 从 dashboard 节点页「复制安装命令」里取）
CONFIG_KEY=<config_key> \
REGISTER_URL=http://localhost:5173 \
REGISTER_NODE_ID=<节点id> \
go run . --port 8080
```

或用 flag（`CONFIG_KEY` 仍走环境变量）：

```bash
CONFIG_KEY=<config_key> go run . \
  --dashboard-url http://localhost:5173 \
  --node-id <节点id> \
  --port 8080
```

启动后向 dashboard 注册一次，成功即停，失败每 60 秒重试；三项缺任一则只启动代理，不注册。

## 管理后台

浏览器打开（密码不对或没设会 404，这是正常的）：

```
http://<服务器IP>:<端口>/config?key=<CONFIG_KEY>
```

能看到：隧道开没开、隧道地址、能用的访问地址、服务器对外显示的 IP、**每个用户用了多少流量**、内存占用、运行了多久。

流量说明：`up` = 用户发出去的，`down` = 用户收到的；**重启后清零**（Serverless 睡眠唤醒后也一样）。

### 在线增删用户（不用重启）

```bash
# 新增（有人写错则整批不通过）
curl -X POST 'http://<IP>:<端口>/config/users/add?key=<CONFIG_KEY>' \
  -H 'Content-Type: application/json' \
  -d '{"uuids":["aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"]}'

# 删除（删不存在的也没事）
curl -X POST 'http://<IP>:<端口>/config/users/remove?key=<CONFIG_KEY>' \
  -H 'Content-Type: application/json' \
  -d '{"uuids":["aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"]}'
```

注意：这里改的只在内存里，**重启后以环境变量里的 `UUID` 为准**；删除用户不会踢掉他正在用的连接（新连接直接拒绝）。

## 客户端怎么连

去后台 `tunnelURL` 那行复制隧道地址（没开隧道就填服务器 IP/域名），客户端里这样填：

- 地址：隧道地址或服务器 IP/域名
- 端口：有隧道填 `443`，直连填 `PORT`
- 传输方式：`ws`，路径：`/`
- 用户 ID：`UUID` 里配的那个

## 获取更新

- Docker：`docker compose pull && docker compose up -d`（`IMAGE_TAG` 写死版本就不跟最新）
- 一键脚本：`sudo ./install.sh update [版本号，不填就是最新]`
- Vercel：网页控制台点重新部署

## 常见问题

1. **后台打不开（404）？** 先看 `CONFIG_KEY` 对不对；压根没设就是打不开，去配上。
2. **隧道地址一直是空？** 刚启动在连，多刷几次；一直没有就换 `TUNNEL_PROTO=quic` 或 `http2`。
3. **IPv6 不通？** 看后台 `ipv6` 那项，服务器本身不支持的话会自动跳过，不会卡住。
4. **Docker 里改端口？** 改 `docker-compose.yml` 的 `ports`（如 `"9090:8080"`），`up -d` 重建。
