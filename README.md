# ccore-ipv6

独立的 Linux IPv6 地址池管理工具，暂不依赖或修改 c-core。

当前提供：

- 通过 netlink 枚举 IPv6 网卡和地址
- 在指定 IPv6 前缀中生成随机地址
- 批量添加和删除地址
- 切换 IPv6 默认路由的源地址，并在状态文件中恢复
- 使用原子 JSON 状态文件记录本工具管理的地址
- 使用跨进程状态锁，避免并发命令覆盖状态
- 服务启动时自动幂等恢复已记录地址和默认路由
- 使用 Cloudflare API Token 将状态里的地址同步为 AAAA 记录
- Cloudflare 临时失败和限流会自动退避重试
- 通过 Unix socket 提供本地状态查询和幂等恢复 API
- 从每个托管地址发起绑定源地址的 TCP/HTTPS 出站可用性检测

当前版本不负责代理协议或 C-Panel。Cloudflare 凭据只从环境变量读取，不写入状态文件。

## 构建

```bash
go build -trimpath -ldflags "-s -w" -o ccore-ipv6 ./cmd/ccore-ipv6
```

Linux VPS 上可以直接使用项目目录中的安装脚本：

```bash
./install.sh install
./check.sh
```

`install.sh update` 会替换二进制并重启 unit；`uninstall` 保留状态目录，`purge` 才会删除状态。

## Web 面板

服务在同一个 Unix Socket 上提供嵌入式面板和 JSON API，不新增 TCP 或公网监听。通过 SSH Unix-socket 转发访问：

```bash
ssh -N -L 127.0.0.1:18765:/run/ccore-ipv6/ccore-ipv6.sock root@<vps-address>
```

保持 SSH 命令运行，然后打开 `http://127.0.0.1:18765`。面板支持查看接口/地址/默认路由、从网卡全局前缀生成并添加地址、删除本工具托管的地址、恢复托管地址。添加操作会等待 DAD；删除接口只接受状态文件登记的地址。DNS 同步和默认路由源地址修改仍通过 CLI 完成。

程序需要 Linux 的 `CAP_NET_ADMIN` 才能增删地址。建议使用专用 systemd unit 授予该能力，不要直接以 root 运行长期公网管理接口。

## 用法

```bash
ccore-ipv6 list
ccore-ipv6 generate --prefix 2001:db8:1::/64 --count 8
ccore-ipv6 add --iface eth0 --prefix 2001:db8:1::/64 --count 8
ccore-ipv6 remove --iface eth0 2001:db8:1::1234/64
ccore-ipv6 route-source --iface eth0 --source 2001:db8:1::1234
ccore-ipv6 restore
ccore-ipv6 serve --socket /run/ccore-ipv6/ccore-ipv6.sock
CCORE_IPV6_CF_TOKEN='***' ccore-ipv6 dns-zones
CCORE_IPV6_CF_TOKEN='***' ccore-ipv6 dns-sync \
  --zone-id <zone-id> --zone-name example.com --name 'node-{n}'
```

默认状态文件为 `/var/lib/ccore-ipv6/state.json`，可用 `--state` 覆盖。

`dns-sync` 支持 `{n}`（从 1 开始）、`{i}`（从 0 开始）和补零形式，例如 `node-{n:3}`。不含占位符时，所有地址会使用同一个记录名，形成多值 AAAA 记录。

Cloudflare 认证只接受最小权限 API Token，环境变量为 `CCORE_IPV6_CF_TOKEN`。Token 只需要目标 Zone 的 DNS Edit 权限。本地模拟 API 可通过 `CCORE_IPV6_CF_ENDPOINT` 覆盖默认 API 地址。

状态文件只包含地址和路由元数据，不保存凭据，因此使用 `0644` 让专用 systemd 用户可以读取由 root CLI 写入的状态；状态锁仍保持 `0600`。

Cloudflare 同步只创建缺少的 AAAA 记录，不删除已有记录；这样可以避免误删 Zone 中由其他系统管理的解析。`check.sh` 只做本机能力和状态预检，不代表公网 IPv6 入站或 DNS 生效已经验证。

批量创建记录之间默认间隔 100ms，遇到 429 或 5xx 会自动退避重试；同步过程可通过取消命令上下文中止。

## 本地 API 和 systemd

`serve` 只监听 Unix socket，默认权限为 `0600`，没有 TCP 监听和公网管理面：

- `GET /healthz`
- `GET /v1/interfaces`
- `GET /v1/state`
- `POST /v1/addresses`
- `DELETE /v1/addresses`
- `POST /v1/probes`（对所有托管地址做一次性出站探测，不写入状态文件）
- `POST /v1/restore`

可直接参考 [systemd/ccore-ipv6.service](systemd/ccore-ipv6.service)。服务启动前会等待 `network-online.target` 并恢复状态文件中的地址和默认路由；恢复失败时服务启动失败，systemd 会按重启策略重试。unit 只授予 `CAP_NET_ADMIN`，不以 root 运行。

```bash
useradd --system --no-create-home --shell /usr/sbin/nologin ccore-ipv6
install -d -o ccore-ipv6 -g ccore-ipv6 /var/lib/ccore-ipv6
install -m 755 ccore-ipv6 /usr/local/bin/ccore-ipv6
install -m 644 systemd/ccore-ipv6.service /etc/systemd/system/ccore-ipv6.service
systemctl daemon-reload
systemctl enable --now ccore-ipv6
```
