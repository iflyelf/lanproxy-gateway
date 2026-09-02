# LAN Proxy Gateway

[![Release](https://img.shields.io/github/v/release/iflyelf/lanproxy-gateway)](https://github.com/iflyelf/lanproxy-gateway/releases/latest)
[![Go](https://img.shields.io/badge/Go-1.26+-00ADD8?logo=go)](https://go.dev/)
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
- **支持 IPv4 / IPv6**:开启 `tproxy.enable_ipv6` 后同时接管 IPv6 TCP 流量(IPv6 策略路由 + `ip6` TPROXY)。
- **代理失败自动回退直连**:`fallback_direct` 开启时,clash 失败自动直连目标保障上网,恢复后新连接自动回到代理。
- **1~2 万并发优化**:分片锁、缓冲池复用、半关闭处理、连接数背压,支持家庭到小型企业场景。
- **实时流量可视化**:四主题 H5 WebUI,流量趋势曲线 + 力导向拓扑图,自适应移动端。

## 功能

- 局域网透明代理(TPROXY),出口转发到 HTTP / SOCKS5 上游(如 clash)
- WebUI 管理面板,展示**在线设备使用情况**:每设备上/下行流量、活动连接、累计连接、主机名
- 最近连接记录
- **WebUI 强制用户名 + 密码登录**(bcrypt 存储,会话 cookie)
- 停止服务时自动清理 nftables 规则、策略路由并恢复 IP 转发状态
- systemd 与 Docker 两种部署方式,GitHub Actions 自动发布二进制与多架构镜像

## 快速开始

> 下载均通过 `https://down.xiaonuo.live?url=` 代理加速,`wget` 支持断点续传(`-c`)。
> 按需把 `amd64` 换成 `arm64`。

### 方式一:systemd(推荐,直接跑在宿主机)

```bash
# 1. 下载二进制到 /tmp 并解压
wget -q -c --no-check-certificate -O /tmp/lanproxy-gateway.tar.gz \
  "https://down.xiaonuo.live?url=https://github.com/iflyelf/lanproxy-gateway/releases/latest/download/lanproxy-gateway-linux-amd64.tar.gz"
tar -xzf /tmp/lanproxy-gateway.tar.gz -C /tmp

# 2. 替换二进制(mv -f 原子覆盖)
mv -f /tmp/lanproxy-gateway-linux-amd64 /usr/local/bin/lanproxy-gateway
chmod +x /usr/local/bin/lanproxy-gateway

# 3. 生成默认配置
lanproxy-gateway config init

# 4. 编辑配置(务必修改 web 用户名/密码,确认 upstream 指向 clash)
vi /etc/lanproxy-gateway/gateway.yaml

# 5. 下载 systemd 单元到 /tmp 再替换
wget -q -c --no-check-certificate -O /tmp/lanproxy-gateway.service \
  "https://down.xiaonuo.live?url=https://raw.githubusercontent.com/iflyelf/lanproxy-gateway/main/deploy/systemd/lanproxy-gateway.service"
mv -f /tmp/lanproxy-gateway.service /etc/systemd/system/lanproxy-gateway.service

# 6. 启动服务
systemctl daemon-reload
systemctl enable --now lanproxy-gateway

# 7. 查看状态与接入指引
lanproxy-gateway status
```

### 方式二:Docker(host 网络 + 特权)

```bash
# 1. 创建工作目录
mkdir -p lanproxy-gateway && cd lanproxy-gateway

# 2. 下载 docker-compose.yml 与配置模板到 /tmp 再替换
wget -q -c --no-check-certificate -O /tmp/docker-compose.yml \
  "https://down.xiaonuo.live?url=https://raw.githubusercontent.com/iflyelf/lanproxy-gateway/main/deploy/docker/docker-compose.yml"
mv -f /tmp/docker-compose.yml ./docker-compose.yml

wget -q -c --no-check-certificate -O /tmp/gateway.yaml \
  "https://down.xiaonuo.live?url=https://raw.githubusercontent.com/iflyelf/lanproxy-gateway/main/gateway.example.yaml"
mv -f /tmp/gateway.yaml ./gateway.yaml

# 3. 修改 web 凭据与 upstream
vi ./gateway.yaml

# 4. 启动
docker compose up -d

# 5. 查看日志(已持久化到宿主机 ./logs)
docker compose logs -f          # 容器 stdout
tail -f ./logs/gateway.log      # 文件日志(按天切割)
```

透明网关必须使用 `network_mode: host`,并需要 `NET_ADMIN` 能力(TPROXY / 策略路由 / nftables)。

> 日志目录 `./logs` 已通过卷挂载到容器的 `/var/log/lanproxy-gateway`,重建容器不丢日志。
> Docker 容器可写层默认可写,不会出现 systemd `ProtectSystem=strict` 那种只读问题;
> 只有当你手动给容器加了 `read_only: true` 时才需要额外声明可写路径。

## 配置说明

`/etc/lanproxy-gateway/gateway.yaml`:

| 字段 | 说明 |
|---|---|
| `lan_interface` | 面向局域网的网卡;留空自动探测 |
| `upstream.type` | `http` 或 `socks5` |
| `upstream.address` | 上游代理地址,通常 `127.0.0.1:7890`(clash) |
| `upstream.username` / `upstream.password` | 上游代理认证凭据,留空则不认证 |
| `tproxy.listen_port` | relay 的 TPROXY 监听端口(默认 12345) |
| `tproxy.fwmark` | 打给被代理流量的 fwmark(默认 1) |
| `tproxy.route_table` | 策略路由表编号(默认 100) |
| `tproxy.bypass_cidrs` | 直连网段 IPv4(局域网/保留地址,不走代理) |
| `tproxy.bypass_cidrs6` | 直连网段 IPv6(不走代理) |
| `tproxy.tcp_only` | 是否仅代理 TCP 流量,不接管 UDP(默认 `true`) |
| `tproxy.enable_ipv6` | 是否同时接管 IPv6 TCP 流量(默认 `false`) |
| `tproxy.fallback_direct` | 代理失败时自动回退直连(默认 `true`) |
| `tproxy.block_quic` | 阻断 UDP/443 强制浏览器用 TCP(消除 YouTube 首次加载延迟,默认 `false`) |
| `tproxy.max_connections` | 并发连接数上限,0=不限(默认 0,高负载场景可设 18000) |
| `web.listen` | WebUI 监听地址,建议绑定 LAN 网段 |
| `web.username` / `web.password` | 登录凭据(明文或 `$2` 开头的 bcrypt 哈希) |
| `web.session_secret` | 会话签名密钥,留空则每次启动随机生成(会导致重启后需重新登录) |
| `device.scan_interval_seconds` | 设备扫描间隔(秒),用于更新在线设备列表(默认 10) |
| `device.dhcp_lease_files` | DHCP 租约文件路径,用于解析设备主机名 |
| `device.remark_file` | 设备备注持久化文件路径(JSON 格式),留空则不保存备注 |
| `log.path` | 日志文件路径,留空则仅控制台;按天切割为 `gateway-YYYY-MM-DD.log` |
| `log.level` | 日志级别 `debug`/`info`/`warn`/`error`(默认 `info`) |
| `log.max_age_days` | 日志保留天数,过期自动清理(默认 `7`,`0`=不清理) |
| `log.console` | 是否同时输出到控制台(默认 `true`) |

## 日志

日志支持按天切割与自动清理:

- **文件路径**:`log.path` 指定基础路径(如 `/var/log/lanproxy-gateway/gateway.log`),实际写入带日期后缀的文件 `gateway-2026-09-02.log`,并维护软链 `gateway.log` 指向当天文件(便于 `tail -f`)。
- **按天切割**:跨天时自动切换到新日期文件,无需重启。
- **自动清理**:每次切割时删除超过 `max_age_days` 天的旧日志(默认保留 7 天)。
- **级别控制**:低于 `level` 的日志不输出;`debug` 最详细,`error` 最少。
- **双输出**:`console: true` 时同时写文件与标准输出(systemd 下可用 `journalctl` 查看)。

```bash
# 实时查看日志
tail -f /var/log/lanproxy-gateway/gateway.log
# 或通过 journal(console=true 时)
journalctl -u lanproxy-gateway -f
```

也可直接在 WebUI 的「日志」页在线查看当天日志尾部(支持级别筛选与关键字过滤)。

## 规则清理

正常停止时(SIGTERM/SIGINT,含 `systemctl stop`、`docker stop`、Ctrl+C)会**自动清理** nftables 表与策略路由,恢复系统状态。已实测验证:

- systemd:`systemctl stop` 清理干净
- 容器:`docker stop` / `docker restart` 清理干净且重启幂等

**例外**:进程被 `SIGKILL`(如 `kill -9`、`docker kill`、OOM)强杀时无法执行清理,会残留规则。此时用 `clean` 命令手动清理:

```bash
lanproxy-gateway clean -c /etc/lanproxy-gateway/gateway.yaml
```

`clean` 会删除本程序的 nft 表(`inet lanproxy_gw`)与对应 fwmark/路由表规则,幂等可重复执行。容器场景建议在 compose 中设置 `stop_grace_period: 30s` 以预留充足清理时间(仓库示例已配置)。

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

## WebUI 功能

登录后 WebUI 分五个页面:

| 页面 | 内容 |
|---|---|
| **网络总览** | 总流量/活动连接/设备数指标卡片、实时流量趋势曲线(5 分钟)、力导向流量拓扑(节点大小=连接数,连线粗细=流量,可拖拽) |
| **设备与服务** | 设备流量排行:每设备上/下行、活动/累计连接、主机名、最近活动;支持为设备编辑自定义备注(持久化到 `device.remark_file`) |
| **访问记录** | 最近连接记录,可按出口(代理/直连/失败)与源 IP 筛选 |
| **日志** | 在线查看当天日志尾部,支持级别筛选、关键字过滤、行数选择、自动刷新(5s),方便远程排查 |
| **设置** | 代理出口与本地公网 IP 检测(代理出口 IP 通过 CloudFlare CDN 检测,本地公网 IP 通过墙外 API 直连检测)、外观主题切换、系统配置一览 |

**四套主题**:暖沙米(默认)、经典浅色、石墨深色、海雾蓝。首次访问按系统深色偏好自动选择(暗→石墨深色 / 亮→暖沙米),手动切换后本地记忆。全部页面 H5 自适应,窄屏 Tab 转顶部横向滚动,表格横向滚动。

## 回退直连与 QUIC 阻断

- **回退直连**(`fallback_direct`,默认开):每条新连接优先走代理;clash 拨号失败时自动改为直连目标,保证上网不中断;clash 恢复后新连接自动回到代理。**无学习记忆**,始终优先代理。
- **QUIC 阻断**(`block_quic`,默认关):开启后 `nft` 追加 `udp dport 443 reject`(IPv4+IPv6),强制浏览器从 QUIC 回退到 TCP 走代理,消除 YouTube 等首次加载延迟。副作用:拒绝所有设备 UDP/443,可能影响部分 P2P/WebRTC 应用,按需开启。

## 高并发(1~2 万连接)

本项目已针对 1~2 万并发连接做加固:统计层分片锁(64 桶)降低锁竞争、`sync.Pool` 缓冲池复用、TCP 半关闭正确处理、HTTP CONNECT 用 `http.ReadResponse` 严格解析、可选 `max_connections` 背压防雪崩。

达到 1~2 万并发需宿主机系统调优(本项目不自动修改 sysctl):

```bash
# 扩大本地端口范围(relay→clash 单目标,受源端口数限制)
sysctl -w net.ipv4.ip_local_port_range="1024 65535"
# 提升连接跟踪表与文件描述符上限
sysctl -w net.netfilter.nf_conntrack_max=262144
sysctl -w fs.file-max=1048576
sysctl -w net.core.somaxconn=32768
sysctl -w net.ipv4.tcp_tw_reuse=1
```

> 真实并发上限最终取决于上游 clash 单实例能力;本网关仅保证自身不成为瓶颈。

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
