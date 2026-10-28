# HiDeck MCP 接入

HiDeck 通过 MCP 向 CLI 和 IDE 中的 AI 助手提供 125 个业务工具。服务默认关闭，在网页「设置 → MCP 接入」开启，使用独立令牌连接。

## 连接

1. 使用网页登录，阅读并确认当前版本的免责声明。
2. 在设置中开启 MCP，创建一个有明确名称的令牌。
3. 保存创建窗口展示的令牌或连接配置。明文只返回一次；关闭窗口后只保留显示前缀。
4. 在「Agent / 客户端」中选择对应客户端，复制配置；也可点击「复制给 Agent」，让有本机配置权限的 Agent 合并配置并验证连接。

页面支持以下八种客户端。配置模板中的 `<MCP_TOKEN>` 需要替换为有效令牌；创建窗口中的配置已填入新令牌，关闭窗口后恢复占位符。明文不会因切换客户端而重新从服务器获取。

| 客户端 | 默认用户配置位置 | 配置方式 |
| --- | --- | --- |
| [Codex](https://learn.chatgpt.com/docs/extend/mcp) | `~/.codex/config.toml` | TOML 的 `mcp_servers.hideck`，使用 `http_headers` |
| [Claude Code](https://code.claude.com/docs/en/mcp) | `~/.claude.json` | `mcpServers.hideck`，HTTP URL 与 Authorization 头 |
| [Cursor](https://cursor.com/docs/context/mcp) | `~/.cursor/mcp.json` | `mcpServers.hideck`，URL 与请求头 |
| [Gemini CLI](https://geminicli.com/docs/tools/mcp-server/) | `~/.gemini/settings.json` | 使用 `httpUrl` 和请求头 |
| [Hermes Agent](https://hermes-agent.nousresearch.com/docs/user-guide/features/mcp) | `~/.hermes/config.yaml` | YAML 的 `mcp_servers.hideck` |
| [DeepSeek dsh](https://github.com/deepseek-ai/deepseek-harness/blob/master/packages/mcp/mcp-client/README.md) | `$DSH_HOME/cordis.patch.yml`，默认 `~/.dsh/cordis.patch.yml` | Cordis `insert` 补丁，注册 `@deepseek-ai/dsh-mcp-client`，传输为 `streamable-http` |
| [Pi Agent](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/mcp.md) | `~/.pi/agent/mcp.json` | 新版内置 MCP，使用 URL 与请求头；`pi mcp list` 验证，`/reload` 重载 |
| [Grok Build](https://docs.x.ai/build/features/mcp-servers) | `~/.grok/config.toml`，支持 `GROK_HOME` | TOML 的 `mcp_servers.hideck`，使用 `headers`；`grok mcp doctor hideck` 诊断，`/mcps` 中按 `r` 刷新 |

HiDeck 服务已经运行在服务端，以上客户端直接连接远程地址。配置应合并到原文件，保留其他连接及设置；自定义配置目录以客户端的实际设置为准。Agent 所在机器必须能访问服务地址，使用本地 CA 的 HTTPS 服务时需信任该 CA。旧版 Pi 若没有 `pi mcp` 命令，需升级到支持内置 MCP 的版本，或使用该版本已安装的 MCP 扩展配置，不能照搬新版配置。

服务地址为 HiDeck 访问地址加 `/mcp`，支持 HTTP 和 HTTPS，例如 `http://<主机>:<HTTP 端口>/mcp` 或 `https://hideck.example.com/mcp`。网页配置使用当前网页的协议、主机和端口；跨机器连接时，应填写 Agent 实际能够访问的地址。

连接使用 Streamable HTTP，协议版本自动协商。支持等待参数的模板提供六分钟等待窗口，以覆盖五分钟 eSIM 下载操作。各客户端的验证范围见 [验证说明](mcp-validation.md)。

通用客户端配置示例（具体客户端的配置文件位置及顶层字段以客户端文档为准）：

```json
{
  "mcpServers": {
    "hideck": {
      "type": "http",
      "url": "https://hideck.example.com/mcp",
      "headers": {
        "Authorization": "Bearer <MCP_TOKEN>"
      }
    }
  }
}
```

令牌不能用于网页登录或 REST 管理接口，网页会话令牌也不能访问 MCP。首版令牌均拥有本页所列业务权限、长期有效，可独立撤销。“撤销”保留失效记录；“删除”永久移除记录，有效及已撤销令牌均可删除。关闭服务、撤销或删除令牌后，后续 HTTP 请求及业务调用立即拒绝，关联网页音频授权失效；已开始的硬件操作不承诺回滚。

SDK 保留 DNS 重绑定保护，外围使用 Go 的跨源保护。CLI 请求不需要 Origin。若本机反向代理连接 `127.0.0.1` 并转发公网 Host，SDK 可能返回 403；应在该代理的 MCP 路由中把上游 Host 设为实际本地服务地址（例如 `localhost:7575`），并继续保留公网 HTTPS 和 Authorization 头。不要关闭跨源校验来规避错误。

## 工具参数与结果

工具名称统一为 `hideck_<领域>_<动作>`，由显式目录注册。没有任意 HTTP 请求工具。

- 路径标识（例如 `device_id`、`iccid`、`call_id`）和查询条件位于参数顶层。
- 原 REST JSON 请求体放在 `request` 字段内；字段名称、业务验证和分页含义保持一致。
- 参数与输出的 JSON Schema、操作说明和只读/破坏性注解通过 `tools/list` 查询。初始化响应的 `instructions` 提供标识符获取、异步结果查询及错误处理说明。
- 结果同时提供 `structuredContent` 和 JSON 文本。一般结果为 `{"status_code": 200, "data": ...}`；真实业务错误保留状态、原因和已有业务错误码，并设置 `isError`。
- 发送短信、外呼、切换 SIM 等写操作不会自动重试。遇到连接中断或超时，应先查询设备、短信投递或通话状态，再决定是否重新操作。

全部 125 项工具均声明 `outputSchema`。普通业务结果包含 `status_code` 和 `data`，204 的 `data` 为 `null`。部分业务数据仍为通用结构，应以对应工具的 Schema 为准。识别激活码、上传 MBN、查询网页音频连接分别返回 `{activation}`、`{asset}`、`{connections}`。

Schema 同时覆盖业务失败和 `{error}` 工具错误。输出不符合契约时会明确报告错误；操作可能已经发生，应先查询真实状态，不能据此自动重试或声称已回滚。

| 操作 | 结果关联与完成判断 |
| --- | --- |
| 短信发送 | `data.message_id` 非空时用 `sms_delivery` 查询；`acked` 仅为发送端受理，不能声称收件手机已收到 |
| 网页拨号 | 先查 `phone_audio_connections` 选连接，再传 `audio_connection_id`；`data.call_id` 用于控制，`phone_active` 查询实际通话状态 |
| eSIM 下载/切换 | 进度不是最终结果；下载最终 `data.step=done` 后仍需保留警告并查询 Profiles，切卡后重新读取身份 |
| 命令执行 | 202 的 `data.execution.id` 对应事件 `execution_id`；使用 `command_results` 的 `after_id` 游标读取，以事件及执行状态判断结束 |
| 自动任务 | 202 的 `data.id` 为运行 ID；按 `data.task_id` 调用 `task_runs` 并匹配运行 ID，`queued/running` 不表示完成 |
| 余额 / MBN | 分别保存 `data.query.id` / `data.operation_id`，用 `balance_get` / `mbn_operations` 查询，不重复发起操作代替查询 |

发送短信示例：

```json
{
  "name": "hideck_sms_send",
  "arguments": {
    "request": {
      "device_id": "<设备 ID>",
      "phone": "<收件号码>",
      "message": "<短信内容>"
    }
  }
}
```

查询短信会话示例（`iccid` 使用当前实际 SIM 标识）：

```json
{
  "name": "hideck_sms_read",
  "arguments": { "iccid": "<ICCID>", "peer": "<对方号码>", "limit": 50 }
}
```

## 能力覆盖

| 领域 | 工具与范围 |
| --- | --- |
| 设备 | `device_list/discover/rescan/get/config/add/update/delete/refresh/reboot/retry_sim_pin/at/ussd/ussd_continue/ussd_cancel` |
| 网络和 SIM | `network_usb_mode/flight_mode/configure/scan/selection_get/selection_set`；`sim_policies/policy_get/policy_set` |
| 短信 | `sms_send/delivery/conversations/read/mark_read/message_get/message_delete/conversation_delete` |
| 联系人 | `contact_lookup/list/save/delete/batch_save/batch_delete/group_rename/group_delete/import/export` |
| 电话 | `phone_devices/configure/reconnect/dial/active/answer/reject/hangup/dtmf/hold/resume/switch/history/history_delete/recorded_call` |
| 自动接听 | `phone_auto_answer_get/set/audio`：配置与音频上传 |
| eSIM | `esim_overview/profiles/eid/chip_info/download/switch/disable/rename/delete/notifications/notification_retry/recover_identity/decode_activation` |
| MBN | `mbn_status/operations/start/cancel/files/file_read/file_upload/file_delete/policies/policy_set` |
| 代理 | `proxy_overview/get/configure/start/stop/restart` |
| 前置代理 | `upstream_list/create/update/delete/probe/countries/routes/route_set/route_delete` |
| 命令 | `command_catalog/execute/results/history_clear` |
| 余额、运营商规则 | `balance_query/device_history/history/get/manual_get/manual_set/manual_delete`；`carrier_rules/rule_create/rule_update/rule_delete` |
| 自动任务 | `task_list/get/create/update/delete/run/runs` |
| 诊断 | `diagnostic_health/system/traffic/logs` |
| 网页音频 | `phone_audio_connections` |

上表省略每个工具的 `hideck_` 前缀。没有开放系统升级、卸载、网页登录凭据、通知密钥配置；没有 OAuth、设备权限矩阵或 AI 实时语音模型。

## 网页电话音频

在电话页点击「连接 MCP」，选择令牌、填写连接名称并授权麦克风。该流程复用网页现有 WebRTC 音频，不另建音频通道。保持网页打开。

使用 `hideck_phone_audio_connections` 查询当前令牌可用的连接，只返回 ID、名称和状态。拨号、接听、拒接、挂断、DTMF、保持、恢复、切换均需要 `audio_connection_id`，服务端从绑定取出经过验证的媒体凭据，仍执行电话服务的所有权校验。

```json
{
  "name": "hideck_phone_dial",
  "arguments": {
    "audio_connection_id": "<连接 ID>",
    "request": { "device_id": "<设备 ID>", "callee": "<被叫号码>" }
  }
}
```

多个网页分别拥有不同连接 ID，可明确选择。媒体重建时更新绑定；音频断开时不可使用，关闭网页、服务器重启、令牌撤销或关闭 MCP 后失效。解除绑定只解除授权；音频断开后的通话收尾/恢复沿用电话服务的原有流程。

`phone_recorded_call` 复用已有 VoWiFi 自动外呼录音能力，与网页实时音频控制独立，其支持范围取决于现有硬件和电话模式。

## 长操作、文件和录音

- eSIM 下载沿用原有五分钟超时；请求带 MCP progress token 时发送阶段消息和进度。取消或 HTTP 断开传递到请求内业务上下文。
- 选网扫描在请求内等待并发送进度消息，复用设备当前扫描及其原有两分钟超时。客户端取消只停止等待；已交给模组的扫描不会被描述为已撤销。
- eSIM 切卡仍沿用原有独立三十秒操作上下文及异步刷新，断开不代表硬件操作回滚。无 context 参数的已有硬件接口也不能保证立即停止。
- 命令中心、余额查询和自动任务返回原有执行/查询/运行 ID，通过结果和运行记录工具查询；保持原有任务生命周期。
- 文件输入使用 `filename`、`content_base64`，不接受宿主机路径。联系人支持 vcf/csv，限制为 8 MiB 和 5000 条；自动接听音频限制为 16 MiB；激活码图片/PDF 限制为 12 MiB；MBN 文件按 MBN 服务规则校验。
- 联系人导出、MBN 文件读取返回 MIME 类型和 base64 内容。
- 通话记录/命令结果中的可读取 MP3 录音返回 `hideck://recordings/<录音名>` 和 resource link，使用 MCP `resources/read` 读取。资源每次读取都需要有效令牌，不返回服务器文件路径。

## 网页管理 API

以下接口仅接受现有网页登录鉴权：

| 方法 | 路径 | 用途 |
| --- | --- | --- |
| GET / PUT | `/api/settings/mcp` | 读取/保存开关；默认关闭，持久化到 YAML |
| GET | `/api/mcp/diagnostics` | 检查服务就绪、工具注册、契约数量及内存中的令牌最近请求/调用；只接受网页会话 |
| GET / POST | `/api/mcp/tokens` | 列出元数据/创建令牌，创建时单次返回 `secret` |
| DELETE | `/api/mcp/tokens/:id` | 撤销令牌并解除其音频绑定 |
| DELETE | `/api/mcp/tokens/:id/record` | 永久删除有效或已撤销令牌记录并解除音频绑定 |
| POST | `/api/mcp/phone-connections` | 校验当前用户的 `media_id`、`lease` 后绑定令牌 |
| PUT | `/api/mcp/phone-connections/:id` | 更新该用户和令牌原有连接的媒体凭据 |
| DELETE | `/api/mcp/phone-connections/:id` | 解除授权，不额外挂断通话 |

SQLite 的 `mcp_tokens` 保存 ID、名称、显示前缀、SHA-256 摘要、创建和撤销时间；不保存令牌明文。音频绑定只保存在内存。

网页「检查接入」返回 `ready/status/message`，区分 MCP 关闭、工具初始化失败、免责声明未确认、无有效令牌和服务就绪；存储读取失败保留真实错误。服务就绪不等于客户端已连接，也不代表模组已驻网。诊断不模拟 Agent 网络，不关闭 TLS 校验。

令牌接入记录仅保留本次进程内最后一次成功鉴权 HTTP 请求时间，以及最后一次工具调用的名称、完成时间、耗时和失败标记。鉴权请求可能只是协议发现或被跨源策略拒绝的请求，不能把它标为工具调用成功。记录不包含令牌、参数或业务结果；重启、撤销、删除后清除，进行中的调用结束也不会恢复已清除记录。开关或令牌列表发生变化时，网页清除旧检查结果，需重新检查。

## 日志与排查

工具调用日志统一显示在网页「实时日志」页面，搜索 `MCP tool` 可筛选。日志包含工具名、令牌 ID、目标设备、耗时及失败状态，不记录令牌明文、短信正文或激活码。

- 401：检查是否携带有效 MCP 令牌，以及令牌是否已撤销或删除。
- 403：检查反向代理的 Host 转发和浏览器 Origin。
- 503：检查 MCP 开关、服务及存储状态。
- 客户端连不上：确认其能访问配置地址；HTTPS 连接同时检查证书信任。

验证范围见 [MCP 验证说明](mcp-validation.md)，当前限制见 [MCP 能力与限制](mcp-review.md)。
