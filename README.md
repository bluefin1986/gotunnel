# gotunnel

反向隧道工具，将内网机器的端口映射到公网服务器。

## 架构

```
访问者 → gotunnel-server (公网) → 隧道 → gotunnel-client (内网) → 本地服务
```

- **Server**: 运行在有公网 IP 的机器上，监听 control 端口接收 client 注册
- **Client**: 运行在内网机器上，连接 server 并注册隧道，将本地端口暴露出去

## 协议

Client 注册时发送：`&sp:<tunnelName>:<visitorPort>\n`
Server 回复：`&00:<tunnelName>\n` (成功) 或 `&01:<reason>\n` (失败)

## 部署

### Server (Mac mini / 公网机器)

```bash
cd /Users/niuma/Workspace/gotunnel

# 配置
cp .env.example .env
# 编辑 .env 设置端口

# 构建镜像
docker compose -f docker-compose.build.yml build

# 启动
docker compose up -d
```

### Client (公司 MacbookAir)

```bash
# 编译 (在任意 Go 环境)
cd /Users/niuma/Workspace/gotunnel
GOOS=darwin GOARCH=arm64 go build -o gotunnel-client ./client/

# 运行 SSH 隧道
./gotunnel-client -server <公网IP>:6000 -local 127.0.0.1:22 -tunnel ssh

# 运行 RDP 隧道
./gotunnel-client -server <公网IP>:6000 -local 127.0.0.1:3389 -tunnel rdp

# 或使用启动脚本
./scripts/start-client.sh -s <公网IP> -n ssh -l 22
./scripts/start-client.sh -s <公网IP> -n rdp -l 3389
```

### 开机自启 (macOS launchd)

创建 `~/Library/LaunchAgents/com.gotunnel.ssh.plist`:

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>com.gotunnel.ssh</string>
    <key>ProgramArguments</key>
    <array>
        <string>/usr/local/bin/gotunnel-client</string>
        <string>-server</string>
        <string>YOUR_IP:6000</string>
        <string>-local</string>
        <string>127.0.0.1:22</string>
        <string>-tunnel</string>
        <string>ssh</string>
    </array>
    <key>KeepAlive</key>
    <true/>
    <key>RunAtLoad</key>
    <true/>
    <key>StandardOutPath</key>
    <string>/tmp/gotunnel-ssh.log</string>
    <key>StandardErrorPath</key>
    <string>/tmp/gotunnel-ssh.log</string>
</dict>
</plist>
```

```bash
launchctl load ~/Library/LaunchAgents/com.gotunnel.ssh.plist
```

## 访问

```bash
# SSH 到公司 MacbookAir
ssh -p 2222 user@<公网IP>

# RDP 到公司 MacbookAir
# 连接 <公网IP>:3389
```

## 多隧道

一个 server 实例支持多个 client 同时注册不同隧道。每个隧道独立监听各自的 visitor 端口。

```bash
# Client A 注册 SSH 隧道
./gotunnel-client -server IP:6000 -local 127.0.0.1:22 -tunnel ssh

# Client B (同一台或不同机器) 注册 RDP 隧道
./gotunnel-client -server IP:6000 -local 127.0.0.1:3389 -tunnel rdp
```
