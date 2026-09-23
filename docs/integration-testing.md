# 端到端联调与验收

本文是 SHIXUN-9 的可复现联调手册。它区分三类内容：**现在就能跑**的部分（本机已验证）、**需要镜像或网络**的部分、以及**需要真实硬件**的部分。三者不得混为一谈——主机测试通过不等于链路可用，构建成功不等于烧录成功。

## 1. 联调矩阵

| 链路 | 需要什么 | 当前状态 |
| --- | --- | --- |
| 模拟设备 → 真实 Broker → Go → 内存库 → REST | 无（进程内 Broker） | **已验证**，见 §3 |
| 模拟设备 → EMQX → Go → PostgreSQL → REST/WebSocket | EMQX 镜像与现有 `postgres-dev` 容器 | **已验证** |
| 真实设备 → EMQX → Go → PostgreSQL → REST | 实物开发板 + 上述镜像 | **已验证**（2026-09-21）；WebSocket 路由自动化测试覆盖 |
| 云端命令 → 设备 ACK | 模拟设备即可 | **已验证**（模拟设备侧），见 §3 |
| 真实设备遥测 → EMQX → Backend → PostgreSQL | 实物开发板 + 手机热点 | **已执行**（2026-09-21）：`MCU001` 完成 MQTT 上报，REST 可查且 `postgres-dev.telemetry` 已落库 |
| 云端命令 → 真实设备 ACK | 实物开发板 + 固件 MQTT 接线 | **已验证**（2026-09-22，见 §7.1） |
| 断网自治（本地采样/判断/声光不依赖网络） | 实物开发板 | **已验证**（2026-09-22，停 EMQX 场景，见 §6 第 2 项）；拔掉 AP 的更强场景未测 |
| 阈值掉电恢复 | 实物开发板（或 Flash 模拟） | 逻辑**已验证**（主机测试覆盖断电截断、擦除失败、单字节翻转）；2026-09-23 人工重新供电/Reset 后版本 10 与 40/80/30 阈值仍在 Flash；受控写入中断电尚未做 |
| 复合火警误报边界 | 调参记录 + 现场数据 | **未评估**：参数为实施方案文档初值 |

## 2. 结论摘要（当前可复现的部分）

已验证：

```sh
cd backend
go test -race -count=1 ./...            # 含 cmd/device-sim 的端到端用例
go test -count=1 -v ./cmd/device-sim/   # 单独看端到端断言
```

`cmd/device-sim` 是设备侧模拟器，它按 `docs/device-protocol.md` 冻结契约收发报文：连接、订阅 `device/control`、按周期向 `device/telemetry` 发布遥测、校验每条命令、并在 `device/command-ack` 上回执。它**故意与固件一样严格**，否则一个更宽松的模拟器会把契约缺陷藏起来而不是暴露出来。

端到端断言（`backend/cmd/device-sim/e2e_test.go`）覆盖：

1. **遥测入链**：模拟设备发布 → 后端入库 → `GET /status` 与 `GET /telemetry/latest` 返回相同数值。
2. **重复投递只入库一次**：模拟器整帧重发（QoS 1 重投的样子），按 `(deviceId, bootId, sequence)` 断言每个序号只出现一次。
3. **复合火警双因子**：同一环境内并排跑两台设备——一台只有气体上升、温度平稳，一台两者都上升。只有后者进入 `fire_warning`；并断言证据里两个量都超过各自阈值、样本数达标。**并排跑是关键**：分开跑的话，规则即使只读了一个因子，负例也会通过。
4. **控制闭环**：REST 下发阈值 → 模拟设备收到并执行 → `GET /commands/{requestId}` 从 `pending` 变为 `applied`，并在结果中报告新的 `thresholdVersion`。
5. **阈值确认**：REST 下发阈值 → 设备采纳新版本 → `GET /thresholds` 的 `desiredVersion` 与 `confirmedVersion` 相等、`confirmationState` 为 `confirmed`。
6. **幂等**：同一 `Idempotency-Key` 重放不会产生第二条命令，设备只收到一次。
7. **离线判定**：设备**保持 MQTT 会话连接**但停止上报，超过契约的 15 秒静默后被判 `offline`。保持连接是刻意的——这条断言证明后端依据的是收到的遥测而不是它看不见的 Broker 连接。

## 3. 在本机复现（无外部依赖）

```sh
cd backend
go fmt ./... && go vet ./...
go test -race -count=1 -coverprofile=coverage.out ./...
go tool cover -func=coverage.out | tail -1
```

PostgreSQL 路径单独启用（需要一个**可被销毁**的数据库，套件会创建并删除自己的 schema）：

```sh
docker start postgres-dev
# 先创建单独的可销毁测试数据库；不要把已有业务库作为测试目标。
docker exec postgres-dev psql -U postgres -d postgres -c 'CREATE DATABASE lab_test'
TEST_DATABASE_URL='postgres://postgres:<existing-password>@localhost:5432/lab_test?sslmode=disable' \
  go test -race -count=1 ./internal/store/
```

嵌入式路径（无 PostgreSQL）在模拟器测试中已被覆盖。

## 4. 带 EMQX 与 PostgreSQL 的联调

```sh
docker start postgres-dev
docker exec -i postgres-dev psql -U postgres -d postgres -v ON_ERROR_STOP=1 < backend/database/bootstrap.sql
docker compose -f deploy/compose.yaml up -d emqx
```

该编排仅提供 EMQX；数据库复用现有 `postgres-dev`，不再创建第二个 PostgreSQL 容器。`deploy/emqx/acl.conf` 实现契约要求的收发方向隔离：设备只能发布 `device/telemetry` 与 `device/command-ack`、只能订阅 `device/control`；后端相反；其余一律拒绝。

EMQX 5.8 已在 2026-09-21 实机联调中加载该 ACL，设备和 Backend 使用不同用户名成功完成遥测方向通信。这是本地集成证据，不代表 TLS、独立设备凭据和生产安全审核已完成。

启动后端并灌入模拟设备：

```sh
cd backend
DATABASE_URL='postgres://postgres:<existing-password>@localhost:5432/lab?sslmode=disable' \
MQTT_BROKER_URL='localhost:1883' \
MQTT_USERNAME='backend' MQTT_PASSWORD='backend-secret' \
BACKEND_ADDR=':8080' \
  go run .

# 另一个终端
cd backend
go run ./cmd/device-sim -broker localhost:1883 -device MCU001 -interval 5s -scenario warm-up
```

无密钥示例配置见 `deploy/README.md`；凭据只存在于编排文件与该命令行中，不进入任何被提交的配置或测试。

### 故障注入

模拟器可以按需制造后端必须容忍的失败模式：

| 参数 | 制造的情况 | 期望结果 |
| --- | --- | --- |
| `-duplicate-every N` | 每第 N 帧整帧重发 | 只入库一次，重复计数上升 |
| `-skip-every N` | 每第 N 帧丢弃 | 序号出现空洞，不产生告警也不丢设备 |
| `-unsynced-clock` | `timestamp` 为 `null` | 后端以 `receivedAt` 排序，不报错 |
| `-silent-after N` | 首次上报后静默 N 秒 | 超过窗口后判 `offline`，恢复后立即 `online` |
| `-scenario gas-surge` | 气体超限的本地告警 | `localAlarm` 为真、原因数组含 `gas_high` |

### 观察点

```sh
curl 'http://localhost:8080/api/v1/devices/MCU001/status'
curl 'http://localhost:8080/api/v1/devices/MCU001/telemetry/latest'
curl 'http://localhost:8080/api/v1/devices/MCU001/alerts'
curl 'http://localhost:8080/api/v1/devices/MCU001/thresholds'
```

WebSocket 实时流（需要 `wscat` 或任一 WebSocket 客户端）：

```sh
wscat -c ws://localhost:8080/ws/v1/devices/MCU001/telemetry
```

控制命令（`Idempotency-Key` 为必需项；控制主题只接受 `set_thresholds`）：

```sh
curl -X PUT 'http://localhost:8080/api/v1/devices/MCU001/thresholds' \
  -H 'Content-Type: application/json' -H 'Idempotency-Key: 01MANUALDEMO' \
  -d '{"temperatureHighC":30.0,"humidityHighRh":80.0,"gasHighPpm":80.0}'
```

返回 202 只表示"已接受并发布"。最终结果看 `GET /commands/{requestId}` 或 WebSocket 的 `command.status_changed`。

## 5. 验收检查表

| 检查 | 通过标准 |
| --- | --- |
| 遥测入链 | 设备上报后 5 秒内 `GET /telemetry/latest` 反映该值 |
| 序号与去重 | `-duplicate-every 2` 下，`(deviceId, bootId, sequence)` 唯一 |
| 复合火警 | 双因子同时满足才 `fire_warning`；单因子最多 `suspect` |
| 误报边界 | 记录触发时的证据（`gasAdcRise`、`temperatureRateCPerMinute`、`sampleCount`），并记录调参过程 |
| 离线与恢复 | 静默超窗判 `offline`，恢复上报立即 `online` 并产生状态事件 |
| 控制闭环 | 202 后命令经 `published` 到 `applied`；`GET /commands/{requestId}` 可见；阈值命令报告新的 `thresholdVersion` |
| 幂等 | 同一 `Idempotency-Key` 重放，设备只收到一次 |
| 阈值 | `confirmedVersion` 追上 `desiredVersion` 后 `confirmationState` 为 `confirmed`；超时不回滚 `desiredVersion` |
| 断网自治 | 拔掉/屏蔽网络后，本地采样、阈值判断、LED 与蜂鸣器继续工作 |
| 掉电恢复 | 写阈值后断电重启，设备仍执行写入的阈值 |
| 日志脱敏 | 日志与截图中不含 Wi-Fi 密码、Broker 凭据、真实内网地址 |

## 6. 需要真实硬件的部分

1. **烧录与上电 —— 已执行（2026-09-22）**：STM32F103C8T6 + STM32F10x SPL，DHT11(PA5)、MQ135(PA1)、SSD1306(PB8/PB9)、LED(PA4)、蜂鸣器(PA8/TIM1_CH1)、ESP8266(USART1 PA9/PA10)。ST-LINK V2（J37S7）+ OpenOCD 0.12.0，release 构建 `program … verify reset`（Verify OK）。蜂鸣器早期方案记为 PB13，实机验收确认实际为 PA8。逐步证据见 `../hardware/README.md` 的「实机闭环验收记录」。
2. **断网自治 —— 已执行（2026-09-22）**：停止 EMQX 容器使设备失去 Broker，SWD 直读 `GPIOA_ODR` PA4=1（LED 报警）且 `TIM1_CCER` CC1E 持续为 1（蜂鸣器输出仍在驱动），即本地采样、判断与声光报警不受网络影响。**未测**：拔掉 AP（Wi-Fi 断开）这一更强的场景，以及「从断网到本地报警的延迟」的量化。
3. **阈值保持 —— 人工电源循环已执行（2026-09-23）**：v2 固件下发并确认 version 10（40/80/30）→ 用户人工重新连接供电并按 Reset → 新 `bootId` 恢复上报 → SWD 直读 `0x0800F800` 得到 schema 1 / version 10 / 40 / 80 / 30，另一槽 `0x0800FC00` 保留 version 9 / 40 / 80 / 80。此前 2026-09-22 仅做过单次复位；本次增加人工电源循环。**未做**写入中途受控断电；双槽切换抗断电仍以主机故障注入测试为证据。
4. **DHT11 时序**：连续采样 1 小时的失败率与超时行为——**未执行**。
5. **MQ135 标定**：预热曲线、负载电阻确认、标准气体标定；标定前 `gasPpm` 只是相对指标——**未执行**。
6. **MQTT 实机链路**：见 §7，已完成上行与下行闭环。

## 7. 实机 MQTT 验收状态

上行射频链路已接入并通过首轮实机验收。原阻断项与处理如下：

| 迁移前状态 | 已落地改造 | 原因 |
| --- | --- | --- |
| 收发缓冲区均 64 字节 | 扩到约 640 字节 | 一帧遥测 PUBLISH 约 430 字节 |
| `+IPD` 正文按 NUL 结尾文本交付 | 按长度交付 | MQTT 帧含 `0x00` 字节 |
| 主循环只维护 TCP 文本帧 | 节拍驱动的会话状态机 | 需要 CONNECT → SUBSCRIBE → 发布/心跳 |

实机首轮证据：EMQX 显示 `MCU001`/`device` 已连接且订阅 1 个主题；Backend 接收的遥测为 `network=online`；PostgreSQL 已按 `(deviceId, bootId, sequence)` 持久化。本轮还发现并修复了 `bootId` 中的连字符违反 Backend 字段约束的问题。

### 7.1 下行控制闭环（2026-09-22）

`device/control` 的 PUBLISH 已接入主循环。实机证据：

- `PUT /thresholds` → 202 → Backend 记录 `command acknowledgement received … status:"applied"` → 命令资源 `state:"applied"`（accepted→completed 约 1 s）→ 下一次遥测 `thresholdVersion` 前进到新版本。
- 旧的 `POST /commands/mute` 已随远程静音能力删除，调用将收到标准 404。
- `PUT /thresholds` → `applied`、`confirmedVersion` 前进，复位后 Flash 记录仍在（详见 §6 第 3 项）。
- 停止 EMQX 后设备失去 Broker 但本地报警继续（§6 第 2 项）；重启 EMQX 后设备**无需复位**自行重连并恢复上报（`bootId` 不变、`sequence` 续增）。

**历史 Backend 侧缺陷复核**：曾出现 MQTT 会话每 30 秒断开一次，导致控制命令随机 503；该缺陷已由 PR #21 修复。2026-09-23 v2 验收中，EMQX 完全停启后 Go 与设备自动重连，随后同值阈值命令返回 `applied`、desired/confirmed version 11 一致，证明本轮的 Broker 重启后命令下发成功。此单次验收不等于长期故障率统计。

## 8. 未覆盖风险

| 风险 | 说明 | 缓解 |
| --- | --- | --- |
| EMQX 生产安全未验证 | 本地 ACL 已运行，但尚无 TLS 和独立设备凭据 | 正式部署前增加 TLS、凭据轮换和方向隔离安全测试 |
| 误报率未知 | 预警参数（150 ADC / 3 °C·min⁻¹）来自实施方案文档初值 | 记录触发证据并留出整定时间；用 `-scenario warm-up` 复现预期触发路径 |
| 真机验收不完整 | 2026-09-23 v2 已验证 DHT11 短时读数、气体阈值蜂鸣器、人工电源循环后 Flash 保持；仍未做：DHT11 连续 1 小时失败率、MQ135 标定、受控写入中途断电与热点 AP 断开 | §6、`hardware/README.md` 已知限制 |
| PostgreSQL 集成测试默认跳过 | 未设 `TEST_DATABASE_URL` 时跳过 | CI 必须提供该变量，否则数据库路径实际覆盖为零 |
| 未同步时钟设备的时间语义 | `timestamp` 为 `null` 时后端以 `receivedAt` 排序 | 契约已规定；`-unsynced-clock` 可复现 |
| 鉴权粒度 | 只有"有 token/无 token"，无用户-设备授权与操作分权 | `AUTH_MODE=none` 不得用于不可信网络；见 `backend/docs/api.md` §1.3 |

## 9. 提交与评审

按根 `AGENTS.md`：联调相关改动走独立分支与 PR；涉及协议或接口的变更必须请求受影响模块负责人评审；必需检查全绿且讨论解决后方可合并，不得直接改 `main`。Multica 中在对应 issue 记录联调环境、命令、观察结果与未覆盖风险。
