# EC20 在 macOS / OrbStack 下的 QMI 请求串行兼容模式

部分 EC20 + macOS + OrbStack USB 直通环境中，同一设备的 QMI 请求重叠时会出现 QMI framing error、EOF、broken pipe，并使 HiDeck 控制面进入恢复循环。本兼容模式让同一个 HiDeck 进程内、相同控制设备路径的请求串行执行。默认关闭，不按操作系统或 USB ID 自动启用。

这是针对已观察触发条件的兼容措施，尚不能唯一归因于 OrbStack 内核、EC20 固件或 USB 硬件；没有供电电压测量，不能据此判定供电不足，也不保证消除所有 USB 断连。

## 启用与撤销

必须先使用包含本补丁的源码构建程序/镜像；仅给旧版官方镜像添加变量不会生效。在已有 Compose 服务中合并：

```yaml
services:
  hideck:
    environment:
      HIDECK_QMI_SERIAL_REQUESTS: "1"
```

然后重建该服务：

```bash
docker compose up -d --no-deps --force-recreate hideck
```

使用自己构建的镜像标签，并保留既有设备映射、配置与数据挂载。撤销时删除此变量（或设为 `"0"`）并再次重建服务。仅精确值 `1` 启用；运行时不要动态改变变量。

## 复现环境与步骤

历史故障及对照实验基于 HiDeck `v2.1.32`。移植时对照的 `v2.1.35` 主分支 `c1d7d98c355e2385074318c68f9135d56c7915e9`，其原始 QMI `client.go` 与该历史基线一致。2026-10-26 补录的当前环境如下，**不代表每项版本都是 10 月 19 日实验时的版本**：

| 项目 | 当前环境 |
| --- | --- |
| 主机 | Apple Silicon MacBook Pro，M5 Max，128 GB |
| macOS | 26.4.1 / 25E253 |
| OrbStack | 2.2.4，设备 Attach 到 Docker machine |
| Linux guest kernel | `7.2.9-orbstack-00391-g2b298d05fbd4` |
| 容器系统 / QMI proxy | Debian 12 / libqmi 1.32.2 |
| 模组 | Quectel EC20，USB `2c7c:0125` |
| EC20 固件（此前设备读取） | `EC20CEHCLGR06A14M1G_AUD` |
| 控制节点 | `/dev/cdc-wdm0`，网卡 `wwan0`，另有 `/dev/ttyUSB*` |

### 在受影响硬件上观察原始故障

请在可中断的测试设备上操作，保留原镜像和配置；不要在正常运行的 HiDeck 旁边同时启动外部 QMI 工具。

1. 将 EC20 接到 Mac，通过 OrbStack Devices 将它 Attach 到 **Docker**。容器使用原部署的 `privileged: true` 和 `/dev:/dev`，确认容器中存在 `/dev/cdc-wdm0` 与对应串口。USB 在 macOS 中出现不等于容器已取得控制节点。
2. 用未修补的 HiDeck 基线启动并接管该 QMI 设备，不设置 `HIDECK_QMI_SERIAL_REQUESTS=1`。
3. 保持后台状态轮询，同时打开设备页读取 SIM/eUICC 信息。在记录到的失败链条中，UIM `Read Record` transaction 39 尚未完成，UIM `Get Card Status` transaction 40 已发出；随后出现 endpoint hangup / framing error，再出现 EOF 和 broken pipe。这是时序相关现象，单次打开页面不保证触发。
4. 被动收集 HiDeck、qmi-proxy 与 guest kernel 日志。观察目标为 `QMI: read failed: EOF`、`QMI framing error detected`、`endpoint hangup: removed`、`write ... @->@qmi-proxy: ... broken pipe`，以及同时间窗口的 `Event TRB for slot 1 ep 0 with no TDs queued`。分享日志前移除 Token、卡片身份与手机号。
5. 换成包含补丁的镜像，设置上述变量为 `1`，重建容器并保持相同设备/配置，重复同样的轮询与卡信息读取。应确认实际 QMI 读写和最后通信时间持续推进，而不是只看容器存活。

### 历史串行 / 并发对照实验

以下是已执行的独立诊断步骤，**不是 HiDeck 内置 CLI 命令**。诊断时停止了应用，逐组重建 guest USB session；未对 modem 做冷复位。

1. 在同一 QMI 控制节点分配 DMS 和 NAS client。
2. 最多执行 120 轮状态轮询：DMS `0x002d`，NAS `0x004f`、`0x0020`、`0x0024`。
3. 比较逐条等待响应的串行发送与重叠发送，分别测试 qmi-proxy 和直接访问设备两条路径。初始轮询间隔为 25 ms。
4. 再把串行组的轮询间隔降为 0 ms，以区分“串行”与“固定延迟”的效果。

| 路径 / 发送方式 | 探针接受的请求结果 | 新增 xHCI 错误 |
| --- | --- | --- |
| proxy / 串行 / 25 ms | 480/480 | 0 |
| direct / 串行 / 25 ms | 480/480 | 0 |
| proxy / 并发 / 25 ms | 4/7，之后连接关闭 | 1 |
| direct / 并发 / 25 ms | 7/8，之后超时 | 1 |
| proxy / 串行 / 0 ms | 480/480 | 0 |
| direct / 串行 / 0 ms | 480/480 | 0 |

这些是传输/请求结果计数：每个串行组包含 120 次被探针接受的 NAS protocol result 74，不能解释为 480 次业务查询均取得成功数据。直接访问路径也能复现，因此 qmi-proxy 并非必要原因。零间隔串行仍通过，补丁没有加入固定发送延迟。

## 为什么不能只给 SendRequest 加 mutex

调用方取消或超时只会结束等待，已经排队、正在写入或等待回复的 QMI 请求仍可能继续。若此时释放设备锁，下一条请求仍会与旧请求重叠。

本模式的边界为：

- 以 `filepath.Clean(devicePath)` 为键，跨 Client 共享进程内 gate，同设备同一时刻只有一个请求持有它；不同路径互不阻塞。软链接、相对/绝对路径别名不合并，使用方应传入一致的控制节点路径。
- 等待 gate 计入调用上下文期限；取消的等待者不分配 transaction ID，也不发送请求。
- 请求入队后调用方取消，立即向调用方返回，后台继续持有 transaction 与 gate，等待写完并接收迟到回复。
- drain 等待最多一个 `DefaultRequestTimeout`（未设置则 30 秒）；写错误、连接关闭或 drain 超时时，先完成该 Client 的 transport Close 和 runtime 停止，再释放 gate，避免本进程的排队/阻塞写稍后继续发出。关闭 proxy socket 本身并不能证明 modem 已取消此前收到的操作。
- 外部 `qmicli`、另一个 HiDeck 进程和其他 QMI 客户端不受此 gate 保护；本方案不串行化 AT 指令，也不改变射频/SIM/运营商策略。兼容模式可能增加同设备请求排队时间。

## 自动化回归与实机验证边界

QMI 是嵌套模块，根目录的 `go test ./...` 不会覆盖这里。无硬件也可运行：

```bash
cd third_party/quectel-qmi-go
go test -race -count=10 -run '^TestSerialRequests' ./pkg/qmi
go test -race ./...
go vet ./...
```

测试通过 `net.Pipe` 观察实际写入、响应和关闭顺序，覆盖跨 Client 串行、默认并发、不同设备、等待者取消/超时、写入排队/阻塞、迟到回复、drain 超时及 transport 关闭边界。最小回归 `TestSerialRequestsSharedAcrossClientsUntilReply` 在未修补的 `SendRequest` 上失败：`request reached modem while another request is outstanding`。

上游已有 `TestReadPNNRecordsReturnsContextCancellation` 偶发报 `response channel not found for key=0x000b0002`；本次在未修改的原始 `client.go` overlay 对照中也复现。全包单次通过不代表重复运行永远通过，本兼容补丁没有修改该测试。

历史最终实机验收（2026-10-19）连续 300.2 秒、11 次采样，QMI proxy 读写计数与最后通信时间推进，未出现新的传输/xHCI/致命错误，设备节点与 HTTP 心跳正常。**该次验收为空档案、飞行模式控制链路观察，不是蜂窝驻网、数据、短信、通话或 eSIM 下载/切换的验收，也不是长期稳定性保证。** 后续使用仍有个别 EOF/自动恢复记录，所以本方案应视为请求重叠的缓解措施，不能作为所有断连问题的完整修复。
