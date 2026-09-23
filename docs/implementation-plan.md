# 智慧机房微环境监控与早期火情预警实施方案

## 1. 目标与验收边界

系统面向机房或实验室微环境，完成温湿度与气体浓度采集、本地断网自治报警、联网遥测、复合火情预警、历史查询和远程控制。核心安全原则是：云端能力增强监控，但不能成为本地报警的前置条件。

本方案定义可分阶段实现的工程边界，不表示仓库已经具备全部能力。各阶段实际进度以根 README、[integration-testing.md](integration-testing.md) 与 `hardware/README.md` 的验收记录为准：截至 2026-09-22，契约、硬件本地闭环、MQTT 联调与 Backend 数据链路已完成实机验收，客户端与系统验收仍在推进。

## 2. 仓库现状与目标态

| 领域 | 当前事实 | 目标态 | 处理原则 |
| --- | --- | --- | --- |
| MCU | STM32F103C8T6 | 保持 | 不更换主控 |
| 固件库 | STM32F10x Standard Peripheral Library | 可继续使用；HAL 仅作为备选迁移 | MQTT 改造不附带 HAL 重写 |
| 温湿度 | DHT11，PA5 | DHT11 可完成课设；SHT30 是精度升级项 | 先复用 DHT11 |
| 气体 | MQ135，PA1/ADC1 Channel 1，10 次均值 | 固定窗口滑动平均、校准后输出 ADC 与估算值 | 保留原始 ADC 以便校准 |
| 显示 | SSD1306，PB8/PB9 软件 I2C | 轮播数值、网络和报警状态 | 复用驱动，调整页面逻辑 |
| 报警 | PA4 LED、PA8/TIM1_CH1 蜂鸣器 | 断网自治；远程仅静音蜂鸣器 | 本地判断优先级最高；PB13 曾在方案中记为蜂鸣器，实机验收后确认实际接在 PA8 |
| 联网 | ESP8266 USART1，TCP 文本帧 | MQTT 连接 EMQX，JSON Payload | 分阶段迁移并保留回退验证 |
| Backend | Go 标准库路由骨架 | MQTT、持久化、复合预警、REST、WebSocket | 不改成 FastAPI/Spring Boot |
| 客户端 | KMP 模板 + 微信原生示例 | 相同需求与 API 的两套实现 | 内部实现保持独立 |

## 3. 总体架构

```text
DHT11 + MQ135
      │
      ▼
STM32F103C8T6
  ├─ 定时采样与滤波
  ├─ 本地阈值/突增判断
  ├─ OLED 轮播
  ├─ LED/蜂鸣器自治报警
  └─ 配置校验与 Flash 持久化
      │ USART1
      ▼
ESP8266 ── MQTT/TLS(部署允许时) ── EMQX
                                      │
                                      ▼
                              Go Backend
                         ├─ 遥测消费与入库
                         ├─ 复合火警状态机
                         ├─ 心跳/离线检测
                         ├─ REST API
                         └─ WebSocket 推送
                                      │
                         ┌────────────┴────────────┐
                         ▼                         ▼
                  KMP Client                WeChat Native
```

## 4. STM32 端设计

### 4.1 任务节拍

裸机主循环可采用毫秒 tick 驱动的非阻塞任务，不需要立即引入 RTOS：

- MQ135 ADC：每 100 ms 采样一次，维护 10 点环形窗口。
- DHT11：每 1 s 采样一次，遵守器件最小间隔。
- 本地安全判断：每次有效采样后执行，不等待网络。
- OLED：每 500 ms 刷新，每 2 s 切换页面。
- MQTT 遥测：正常每 5 s 上报；告警状态变化时立即补报。
- 网络状态机：持续推进，禁止用长时间阻塞重连拖停本地安全任务。

### 4.2 气体滑动平均

使用固定大小环形缓冲保存原始 ADC：

```c
sum -= samples[index];
samples[index] = new_adc;
sum += new_adc;
index = (index + 1U) % WINDOW_SIZE;
filtered_adc = sum / valid_sample_count;
```

窗口大小建议从 10 开始。必须同时保留 `gasAdcRaw` 与 `gasAdcFiltered`，估算浓度 `gasPpm` 只能在完成传感器预热、负载电阻确认和标定后用于定量展示。

### 4.3 本地报警优先级

本地报警条件至少包含：

1. 温度达到 `temperatureHighC`；
2. 滤波后气体值达到 `gasHighPpm`；
3. 可选快速通道：短窗口气体增量或温升速率达到紧急阈值。

网络连接、MQTT 发布和远程命令处理均不得改变本地告警判断。v2 不支持远程静音；蜂鸣器只对气体超限或突增按间歇节奏鸣响，温湿度及传感器故障仍保留本地状态显示和遥测，但不单独驱动蜂鸣器。

### 4.4 阈值持久化

STM32F103C8T6 没有内部 EEPROM，可使用保留 Flash 页模拟配置存储。配置记录建议包含：

```text
magic | schemaVersion | sequence | temperatureHighC | gasHighPpm | crc32
```

（以上为初版草案；实际落地格式把 `sequence` 实现为 `version`，并增加了湿度与上升阈值字段，以 `hardware/README.md`「阈值掉电保存」为准。）

采用双槽/双页和递增 sequence，写入新记录并校验成功后再使其生效。收到控制命令时先验证范围与版本，再写 Flash；写入失败继续使用上一次有效配置。避免每次遥测或滑块拖动都写 Flash，Backend 应对连续操作做合并或确认提交。

### 4.5 ESP8266 MQTT 路径

优先评估模块 AT 固件是否支持 MQTT 指令集。若支持，可使用 `AT+MQTTUSERCFG`、连接、订阅和发布相关指令；若不支持，再评估在 MCU 上实现轻量 MQTT 编解码。**实际选型**：因无法确认目标模组的 AT 固件版本，MQTT 指令集路径被排除，最终在 MCU 侧实现 MQTT 编解码，理由见 `hardware/README.md`「MQTT 接入设计」。无论采用哪条路径，都应复用现有 USART 中断接收、超时、重试和状态解析思路。

当前 TCP 文本协议不得在 MQTT 未经实机验证前删除。迁移验收至少覆盖冷启动、AP 不存在、密码错误、Broker 重启、断网恢复、下发重复命令和超长 Payload。

## 5. MQTT 与消息契约

首期按需求使用固定主题：

- 上报：`device/telemetry`
- 下发：`device/control`

Payload 中必须携带 `deviceId`，详细 Schema、QoS、保留策略、示例和命令确认见 [device-protocol.md](device-protocol.md)。若后续支持多租户或大量设备，再评审迁移到 `devices/{deviceId}/...`，不能静默修改。

## 6. Backend 设计

### 6.1 模块职责

初期保持单体 Go 服务和清晰边界，不预建复杂 DDD：

- MQTT ingress：校验版本、设备 ID、数值范围和消息时间。
- Telemetry store：写入遥测记录；首选 PostgreSQL，按时间与设备建立索引。
- Device state：维护最后消息时间、在线状态和最新遥测。
- Alert evaluator：计算气体突增与温升速率，驱动告警状态机。
- Command publisher：发布静音和阈值命令，关联 `requestId` 与设备确认。
- HTTP/WebSocket：遵守 OpenAPI 和实时消息契约。

数据库与 MQTT 客户端已选型：PostgreSQL（`postgres-dev` 容器，接入见 `backend/internal/store`）与 EMQX 5.8（`deploy/compose.yaml`，接入见 `backend/internal/mqtt`）。接口和算法保持纯 Go 单元测试，驱动层单独验证。

### 6.2 复合预警算法

对每台设备维护时间有序滑动窗口，例如最近 60 秒：

```text
gasRise = latest.filteredGas - median(first stable window)
temperatureRate = linearSlope(temperature, eventTime) × 60  // ℃/min
```

只有当 `gasRise >= gasRiseThreshold` 且 `temperatureRate >= temperatureRateThreshold`，并持续满足最小确认时长/样本数时，才进入 `fire_warning`。状态机建议为：

```text
normal → suspect → fire_warning → recovered
```

原方案含 `acknowledged` 状态；首期没有告警确认接口，保留它会形成无法产生的契约，故首期冻结范围不含该状态，留作二期（见 [device-protocol.md](device-protocol.md) FD-9 与 `docs/api/openapi.yaml` 的 `AlertState` 说明）。

算法必须处理乱序、重复、缺失和设备重启数据；使用设备时间参与趋势计算，同时保存服务端 `receivedAt`。每个告警事件记录起止时间、峰值、斜率、触发阈值和原始样本引用，便于解释和答辩。

### 6.3 在线判定

设备正常每 5 秒上报时，连续 3 个周期未收到有效遥测可标记为 `offline`，即默认 15 秒；阈值应配置化。恢复收到合法数据后标记 `online` 并产生状态事件。Broker 连接状态不能替代应用层最后遥测时间。

### 6.4 数据模型建议

- `devices`：设备元数据、最新状态、最后在线时间。
- `telemetry`：设备时间、接收时间、温湿度、原始/滤波气体值、报警和网络状态。
- `alert_events`：告警级别、状态、触发证据、确认与恢复时间。
- `device_thresholds`：当前期望值、版本、设备确认版本。
- `device_commands`：requestId、类型、Payload、发布/确认/超时状态。

## 7. API 与客户端

REST 与 WebSocket 路径见 [api/openapi.yaml](api/openapi.yaml)。两套客户端必须使用同一字段、单位和错误模型。

客户端页面按三屏最小闭环实现：

1. 仪表盘：在线状态、温湿度、气体安全等级、本地告警，折线图使用相同时间窗口。
2. 远程控制：静音开关与阈值滑块；提交后展示“等待设备确认”，不能把 HTTP 接受误当成设备已执行。
3. 告警历史：时间、等级、触发证据、确认/恢复状态。

微信原生端可使用适配微信小程序的 ECharts 组件；KMP 端共享数据解析、状态和业务规则，但微信 WXML/WXSS 不进入 `commonMain`。

## 8. 安全与可靠性

- 移除源码中的明文 Wi-Fi 密码、Broker 凭据和固定 IP，使用本地不入库配置或安全配网流程。
- 生产部署使用独立 MQTT 账号、最小主题 ACL；条件允许时启用 TLS。
- 控制命令包含唯一 `requestId`、版本与过期时间，设备按 requestId 去重。
- 所有外部 Payload 做长度、JSON、范围和枚举校验。
- API 在接入真实环境前补鉴权、授权、限流和审计日志。
- 本地报警默认安全失败：配置损坏时回退到编译期安全阈值。

## 9. 分阶段计划与验收

1. **契约阶段（已完成）**：事实文档、路由、OpenAPI、协议草案、Git 基线。
2. **硬件本地闭环（已完成）**：滑动平均、OLED 轮播、断网报警、Flash 配置单元与实机测试。
3. **MQTT 联调（已完成）**：EMQX、遥测发布、控制订阅、命令确认和断线恢复。
4. **Backend 数据链路（已完成）**：消费、校验、PostgreSQL、在线状态、算法测试。
5. **客户端（进行中）**：先原生微信 baseline，再 KMP 方案；按同一验收脚本实现。
6. **系统验收（进行中）**：断网自治、误报抑制、端到端延迟、历史查询、远程控制确认和两客户端对照——其中断网自治与远程控制确认已取得实机证据（2026-09-22），误报抑制与两客户端对照未做。

所有成员与 Agent 按根 `AGENTS.md` 使用 Multica CLI 同步对应 issue 的进度、验证、风险和跨模块变更。
