# 安装指南

## 方式一：插件商店安装（推荐）

将本仓库添加为 CPA 的第三方插件源，通过 CPA 管理接口一键安装。后续更新只需重新调用安装接口。

### 1. 添加插件源

在 CPA 的 `config.yaml` 中，找到 `plugins` 段，添加 `store-sources`：

```yaml
plugins:
  enabled: true
  dir: plugins
  store-sources:
    - "https://raw.githubusercontent.com/uf-hy/cpa-plugin-codexcomp/master/registry.json"
  configs:
    codexcomp:
      enabled: true
      priority: 1
      # 可选：自定义截断后的续写提示。不配置时默认是 Continue thinking...
      # marker_text: "Spend time on thinking; you do not need to use the commentary channel to report progress to me."
      # 可选：最多续写轮数。默认 3；设为 0 可临时禁用续写做 A/B 对比。
      # max_continue: 3
      # 可选：最大截断层级。默认 6；设为 0 表示不限制上限。
      # max_tier_n: 6
      # 可选：截断检测步长。默认 518；没有新样本证据时不建议修改。
      # truncation_step: 518
      # 可选：输出调试日志到 CPA host log。默认 false，排障时再开。
      # debug_log: false
```

### 2. 重启 CPA

```bash
# Docker：
cd <CPA_DIR> && docker compose restart

# 独立部署：
systemctl restart cli-proxy-api
```

### 3. 安装插件

通过 CPA 管理接口安装：

```bash
curl -X POST "<CPA_URL>/v0/management/plugin-store/codexcomp/install" \
  -H "Authorization: Bearer <MANAGEMENT_KEY>"
```

`<MANAGEMENT_KEY>` 是 `config.yaml` 中 `remote-management.secret-key` 的值。

CPA 会自动下载对应架构的 `.so` 文件，校验 SHA256，放到 `plugins/` 目录，并热重载。通常无需再次重启。

### 4. 验证

```bash
curl -sN <CPA_URL>/v1/responses \
  -H "Authorization: Bearer <YOUR_KEY>" \
  -H "Content-Type: application/json" \
  -d '{"model":"gpt-5.5","stream":true,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"Hi"}]}],"reasoning":{"effort":"low"}}' \
  | grep proxy_rounds
```

如果输出中看到 `proxy_rounds`，说明插件正常工作。

### 更新

重新调用安装接口即可拉取最新 release：

```bash
curl -X POST "<CPA_URL>/v0/management/plugin-store/codexcomp/install" \
  -H "Authorization: Bearer <MANAGEMENT_KEY>"
```

---

## 方式二：手动安装

适用于无法使用插件商店管理接口的场景。

### 1. 确认 CPU 架构并下载成品

```bash
set -euo pipefail

ARCH="$(uname -m)"
case "$ARCH" in
  x86_64|amd64)
    GOARCH="amd64"
    ;;
  aarch64|arm64)
    GOARCH="arm64"
    ;;
  *)
    echo "不支持的 CPU 架构：$ARCH。当前 release 只提供 Linux amd64/arm64 成品。" >&2
    exit 1
    ;;
esac

# 获取最新版本号（不带 v 前缀）
VERSION=$(curl -s https://api.github.com/repos/uf-hy/cpa-plugin-codexcomp/releases/latest \
  | python3 -c "import sys,json;print(json.load(sys.stdin)['tag_name'].lstrip('v'))")

wget -qO /tmp/codexcomp.zip \
  "https://github.com/uf-hy/cpa-plugin-codexcomp/releases/latest/download/codexcomp_${VERSION}_linux_${GOARCH}.zip"

test -s /tmp/codexcomp.zip
```

### 2. 创建插件目录并解压

```bash
mkdir -p <CPA_DIR>/plugins
unzip -o /tmp/codexcomp.zip -d <CPA_DIR>/plugins/
```

### 3. 在 config.yaml 中启用插件

检查 `<CPA_DIR>/config.yaml`。如果没有 `plugins` 段，添加：

```yaml
plugins:
  enabled: true
  dir: plugins
  configs:
    codexcomp:
      enabled: true
      priority: 1
```

如果已经有 `plugins` 段，确保 `plugins.enabled: true`，并确保 `configs.codexcomp.enabled: true` 存在。

### 4. 挂载插件目录（仅 Docker）

如果 CPA 跑在 Docker 里，确保 `docker-compose.yml` 有 plugins 卷映射：

```yaml
volumes:
  - ./plugins:/CLIProxyAPI/plugins
```

### 5. 重启 CPA

```bash
# Docker：
cd <CPA_DIR> && docker compose restart

# 独立部署：
systemctl restart cli-proxy-api
```

### 6. 验证

```bash
curl -sN <CPA_URL>/v1/responses \
  -H "Authorization: Bearer <YOUR_KEY>" \
  -H "Content-Type: application/json" \
  -d '{"model":"gpt-5.5","stream":true,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"Hi"}]}],"reasoning":{"effort":"low"}}' \
  | grep proxy_rounds
```

## 卸载

```bash
rm <CPA_DIR>/plugins/codexcomp*.so
cd <CPA_DIR> && docker compose restart
```

如果是独立部署，删除插件后改用对应的服务重启命令。

## 排障

- **插件没加载**：检查 CPA 日志中是否有 `codexcomp` 相关条目。确保 `plugins.enabled: true` 且 `.so` 文件在 `plugins` 目录中。
- **Docker 没挂载插件目录**：确认 `./plugins:/CLIProxyAPI/plugins` 已写入 `docker-compose.yml`，并且宿主机上的 `<CPA_DIR>/plugins/codexcomp.so` 存在。
- **架构不匹配**：`.so` 必须匹配 CPA 容器或进程的运行时架构，不是宿主机架构。Apple Silicon 上跑 Docker 需要确认容器实际是 `linux/amd64` 还是 `linux/arm64`。
- **想看续写是否触发**：临时设置 `debug_log: true`，重启 CPA 后查看 CPA 日志。最终响应里也会有 `metadata.proxy_rounds`、`metadata.proxy_billed_usage` 和可能的 `metadata.proxy_stopped_reason`。CPA 如果配置了 `debug: true` 和 `logging-to-file: true`，日志文件在容器内的 `/CLIProxyAPI/logs/main.log`。
