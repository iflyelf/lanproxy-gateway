# LAN Proxy Gateway

[![Release](https://img.shields.io/github/v/release/iflyelf/lanproxy-gateway)](https://github.com/iflyelf/lanproxy-gateway/releases/latest)
[![Go](https://img.shields.io/badge/Go-1.25+-00ADD8?logo=go)](https://go.dev/)
[![License](https://img.shields.io/github/license/iflyelf/lanproxy-gateway)](LICENSE)

把一台常驻的 Debian / Ubuntu 小主机变成局域网**透明代理网关**。局域网内的手机、电视、游戏机等设备无需安装任何代理 App,只需把**网关**和 **DNS** 指向这台主机,即可复用已有的 Clash / Mihomo 等代理。

> 本项目用 Go 编写,透明代理基于 **nftables TPROXY**:
> - **不依赖 eBPF**,不需要特殊内核(区别于 dae)
> - **不使用 iptables**,只用 `nft`(与 firewalld 的 nftables 后端共存)
> - 与主机上已有的 **smartdns(53)** 和 **clash 容器(7890)** 协同工作

## 工作原理

```
局域网设备 (网关 + DNS 指向本机)
  │
  ├─ DNS 查询 (53) ───────────► smartdns   (已有,负责域名解析与分流)
  │
  └─ TCP 流量 ──► nftables TPROXY (fwmark + 策略路由)
                   │
                   ▼
              Go relay (IP_TRANSPARENT 监听)
                   │  读取原始目标 IP:port,按源 IP 统计流量
                   ▼
              HTTP CONNECT / SOCKS5 ──► clash 容器 (127.0.0.1:7890)
```

- **域名分流交给 smartdns**:设备的 DNS 指向本机 smartdns,由它完成解析与分流,网关只负责把 TCP 流量透明转发给 clash。
- **仅接管 TCP**:UDP(含 QUIC)默认直连,DNS 由 smartdns 处理。
- **不改内核、不动 iptables**:TPROXY 是主线内核标准 netfilter 功能,Ubuntu/Debian 自带。

## 功能

- 局域网透明代理(TPROXY),出口转发到 HTTP / SOCKS5 上游(如 clash)
- WebUI 管理面板,展示**在线设备使用情况**:每设备上/下行流量、活动连接、累计连接、主机名
- 最近连接记录
- **WebUI 强制用户名 + 密码登录**(bcrypt 存储,会话 cookie)
- 停止服务时自动清理 nftables 规则、策略路由并恢复 IP 转发状态
- systemd 与 Docker 两种部署方式,GitHub Actions 自动发布二进制与多架构镜像

## 快速开始

### 方式一:systemd(推荐,直接跑在宿主机)

```bash
# 1. 下载二进制(或用源码 make build)
curl -fsSL https://github.com/iflyelf/lanproxy-gateway/releases/latest/download/lanproxy-gateway-linux-amd64.tar.gz | tar xz
sudo install lanproxy-gateway-linux-amd64 /usr/local/bin/lanproxy-gateway

# 2. 生成默认配置
sudo lanproxy-gateway config init

# 3. 编辑配置(务必修改 web 用户名/密码,确认 upstream 指向 clash)
sudo vi /etc/lanproxy-gateway/gateway.yaml

# 4. 安装 systemd 服务
sudo cp deploy/systemd/lanproxy-gateway.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now lanproxy-gateway

# 5. 查看状态与接入指引
lanproxy-gateway status
```

### 方式二:Docker(host 网络 + 特权)

```bash
cd deploy/docker
cp ../../gateway.example.yaml ./gateway.yaml
vi ./gateway.yaml          # 修改 web 凭据与 upstream
docker compose up -d
```

透明网关必须使用 `network_mode: host`,并需要 `NET_ADMIN` 能力(TPROXY / 策略路由 / nftables)。

## 配置说明

`/etc/lanproxy-gateway/gateway.yaml`:

| 字段 | 说明 |
|---|---|
| `lan_interface` | 面向局域网的网卡;留空自动探测 |
| `upstream.type` | `http` 或 `socks5` |
| `upstream.address` | 上游代理地址,通常 `127.0.0.1:7890`(clash) |
| `tproxy.listen_port` | relay 的 TPROXY 监听端口(默认 12345) |
| `tproxy.fwmark` | 打给被代理流量的 fwmark(默认 1) |
| `tproxy.route_table` | 策略路由表编号(默认 100) |
| `tproxy.bypass_cidrs` | 直连网段(局域网/保留地址,不走代理) |
| `web.listen` | WebUI 监听地址,建议绑定 LAN 网段 |
| `web.username` / `web.password` | 登录凭据(明文或 `$2` 开头的 bcrypt 哈希) |
| `device.dhcp_lease_files` | DHCP 租约文件路径,用于解析设备主机名 |

## 设备接入

在每台需要走代理的设备上,手动设置静态网络(以本机 IP 为 `10.0.9.60` 为例):

| 设备设置 | 填写内容 |
|---|---|
| IP 设置 | 手动 / 静态 |
| IP 地址 | 同网段且未占用的地址;每台设备不同 |
| 子网掩码 | `255.255.255.0`(Android 前缀长度 `24`) |
| 网关 / 路由器 | 运行网关的主机 IP(如 `10.0.9.60`) |
| DNS | 运行网关的主机 IP(smartdns 提供,如 `10.0.9.60`) |
| 设备代理 | 无 / 不使用 |

保存后重连网络即可。域名分流由 smartdns 完成,代理由 clash 完成,本网关负责透明转发与流量可视化。

## 安全提示

- WebUI **默认凭据为 `admin/admin`,首次启动前务必修改**。
- 建议将 `web.listen` 绑定到 LAN 网段地址而非 `0.0.0.0`,避免暴露到不受信网络。
- relay 需要 `CAP_NET_ADMIN`;systemd 单元已用 `AmbientCapabilities` 做最小授权。

## 从源码构建

```bash
make build      # 编译当前平台二进制到 bin/
make test       # 运行单元测试
make cross      # 交叉编译 amd64 / arm64 到 dist/
```

## 许可证

[MIT](LICENSE) © iflyelf
