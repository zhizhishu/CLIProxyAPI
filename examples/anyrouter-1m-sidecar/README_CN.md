# AnyRouter 1M Sidecar

这个 sidecar 用来把官方 CPA 的 Claude 1M 渠道转发到 AnyRouter, 不改 CPA 主程序。

分工:

- CPA: 鉴权, UI, 模型管理, 日志, 路由, OpenAI/Claude/Responses 协议转换。
- Sidecar: 只处理 AnyRouter Opus 4.7 1M 的上游细节。

Sidecar 会做:

- 把 body 里的 `claude-opus-4-7[1m]` 改成上游可识别的 `claude-opus-4-7`。
- 补 `Anthropic-Beta: context-1m-2025-08-07`。
- 保留 Claude CLI 风格 UA 和 `X-Stainless-*` 头。
- 补 `metadata.user_id` 为 Claude CLI JSON 字符串形态。
- 可选拦截 `messages: []`, 避免空消息打到 AnyRouter 后返回 503。

## 部署

在服务器 `/root/CLIProxyAPI/.env` 里保留 CPA 镜像, 再加 sidecar 镜像:

```env
CLI_PROXY_IMAGE=ghcr.io/zhizhishu/cliproxyapi:future
ANYROUTER_1M_SIDECAR_IMAGE=ghcr.io/zhizhishu/anyrouter-1m-sidecar:future
ANYROUTER_1M_PORT=8787
ANYROUTER_UPSTREAM=https://anyrouter.top
ANYROUTER_1M_ADMIN_TOKEN=change-this-token
```

启动:

```bash
docker compose -f docker-compose.yml -f docker-compose.anyrouter-1m-sidecar.yml pull
docker compose -f docker-compose.yml -f docker-compose.anyrouter-1m-sidecar.yml up -d --no-build
```

打开 UI:

```text
http://服务器IP:8787
```

如果设置了 `ANYROUTER_1M_ADMIN_TOKEN`, 保存配置时在页面底部输入这个 token。

## CPA 配置

普通 Opus 直连 AnyRouter:

```yaml
claude-api-key:
  - api-key: 你的AnyRouterKey
    priority: 20
    base-url: https://anyrouter.top
    proxy-url: ""
    models:
      - name: claude-opus-4-7
        alias: "claude-opus-4-7"
    headers:
      Anthropic-Dangerous-Direct-Browser-Access: "true"
      Anthropic-Version: "2023-06-01"
      User-Agent: claude-cli/2.1.126 (external, claude-vscode, agent-sdk/0.2.126)
      X-App: cli
      X-Stainless-Arch: x64
      X-Stainless-Lang: js
      X-Stainless-Os: Windows
      X-Stainless-Package-Version: 0.81.0
      X-Stainless-Retry-Count: "0"
      X-Stainless-Runtime: node
      X-Stainless-Runtime-Version: v24.3.0
      X-Stainless-Timeout: "600"
    cloak:
      mode: auto
```

1M Opus 走 sidecar:

```yaml
claude-api-key:
  - api-key: 你的AnyRouterKey
    priority: 19
    base-url: http://anyrouter-1m-sidecar:8787
    proxy-url: ""
    models:
      - name: claude-opus-4-7
        alias: "claude-opus-4-7[1m]"
    headers:
      Anthropic-Dangerous-Direct-Browser-Access: "true"
      Anthropic-Version: "2023-06-01"
      User-Agent: claude-cli/2.1.126 (external, claude-vscode, agent-sdk/0.2.126)
      X-App: cli
      X-Stainless-Arch: x64
      X-Stainless-Lang: js
      X-Stainless-Os: Windows
      X-Stainless-Package-Version: 0.81.0
      X-Stainless-Retry-Count: "0"
      X-Stainless-Runtime: node
      X-Stainless-Runtime-Version: v24.3.0
      X-Stainless-Timeout: "600"
    cloak:
      mode: auto
```

下游 UI 里:

- 普通模型填 `claude-opus-4-7`。
- 1M 模型填 `claude-opus-4-7[1m]`。

## 注意

- 不要把 AnyRouter key 写进 sidecar UI。CPA 会把 key 作为上游请求头转给 sidecar, sidecar 再转给 AnyRouter。
- 如果 CPA 和 sidecar 不在同一个 compose 网络, `base-url` 要改成 sidecar 的实际地址。
- sidecar 只处理 `/v1/messages` 和 `/v1/messages/count_tokens` 的 Claude 请求体。协议转换仍由 CPA 完成。
