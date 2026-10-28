package api

const mcpInstructions = `HiDeck 用于管理设备、SIM/eSIM、短信、电话、联系人、代理和任务。
先调用 hideck_device_list 获取 device_id；需要当前 SIM 身份时读取设备概览/配置，不要猜测 device_id、ICCID、IMSI 或复用切卡前身份。
结果同时提供 structuredContent 和等价 JSON 文本。先判断 isError；普通业务结果为 {status_code,data}，data 是原有业务响应，204 的 data 为 null。文件识别、MBN 上传和网页音频连接分别返回 {activation}、{asset}、{connections}，不包业务信封。
isError=true 表示失败；保留原始 error、data.code、data.message、data.error 等原因。202 仅表示已受理，不代表任务完成；200 也不保证短信已送达或电话已接通。
短信：hideck_sms_send 的 data.message_id 非空时，使用 hideck_sms_delivery 查询；acked 只表示发送端受理。没有 message_id 时不能声称已确认投递。
网页电话：先调用 hideck_phone_audio_connections 获取当前令牌获准使用的连接 ID，再给拨号/接听/通话控制传 audio_connection_id；没有连接时请用户到电话页授权。call_id 来自拨号结果或 hideck_phone_active，实际通话状态以查询为准，不能传入媒体租约或绕过通话所有权。
eSIM：先查 hideck_esim_overview/hideck_esim_profiles；下载和扫描可接收请求内进度，但只有最终结果能判断完成。切换后重新读取设备和 Profile 状态。客户端断开或超时不表示硬件已回滚。
命令：先查 hideck_command_catalog，再执行 hideck_command_execute；保存 data.execution.id，使用 hideck_command_results 的 after_id 游标读取事件并按 execution_id 匹配，result/error 和 execution.state 判断完成。
自动任务：hideck_task_run 返回 data.id、data.task_id 和排队状态；用 hideck_task_runs 按 task_id 查询并匹配运行 id，queued/running 不是完成。余额和 MBN 操作也应使用返回的 query/operation 标识查询，不能重复发起代替查询。
所有写操作均不自动重试。遇到发送、拨号、切卡、下载的超时或输出契约错误，先查询真实状态，再决定后续动作，避免重复执行。分页沿用每项工具的 limit/offset 或事件游标。
录音通过 hideck://recordings/... 的资源引用读取；导入文件使用文件名和明确格式的文件内容，不接受任意服务器路径。不要把短信正文、令牌或 eSIM 激活码写入日志或提交到仓库。
接入错误：HTTP 401 表示令牌无效、已撤销或误用了网页登录凭据；503 可能是 MCP 关闭或存储未就绪；403 检查 Host/Origin。TLS、网络及客户端协议错误应保留实际原因，不能用业务健康状态代替连接结论。网页“检查接入”展示服务就绪和本次进程的真实请求/调用记录，不探测 Agent 所在机器的网络。`

func mcpToolGuidance(name string) string {
	guidance := map[string]string{
		"hideck_device_list":             "返回 data.devices；从中选择真实 device_id。",
		"hideck_sms_send":                "先查设备及当前 SIM 身份。data.message_id 用于 hideck_sms_delivery；acked/已提交不等于送达；超时先查状态，不要直接重发。",
		"hideck_sms_delivery":            "message_id 来自发送结果；未找到记录不表示应重新发送。",
		"hideck_phone_audio_connections": "从 connections 选择有效 ID 用作 audio_connection_id；空列表需在电话页授权。",
		"hideck_phone_dial":              "先选网页音频连接。返回 data.call_id；202 表示开始呼叫，使用 hideck_phone_active 确认实际状态。",
		"hideck_phone_active":            "返回 data.calls；call_id 用于后续控制。read_only 是查询时的媒体所有权视角，实际控制仍需选择有效音频连接并通过服务端校验。",
		"hideck_esim_download":           "请求内进度不是最终成功；data.step=done 才表示下载结束。保留 warning/warning_code，并重新查询 Profiles；失败或断开不要自动重新下载。",
		"hideck_esim_switch":             "ICCID/AID 来自 Profiles；切换后重新查询设备和 Profile，不复用旧 SIM 身份。",
		"hideck_command_execute":         "先查命令目录；202 返回 data.execution.id。用 hideck_command_results 查询事件并匹配 execution_id，不重复执行命令来等结果。",
		"hideck_command_results":         "返回 data.events；按 execution_id 关联执行，以事件 id 更新 after_id 游标。kind=result/error 或 execution.state 表示执行结果。",
		"hideck_task_run":                "202 返回 data.id（运行 ID）和 data.task_id；用 hideck_task_runs 查询并匹配 id，受理不等于执行完成。",
		"hideck_task_runs":               "按 task_id 筛选，返回 data.runs、total、limit、offset；按运行 id 匹配，检查 status、output、error。",
		"hideck_balance_query":           "保存 data.query.id，并使用 hideck_balance_get 查询最终状态，不重复发起查询。",
		"hideck_mbn_start":               "保存 data.operation_id，使用 hideck_mbn_operations 查询；受理不表示驻网成功。",
	}
	if text := guidance[name]; text != "" {
		return "\n调用说明：" + text
	}
	return ""
}
