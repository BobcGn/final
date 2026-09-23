# Backend API Detailed Contract

状态：`v2.0.0`（已冻结，与 [`../../docs/api/openapi.yaml`](../../docs/api/openapi.yaml) 同步；远程静音已删除）。本文是 Go Backend 的开发者接口说明；机器可读事实源是 OpenAPI 文件。

**当前实现状态**：`/healthz` 与全部 `/api/v1` 路由、`/ws/v1` 实时流均已实现。数据来自 MQTT 接入的设备遥测；未配置 `MQTT_BROKER_URL` 时进程仍可启动，但没有设备数据，控制命令会返回 `503 broker_unavailable`。

## 1. Conventions

### 1.1 Base URLs

```text
REST:      /api/v1
WebSocket: /ws/v1
Health:    /healthz
```

默认监听 `:8080`，由 `BACKEND_ADDR` 覆盖。生产域名、TLS 终止与反向代理尚未确定。

### 1.2 Media Type and Encoding

- 请求与响应使用 `application/json; charset=utf-8`。
- 时间使用 UTC RFC 3339，秒精度，例如 `2026-09-24T10:40:30Z`。
- MQTT 设备时间使用 Unix 毫秒。设备时钟未同步时 `timestamp` 为 `null`，Backend 始终填充 `receivedAt`，并以它作为排序与告警窗口的事件时间。
- 温度单位 °C，湿度 %RH，气体 ADC 为 0–4095 的 12 位码，估算浓度单位 ppm。
- JSON 字段使用 lower camel case。**未知请求字段一律拒绝**，避免拼写错误被静默忽略。

### 1.3 Authentication and Authorization

`AUTH_MODE` 选择鉴权方式：

| Mode | Behavior |
| --- | --- |
| `none` | 不校验凭据。仅用于本地开发；启动时记录 WARN。 |
| `bearer` | 要求 `Authorization: Bearer <token>`，token 在 `AUTH_TOKENS` 中配置。 |

`AUTH_TOKENS` 格式为 `token:actor,token:actor`。**actor 是必填的**，它写入控制命令的审计字段；没有 actor 的 token 会导致启动失败。token 比较使用常数时间比较。

`AUTH_MODE=none` 时控制类路由对任何能访问端口的人开放，**不得**部署到不可信网络。设备访问范围隔离（哪些用户可访问哪些设备）与查看/改阈值分权尚未实现，属于后续工作。

### 1.4 Common Headers

| Header | Direction | Required | Meaning |
| --- | --- | --- | --- |
| `Authorization` | Request | `AUTH_MODE=bearer` 时必需 | Bearer access token |
| `Content-Type` | Both | 有请求体时必需 | `application/json` |
| `X-Request-ID` | Both | 可选 | 客户端 trace id，回显在错误响应中 |
| `Idempotency-Key` | 控制请求 | 必需 | 防止重复命令，1–64 字符 |

缺失 `Idempotency-Key` 返回 `400 invalid_request`。相同设备、相同 key、相同 Payload 的重复请求返回同一命令且**不会再次发布**；相同 key 配不同 Payload 返回 `409 version_conflict`。幂等键按设备隔离，不同设备可以使用同一个 key。

### 1.5 Device ID

路径参数 `deviceId` 必须匹配：

```regex
^[A-Za-z0-9_-]{1,32}$
```

必须与 MQTT Payload 中的 `deviceId` 一致。该值同时是 MQTT `clientId` 与数据库主键，因此三类事实源中的格式必须相同。

设备在**首次收到合法遥测时自动登记**。在收到第一条有效遥测之前，`GET /status` 返回 `404 device_not_found`。

### 1.6 Error Envelope

所有业务错误采用同一结构：

```json
{
  "error": {
    "code": "invalid_threshold",
    "message": "domain: invalid threshold: gasHighPpm 5000 outside [1,999]",
    "requestId": "01K5H2YRG92V0V6A3EJ8VQPW03",
    "details": {
      "field": "gasHighPpm"
    }
  }
}
```

`requestId` 仅回显客户端传入的 `X-Request-ID`；服务端不会伪造 trace id。`details` 仅出现在可结构化描述的错误上。

错误码（冻结，与 OpenAPI `ErrorCode` 枚举一致）：

| HTTP | Code | Scenario |
| --- | --- | --- |
| 400 | `invalid_request` | JSON、参数、时间范围、游标、缺失 Idempotency-Key |
| 401 | `unauthenticated` | 缺少或无效凭据 |
| 403 | `forbidden` | 无设备或控制权限（当前无分权模型，保留） |
| 404 | `device_not_found` | 设备从未上报有效遥测 |
| 404 | `command_not_found` | 该设备下不存在该 requestId |
| 409 | `version_conflict` | 幂等键冲突，或阈值版本未前进 |
| 422 | `invalid_threshold` | 阈值超出设备允许范围 |
| 500 | `internal_error` | 未预期服务端错误 |
| 503 | `rate_limited` | 实时连接数达到上限（仅 WebSocket 满员，见 §11） |
| 503 | `broker_unavailable` | 控制命令无法发布到 Broker |
| 504 | `device_ack_timeout` | 等待设备确认超时（保留；当前以命令状态 `timed_out` 表达） |

## 2. Route Summary

| Method | Route | Purpose | Current |
| --- | --- | --- | --- |
| GET | `/healthz` | 进程存活检查 | Implemented |
| GET | `/api/v1/devices/{deviceId}/status` | 设备在线与告警状态 | Implemented |
| GET | `/api/v1/devices/{deviceId}/telemetry/latest` | 最新有效遥测 | Implemented |
| GET | `/api/v1/devices/{deviceId}/telemetry` | 历史遥测分页 | Implemented |
| GET | `/api/v1/devices/{deviceId}/alerts` | 历史告警分页 | Implemented |
| GET | `/api/v1/devices/{deviceId}/thresholds` | 期望与设备确认阈值 | Implemented |
| PUT | `/api/v1/devices/{deviceId}/thresholds` | 校验并下发阈值 | Implemented |
| GET | `/api/v1/devices/{deviceId}/commands/{requestId}` | 查询控制命令状态 | Implemented |
| GET | `/ws/v1/devices/{deviceId}/telemetry` | 实时 WebSocket 流 | Implemented |

`contract_test.go` 会同时比对路由表与 OpenAPI，任一侧缺失都会导致测试失败。

## 3. Health

### GET `/healthz`

进程级 liveness。**不**检查数据库、EMQX 或设备状态，因此依赖故障时仍返回 200。该路由不需要鉴权，因为编排探针不应需要凭据。后续如需要 readiness，新增独立 `/readyz`，不改变本路由语义。

```json
{ "status": "ok" }
```

## 4. Device Status

### GET `/api/v1/devices/{deviceId}/status`

```json
{
  "deviceId": "MCU001",
  "connectivity": "online",
  "alarmState": "suspect",
  "localAlarm": true,
  "lastSeenAt": "2026-09-24T10:40:30Z",
  "offlineAfterSeconds": 15,
  "thresholdVersion": { "desired": 4, "confirmed": 4 }
}
```

`connectivity`：`online | offline | unknown`，由 Backend 依据最后一条有效遥测的**到达时间**计算，不使用 Broker 连接状态，也不使用设备自报的 `network` 字段。

`alarmState`：

- `normal`：无复合预警，且记录中没有告警事件；
- `suspect`：部分条件成立，等待确认；
- `fire_warning`：气体突增与温升速率同时满足且达到最小样本数/时长；
- `recovered`：最近一次事件已结束且未再次触发。`recovered` 是有意保持的状态：它与 `normal` 都表示"当前未告警"，区别在于设备是否发生过事件。首期**没有** `acknowledged`，因为不存在告警确认接口，保留该状态会形成无法产生的契约。

`localAlarm` 来自设备本地判断，与 Backend 复合状态不是同一概念，两者可能不一致。

`lastSeenAt` 来自 liveness tracker；设备从未上报时为 `null`。

## 5. Latest Telemetry

### GET `/api/v1/devices/{deviceId}/telemetry/latest`

返回最近一条通过 Schema、范围与权限校验的遥测。没有有效遥测时返回 `404`，不使用全零对象。

```json
{
  "deviceId": "MCU001",
  "bootId": "9f3ac21b",
  "sequence": 42,
  "timestamp": "2026-09-24T10:40:29Z",
  "receivedAt": "2026-09-24T10:40:30Z",
  "temperatureC": 28.0,
  "humidityRh": 61.0,
  "gasAdcRaw": 1350,
  "gasAdcFiltered": 1328,
  "gasPpm": 25.0,
  "gasCalibrated": false,
  "localAlarm": true,
  "alarmCauses": ["gas_high"],
  "network": "online",
  "sensorFault": false
}
```

`timestamp` 在设备未同步时钟时为 `null`；`receivedAt` 永远由 Backend 填充。`gasCalibrated=false` 时 `gasPpm` 是未标定估算值，客户端应优先展示 ADC 安全分级。

## 6. Historical Telemetry

### GET `/api/v1/devices/{deviceId}/telemetry`

| Name | Type | Required | Rules |
| --- | --- | --- | --- |
| `from` | RFC 3339 | No | 含端点；默认 `now-1h` |
| `to` | RFC 3339 | No | 不含端点；默认 `now` |
| `limit` | integer | No | 1–1000；默认 200 |
| `cursor` | string | No | 上一页返回的不透明游标 |
| `order` | enum | No | `asc` 或 `desc`；默认 `asc` |

**游标规则**：出现 `cursor` 时**必须**同时重复第一页的 `from` 与 `to`，否则返回 `400`。游标内绑定了 `(deviceId, from, to, order)`，不匹配时同样返回 `400`，不会静默返回另一条序列的分页。`limit` 允许在翻页之间改变。

排序与游标使用稳定组合键 `(eventTime, bootId, sequence)`。`eventTime` 在设备时钟未同步时取 `receivedAt`，因此排序键永不为空。同一时间戳的多条记录不会跳页或重复。

最大查询跨度 31 天；更长区间应使用聚合接口或导出任务。

```json
{
  "items": [ { "deviceId": "MCU001", "sequence": 41, "...": "..." } ],
  "nextCursor": "eyJ0IjoxNzkw..."
}
```

`nextCursor` 为 `null` 表示已是最后一页。

## 7. Alert Events

### GET `/api/v1/devices/{deviceId}/alerts`

查询参数：`from`、`to`、`limit`、`cursor` 与历史遥测一致，另支持：

| Name | Type | Meaning |
| --- | --- | --- |
| `state` | enum | 按 `normal/suspect/fire_warning/recovered` 过滤 |
| `active` | boolean | 仅返回尚未结束的事件 |

```json
{
  "items": [
    {
      "id": "01K5H7T7T4J2MYE7Y0BR1ZBQ0Q",
      "deviceId": "MCU001",
      "state": "fire_warning",
      "startedAt": "2026-09-24T10:18:00Z",
      "endedAt": null,
      "evidence": {
        "gasAdcRise": 600,
        "gasAdcRiseThreshold": 150,
        "temperatureRateCPerMinute": 6.0,
        "temperatureRateThresholdCPerMinute": 3.0,
        "sampleCount": 8,
        "windowSeconds": 35
      }
    }
  ],
  "nextCursor": null
}
```

事件在触发时写入证据，**不会**在读取时重算。客户端不能用当前实时值反推历史告警原因。气体项以 ADC 码而非 ppm 表示：估算浓度未标定、`gasPpm` 可能缺失，而增量本身在未标定时仍然有效。

### 7.1 evidence 字段名必须是 lowerCamelCase

上例中的六个键名与 `../../docs/api/openapi.yaml` 的 `AlertEvidence` 一致，**必须**原样使用：

`gasAdcRise`、`gasAdcRiseThreshold`、`temperatureRateCPerMinute`、`temperatureRateThresholdCPerMinute`、`sampleCount`、`windowSeconds`。

约束与边界：

- **不接受 Go 导出名**（`GasAdcRise`、`SampleCount`…）。本服务曾把 `domain.AlertEvidence` 直接序列化到线上，输出的就是 PascalCase，任何按契约实现的客户端都解析不了。现已改为在 REST 与事件两条边界各用带显式 `json` 标签的 DTO（`internal/api/dto.go` 的 `alertEvidenceResource`、`internal/events/events.go` 的 `AlertEvidenceData`）。
- **不保留 PascalCase 别名**，两套键名不会同时出现。历史上的 PascalCase 输出**不属于兼容契约**，不构成兼容性承诺。
- 事实源是 `../../docs/api/openapi.yaml`；实现不得反过来去改契约以迎合错误的输出。
- 该约束由测试强制：`internal/api/api_test.go` 的 `TestAlertsListingAndFilters` 对**原始响应体**断言六个 camelCase 键存在、六个 PascalCase 键不存在（只解码到 Go struct 是看不出这件事的，因为 `encoding/json` 对键名匹配较宽松）；`contract_test.go` 把实现的键名集合与 `AlertEvidence.properties` 对齐。
- `AlertEvidence.required` 只列了 `gasAdcRise`、`temperatureRateCPerMinute`、`sampleCount` 三项。这是「读者必须容忍其缺失」的保守声明，不是本服务的保证：本服务**六项总是输出**（`domain.AlertEvidence` 无可选成员，触发时即全部写入）。客户端应把另外三项视为可选来解析。见 `TestAlertEvidenceRequiredMatchesWhatTheBackendGuarantees`。

每台设备**至多一个未结束事件**（数据库部分唯一索引强制）。

## 8. Thresholds

### GET `/api/v1/devices/{deviceId}/thresholds`

```json
{
  "desiredVersion": 4,
  "confirmedVersion": 3,
  "temperatureHighC": 30.0,
  "humidityHighRh": 80.0,
  "gasHighPpm": 80.0,
  "updatedAt": "2026-09-24T10:10:00Z",
  "confirmationState": "pending"
}
```

设备从未被配置时返回**编译期默认值**（`hardware/STM32_Project1/User/app_config.h`：30 °C / 80 %RH / 20 ppm）与 `desiredVersion: 1`、`confirmedVersion: null`。版本 1 定义为"编译期默认"，因此设备永远不会报告版本 0。

`confirmationState`：`confirmed`（设备确认版本 ≥ 期望版本）| `pending` | `rejected`（保留）| `timed_out`（保留）。当前实现只产生 `confirmed` 与 `pending`；`rejected` 与 `timed_out` 由命令资源表达，避免同一事实两处不一致。

### PUT `/api/v1/devices/{deviceId}/thresholds`

Headers：

```http
Content-Type: application/json
Idempotency-Key: 01K5H0PN0M1N9NB8B7RBTVWT8P
```

```json
{ "temperatureHighC": 30.0, "humidityHighRh": 80.0, "gasHighPpm": 80.0 }
```

三个字段**全部必填**且不允许额外字段。范围：

- `temperatureHighC`：0–80 °C
- `humidityHighRh`：0–100 %RH
- `gasHighPpm`：1–999 ppm（与 `gasPpm` 同单位的估算值）

`202 Accepted`：

```json
{
  "requestId": "01K5H0PN0M1N9NB8B7RBTVWT8P",
  "status": "pending",
  "desiredVersion": 4,
  "expiresAt": "2026-09-24T10:21:30Z"
}
```

**HTTP 202 只表示 Backend 接受并已发布命令，不表示设备已写入 Flash。** 客户端必须等待设备确认或超时，可通过实时流或 `GET /commands/{requestId}` 获知。

特殊错误：`409 version_conflict`（幂等键冲突或版本未前进）、`422 invalid_threshold`、`503 broker_unavailable`（未成功发布，命令状态为 `publish_failed`）。

超时不回滚 `desiredVersion`，而是把命令标记为 `timed_out`，供用户重试或诊断。

### 阈值版本与持久化

- 新版本 = 当前期望版本 + 1，严格递增。
- 命令与期望阈值在**同一事务**中写入，避免出现"版本已前进但没有命令"的窗口。
- 设备只在其 Flash 校验写入成功后更新生效版本并回执 `applied`。
- 设备在遥测中持续上报 `thresholdVersion`；Backend 据此确认版本，即使原始 ACK 丢失也能收敛。

设备离线时控制命令短期排队并带 `expiresAt`；过期后**绝不**在重连时执行，只会被标记为 `timed_out`。

## 9. Command Status

### GET `/api/v1/devices/{deviceId}/commands/{requestId}`

```json
{
  "requestId": "01K5H0PN0M1N9NB8B7RBTVWT8P",
  "deviceId": "MCU001",
  "type": "set_thresholds",
  "state": "applied",
  "acceptedAt": "2026-09-24T10:20:30Z",
  "completedAt": "2026-09-24T10:20:32Z",
  "expiresAt": "2026-09-24T10:21:30Z",
  "desiredVersion": 4,
  "confirmedVersion": 4,
  "errorCode": null
}
```

该路由存在的理由：客户端在 ACK 到达时可能正断线。没有它，`accepted` 与 `applied` 无法区分，超时结果也不可见。

`state` 生命周期：

```text
accepted → published → applied
                    ↘ rejected
                    ↘ expired
                    ↘ duplicate
                    ↘ failed
          ↘ timed_out
          ↘ publish_failed
```

- `accepted`、`published` **不是**终态：设备尚未回应。
- `applied`、`rejected`、`expired`、`duplicate`、`failed` 来自设备 ACK。
- `timed_out`（在 `expiresAt` 前无 ACK）与 `publish_failed`（Broker 拒绝）是 Backend 的终态结论。
- 终态**不会**被后续迟到的 ACK 改写：客户端可能已经看到过该结果。
- 重复的相同 ACK 是幂等的（MQTT QoS 1 至少一次投递）。

## 10. WebSocket Telemetry

### GET `/ws/v1/devices/{deviceId}/telemetry`

标准 WebSocket Upgrade。鉴权失败在 Upgrade **之前**返回 HTTP 错误；订阅未上报过的设备返回 `404`（而不是给客户端一条永远为空的流）。连接数达到 `MAX_WS_CLIENTS` 时返回 `503 rate_limited`。

服务端事件 Envelope：

```json
{
  "type": "telemetry.updated",
  "eventId": "01K5H8E4SXGPHCQQY6H11XK9RQ",
  "occurredAt": "2026-09-24T10:40:30Z",
  "deviceId": "MCU001",
  "data": { "sequence": 42, "temperatureC": 28.0, "gasAdcFiltered": 1328, "localAlarm": true }
}
```

事件类型（冻结）：

| Type | Meaning | `data` 内容 |
| --- | --- | --- |
| `telemetry.updated` | 新的有效遥测 | 遥测字段 |
| `device.status_changed` | online/offline/unknown 变化 | `connectivity`、`lastSeenAt`、`alarmState` |
| `alert.state_changed` | 复合预警状态变化 | 告警事件字段与证据 |
| `command.status_changed` | 控制命令确认、拒绝或超时 | 命令状态字段 |
| `thresholds.confirmed` | 设备确认阈值版本 | `confirmedVersion` |

连接要求：

- 服务端每 30 秒发送 ping，客户端必须回应 pong；两次未回应即断开。
- 慢客户端采用**有界队列（64 条）**；队列满时断开该连接，要求客户端用 REST 补数。服务端不会无限缓冲，也不会让遥测接入依赖客户端的读取速度。
- 实时流只承载增量，**不保证历史补发**。重连后客户端先请求 latest/history，再订阅。
- 同一 `eventId` 可用于去重；客户端不能假定事件绝不重复。
- 默认只允许同源 Origin；非浏览器客户端（无 Origin 头）允许连接。

## 11. 复合火情预警算法

参数（可由 dotenv 中的 `ALERT_*` 字段设置，见 §13）：

| Parameter | Default | Meaning |
| --- | --- | --- |
| `ALERT_WINDOW_SECONDS` | 60 | 滑动窗口长度 |
| `ALERT_MIN_SAMPLES` | 6 | 确认火警所需最小样本数（**至少 2**） |
| `ALERT_MIN_DURATION_SECONDS` | 20 | 确认火警所需最小时间跨度 |
| `ALERT_GAS_RISE_ADC` | 150 | 判定为气体突增的 ADC 增量 |
| `ALERT_TEMP_RATE_C_PER_MIN` | 3.0 | 判定为快速温升的斜率 |
| `ALERT_RECOVERY_HOLD_SECONDS` | 30 | 条件持续满足多久才结束事件 |

计算方式：

```text
baseline      = median(窗口内最早的 max(3, len/3) 个 gasAdcFiltered)
gasAdcRise    = 最新 gasAdcFiltered - baseline
temperatureRate = 对 (eventTime, temperatureC) 做最小二乘拟合的斜率 × 60   // °C/min
```

判据（两者同时满足才可能升为 `fire_warning`）：

```text
gasSurge  = gasAdcRise >= ALERT_GAS_RISE_ADC
rapidRise = temperatureRate >= ALERT_TEMP_RATE_C_PER_MIN
confirmed = gasSurge && rapidRise && sampleCount >= MIN_SAMPLES && span >= MIN_DURATION
```

状态转移：`normal → suspect → fire_warning → recovered`。

必须遵守的规则：

1. **单次采样绝不产生火警**：确认需要最小样本数与最小时长；`ALERT_MIN_SAMPLES < 2` 会被配置校验拒绝。
2. **两因子缺一不可**：只有气体或只有温升都停留在 `suspect`。
3. **恢复需要保持**：任一条再次成立都会重置恢复计时，避免阈值附近抖动导致事件反复开合。
4. **设备重启重置窗口**：`bootId` 变化时丢弃整段趋势，因为序列号与传感器预热状态都重新开始。
5. **乱序样本进入窗口但不驱动状态**：迟到样本参与趋势计算，但不会改写当前设备快照或触发状态变化。
6. **重复样本只计一次**：`(deviceId, bootId, sequence)` 重复的报文在入库前去重，不进入告警窗口。

`gasAdcRise` 使用中位数基线：单个离群读数不会像使用均值那样把基线抬高并制造"突增"。

## 12. Configuration

进程启动时直接读取 `backend/.env.local`。已提交的 `backend/.env.example`
是完整模板，`.env.local` 保存本机密码与地址并被 Git 忽略。可用
`BACKEND_CONFIG_FILE` 选择另一个 dotenv 文件，但其他 shell 环境变量不覆盖文件内容。

| Field | Default | Meaning |
| --- | --- | --- |
| `BACKEND_ADDR` | `:8080` | HTTP 监听地址 |
| `DATABASE_URL` | 空 | PostgreSQL DSN。**为空时使用内存存储，重启丢失全部数据，仅用于开发** |
| `MQTT_BROKER_URL` | 空 | Broker 地址，`host:port`；接受并可去除 `tcp://`、`mqtt://`、`ssl://`、`tls://`、`mqtts://` 前缀。为空时禁用设备接入与控制下发 |
| `MQTT_TLS` | `false` | 启用 TLS（最低 TLS 1.2） |
| `MQTT_USERNAME` / `MQTT_PASSWORD` | 空 | Broker 凭据 |
| `MQTT_CLIENT_ID` | `lab-backend` | MQTT clientId，1–23 字符 |
| `MQTT_KEEPALIVE_SECONDS` | `30` | MQTT keep-alive |
| `AUTH_MODE` | `none` | `none` 或 `bearer` |
| `AUTH_TOKENS` | 空 | `token:actor,token:actor` |
| `DEVICE_ALLOWLIST` | 空 | 允许的 `deviceId` 列表。为空时接受任何格式合法的设备号 |
| `OFFLINE_AFTER_SECONDS` | `15` | 判定离线所需的静默时长，至少为 3 个上报周期 |
| `COMMAND_TTL_SECONDS` | `60` | 控制命令有效期 |
| `SWEEP_INTERVAL_SECONDS` | `5` | 离线判定与命令过期的扫描周期 |
| `MAX_WS_CLIENTS` | `128` | 实时连接数上限 |
| `LOG_LEVEL` | `info` | `debug`、`info`、`warn`、`error` |
| `ALERT_*` | 见 §12 | 复合预警参数 |

dotenv 支持空行、整行 `#` 注释、`export KEY=VALUE`、单引号和双引号值。
**未知键、重复键、格式错误或非法值一律导致启动失败**，不会静默回退。
启动日志会记录脱敏后的配置，并对 `AUTH_MODE=none`、内存存储、未配置
Broker 三种情况分别打 WARN。

`DEVICE_ALLOWLIST` 存在的理由：首期主题固定为 `device/telemetry`，主题本身无法表达"哪个设备允许发布"，因此设备身份只能由 Payload 声明。在按设备凭据与 ACL 就位之前，该白名单是应用层的替代措施。

## 14. 数据库

PostgreSQL。Schema 由 `internal/store/migrations/` 下的 SQL 定义，进程启动时自动应用（幂等）。

要点：

- `telemetry` 的列名带单位；`event_time` 与 `received_at` 分开存储；`UNIQUE (device_id, boot_id, sequence)` 承载去重；`(device_id, event_time, boot_id, sequence)` 索引同时服务查询与游标。
- `alert_events` 用部分唯一索引 `WHERE ended_at IS NULL` 保证每设备至多一个未结束事件。
- `device_commands` 用部分唯一索引 `WHERE idempotency_key <> ''` 保证幂等键按设备唯一。
- `device_thresholds` 的 `desired_version` 与 `confirmed_version` 分列，确认只能前进（`GREATEST`）。
- `devices` 是派生缓存，由遥测与命令流量驱动。

## 15. 一致性与命令生命周期

遥测、设备状态、告警与控制结果之间存在短暂最终一致性。**API 不得把 Backend 期望值冒充设备确认值**：`desiredVersion` 与 `confirmedVersion` 分列，`Thresholds.confirmationState` 只产生实现真正能产生的取值。

控制命令记录保存：requestId、类型、Payload、幂等键、操作者（actor）、接受/发布/完成时间、期望与确认版本、失败原因。

## 16. 监控与诊断

进程以 JSON 结构化日志输出到 stdout，关键事件包括：启动配置（脱敏）、MQTT 连接与重连、消息被拒绝（带 topic 与原因）、离线扫描失败、命令过期数量。

拒绝计数按稳定的原因分类（`malformed_json`、`unknown_field`、`missing_field`、`schema_unsupported`、`out_of_range`、`device_mismatch`、`device_not_allowed` 等），可以在不解析日志正文的情况下判断是某一种系统性问题还是偶发噪声。

## 17. Test Requirements Per Route

每条路由至少具备：

1. 方法与路径匹配测试；
2. 合法请求/响应 Schema 测试；
3. 非法 deviceId、JSON、Query、游标与范围测试；
4. 401 权限边界测试；
5. 数据层或 Broker 失败映射测试（`503 broker_unavailable`）；
6. 幂等、重复、超时与状态机测试（控制路由）；
7. MQTT 与数据库集成测试；
8. WebSocket Upgrade、消息、未授权、未知设备与慢消费者测试。

Backend 统一质量命令：

```sh
go fmt ./...
go vet ./...
go test -race -coverprofile=coverage.out ./...
go tool cover -func=coverage.out
```

PostgreSQL 集成套件由 `TEST_DATABASE_URL` 启用；未设置时跳过（在 CI 中必须设置，否则该套件的覆盖为 0）。内存实现与 PostgreSQL 实现跑**同一套 conformance 测试**（`internal/store/conformance_test.go`），避免两套语义漂移。

覆盖率门槛为可测试包总行覆盖率 ≥ 80%，新增/修改核心逻辑目标 ≥ 90%。覆盖率只是门槛；错误语义、并发安全与关键集成链路仍需独立验证。

## 18. Change Process

修改路由或字段时必须在同一 PR 中：

1. 更新本文件；
2. 更新 `../../docs/api/openapi.yaml`；
3. MQTT 字段变化时更新 `../../docs/device-protocol.md`；
4. 更新 Handler、模型和测试；
5. 通知 Hardware、KMP、微信端负责人；
6. 在 Multica issue 记录兼容性、迁移和版本策略。

`contract_test.go` 会核对路由表、错误码、告警状态、告警原因、命令状态与实时事件类型是否与 OpenAPI 一致；只改一侧会导致测试失败。
