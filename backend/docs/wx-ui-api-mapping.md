# 微信小程序界面 ↔ Backend API 对照表

> 面向对象：Backend / 微信端开发者。用于核对「界面上的每个区块吃哪条接口、哪个字段」。
> 事实源：[`api.md`](api.md)、[`../../docs/api/openapi.yaml`](../../docs/api/openapi.yaml)（v2.0.0）
> 前端实现：`client-wx-native/`，数据经 `services/device.js`（REST 底层网关）与 `services/monitoring.js`（对齐 KMP 业务门面）驱动。
> 当前状态：Backend 全部路由已实现；前端可通过 `config/env.js` 的 `useMock` 开关在本地 Mock 与真实后端间无缝切换。
> 架构对齐：与 `client-kmp` 方案保持严格一致——采用 3000ms 原子快照轮询模型（REST 驱动），完全移除 WebSocket 与远程静音能力。

---

## 1. 总览：8 条业务路由 ↔ 前端方法 ↔ 使用页面

| # | 接口 | 前端方法 | 使用页面 | 后端现状 | 前端现状 |
|---|---|---|---|---|---|
| 1 | `GET /healthz` | 未接入 | — | Implemented | — |
| 2 | `GET /api/v1/devices/{id}/status` | `monitoring.loadDashboard()` / `deviceService.getStatus()` | 监控页 | Implemented | **已接入**（原子快照） |
| 3 | `GET /api/v1/devices/{id}/telemetry/latest` | `monitoring.loadDashboard()` / `deviceService.getLatestTelemetry()` | 监控页 | Implemented | **已接入**（404 视为空态） |
| 4 | `GET /api/v1/devices/{id}/telemetry` | `monitoring.loadTrendSamples()` / `deviceService.getTelemetryHistory()` | 趋势页 | Implemented | **已接入**（order=desc 倒序取页后本地反转升序） |
| 5 | `GET /api/v1/devices/{id}/alerts` | `monitoring.loadAlerts()` / `deviceService.getAlerts()` | 告警页 | Implemented | **已接入**（全量拉取后本地筛选） |
| 6 | `GET /api/v1/devices/{id}/thresholds` | `monitoring.loadSettings()` / `deviceService.getThresholds()` | 设置页 | Implemented | **已接入** |
| 7 | `PUT /api/v1/devices/{id}/thresholds` | `monitoring.updateThresholds()` / `deviceService.putThresholds()` | 设置页保存 | Implemented | **已接入**（三字段必填 + 幂等键） |
| 8 | `GET /api/v1/devices/{id}/commands/{requestId}` | `monitoring.awaitCommandOutcome()` / `deviceService.getCommandStatus()` | 设置页 | Implemented | **已接入**（轮询 10 × 1.5s 确认 applied） |

> 路径中的 `{id}` 固定使用 `MCU001`（匹配 `^[A-Za-z0-9_-]{1,32}$`）。
> `ws/v1` 虽然在后端存在，但按 KMP 方案不采用，保持双端刷新策略与生命周期完全一致。
> 远程静音能力已根据 OpenAPI v2.0.0 彻底废除，`POST .../commands/mute` 路由返回 404，前端无静音按钮。

---

## 2. 监控页（实时监控）逐块映射

数据入口：`pages/dashboard/dashboard.js`
- 刷新机制：每 **3000 ms** 执行一次 `loadDashboard()`，并发获取 **接口 2 + 接口 3** 组成的原子快照。
- 生命周期：`onShow` 启动轮询，`onHide` / `onUnload` 销毁定时器。
- 并发保护：上一轮快照未返回时跳过本轮；偶发网络抖动写入 `error`，不中断后续轮询。
- 404 语义：最新遥测接口 404 视为空态（`hasData = false`），正常呈现设备状态与空态提示，非系统错误。

### 2.1 页头

| UI 元素 | 接口 | 字段 |
|---|---|---|
| 「机房环境总览 / 智慧机房 · 实时动环监测」 | 无 | 静态文案 |

### 2.2 系统风险状态卡

| UI 元素 | 接口 | 字段 / 取值 | 说明 |
|---|---|---|---|
| 小标题「系统风险状态」 | 无 | 静态 | — |
| 圆点颜色 + 大字 | **接口 2** | `alarmState`：`normal｜suspect｜fire_warning｜recovered` | 枚举严格对齐（首期无 `acknowledged`） |
| 右侧胶囊 | **接口 2** | `connectivity`：`online -> 在线｜offline -> 离线｜unknown -> 未知` | 离线显示灰色，在线显示薄荷绿 |
| 下方说明文字 | **接口 2** | 派生说明：环境正常 / 疑似异常 / 火情预警 / 指标已恢复 | 由展示层统一派生 |

### 2.3 实时数据三卡（T / H / G）

| UI 元素 | 接口 | 字段 | 换算与精度（对齐 KMP） |
|---|---|---|---|
| 温度数字 | **接口 3** | `temperatureC` | `reading()` 整数取整（half-up），例如 `26` |
| 湿度数字 | **接口 3** | `humidityRh` | `reading()` 整数取整（half-up），例如 `60` |
| 气体数字 | **接口 3** | `gasPpm` | `reading()` 整数取整；**null 必须展示为 `--`（未测量），绝不能显示为 0** |
| 温度进度条宽度 | **接口 3** | `temperatureC` | 契约量程 0–80 °C，clamped 0–100% |
| 湿度进度条宽度 | **接口 3** | `humidityRh` | 契约量程 0–100 %RH，clamped 0–100% |
| 气体进度条宽度 | **接口 3** | `gasPpm` | 契约量程 1–999 ppm，clamped 0–100%；null 为 0% |
| 区块右上角提示 | 无 | 静态文案 | `每 3 秒同步` |

### 2.4 设备状态卡

| UI 元素 | 接口 | 字段 | 说明 |
|---|---|---|---|
| 设备编号 | **接口 2** | `deviceId` | `MCU001` |
| 在线胶囊 | **接口 2** | `connectivity` | 在线 / 离线 |
| 本地报警 | **接口 3**（缺省回退 **接口 2**） | `localAlarm` | `报警中`（danger）/ `正常`（mint） |
| 声光提示 | **接口 3**（缺省回退 **接口 2**） | 由 `localAlarm` 派生 | `localAlarm ? '报警策略生效' : '待机'`（全链路无远程静音） |
| 更新时间 | **接口 3** | `receivedAt`（缺省回退 `status.lastSeenAt`） | RFC 3339 字符串 |

---

## 3. 趋势页（历史趋势）逐块映射

数据入口：`pages/trends/trends.js` → `services/monitoring.js:loadTrendSamples()` → **接口 4**

| UI 元素 | 接口 | 参数 / 字段 | 说明 |
|---|---|---|---|
| 近1小时 / 近6小时 / 近24小时 | **接口 4** | 窗口 key → `from = now - N 小时`、`to = now`、`limit = 200`、`order = desc` | 倒序拉取保证保留最靠近当下的样本 |
| 样本升序反转 | — | 客户端本地 `rawItems.slice().reverse()` | 统计摘要与 Canvas 折线图**共用同一批升序样本** |
| 共 N 条样本 | **接口 4** | `items.length` | 实际统计样本数 |
| 温度/湿度/气体统计（最低/平均/最高） | **接口 4** | 前端 `summarizeMetric()` | 排除 null 样本，`reading()` 整数取整 |
| 峰值时刻 | **接口 4** | 最大值样本对应 `clockText(receivedAt)` | `HH:mm:ss` 格式 |
| 气体统计提示 | **接口 4** | 仅当有效气体样本数 < 总样本数时展示 | `N 条样本没有已校准气体读数，未计入气体统计` |
| 曲线区（原生 Canvas 2D） | **接口 4** | `buildChartGeometry()` + `drawTrendChart()` | 独立量程缩放、气体 null 打断线段、双重防竞态保护 |
| 失败重试与空态 | **接口 4** | 独立于统计卡片渲染 | 网络失败展示错误信息与「点此重试」；空数据展示「暂无历史数据」 |

---

## 4. 告警页（告警记录）逐块映射

数据入口：`pages/alerts/alerts.js` → `services/monitoring.js:loadAlerts()` → **接口 5**

| UI 元素 | 接口 | 字段 | 说明 |
|---|---|---|---|
| 筛选胶囊 | **接口 5** | 本地 `activeFilter` 过滤 | 选项：`全部 / 火情 / 疑似 / 已恢复`（**无 acknowledged**） |
| 状态胶囊与状态点 | **接口 5** | `state` | `fire_warning 火情预警(danger)`、`suspect 疑似异常(warning)`、`recovered 已恢复(info)`、`normal 正常(mint)` |
| 开始时间 | **接口 5** | `startedAt` | 时钟格式 `clockText()` |
| 气体上升 / 触发阈值 | **接口 5** | `evidence.gasAdcRise` / `gasAdcRiseThreshold` | **ADC 码口径**（非 ppm） |
| 温升速率 / 速率阈值 | **接口 5** | `evidence.temperatureRateCPerMinute` / `temperatureRateThresholdCPerMinute` | 1 位小数，单位 `°C/min` |
| 样本数 | **接口 5** | `evidence.sampleCount` | 整数计数 |
| 底部时间 | **接口 5** | `endedAt` | 仅在已恢复时展示 `已恢复 HH:mm:ss`，**无 acknowledgedAt** |

> 契约 §7：告警证据必须读自后端持久化的 `evidence`，客户端绝不用最新值反推历史原因。

---

## 5. 设置页（预警阈值）逐块映射

数据入口：`pages/settings/settings.js` → `services/monitoring.js:loadSettings() / updateThresholds()` → **接口 6 / 接口 7 / 接口 8**

| UI 元素 | 接口 | 字段 | 约束与行为 |
|---|---|---|---|
| 温度上限数字 + 滑块 | **接口 6** | `temperatureHighC` | 0–80 °C，步长 1（固件整度执行） |
| 气体浓度上限数字 + 滑块 | **接口 6** | `gasHighPpm` | 1–999 ppm，步长 1 |
| 湿度上限（不可编辑） | **接口 6** | `humidityHighRh` | 0–100 %RH；界面上虽未提供滑块，但**下发时原样带上现有值**（三字段必填） |
| 本地预校验 | — | `validateThresholds()` | 越界时弹 Toast 拦截，文案与 KMP 严格一致 |
| 保存并下发 | **接口 7** `PUT /thresholds` | 请求体三字段 + `Idempotency-Key` | 返回 202 仅代表在途受理，界面展示「等待设备确认」 |
| 命令终态轮询 | **接口 8** `GET /commands/{requestId}` | `awaitCommandOutcome()` | 每 1.5s 轮询一次，最多 10 次；只有 `applied` 算成功并提示「设备已确认」 |
| 规则同步状态 | **接口 6** | `confirmationState` + `confirmedVersion >= desiredVersion` | 仅当 confirmationState 为 confirmed 且确认版本匹配时为薄荷绿已确认 |
| 更新时间 | **接口 6** | `updatedAt` | 展示为服务端时间 |
| 底部提示 | 无 | 静态文案 | `下发后需设备确认，确认前仍按旧规则报警` |

---

## 6. 控制命令生命周期时序

```text
用户拖动滑块点击「保存并下发到设备」
  → 前端 validateThresholds 本地校验范围
  → 生成 Idempotency-Key（UUID v4）
  → PUT /api/v1/devices/{deviceId}/thresholds
  ← 202 { requestId, status: "pending", desiredVersion, expiresAt }
  → 界面展示「等待设备确认」，进入 saving 状态
  → 启动轮询：GET /api/v1/devices/{deviceId}/commands/{requestId}（10 × 1.5s）
      • state: published → 仍在途，继续等待
      • state: applied → 成功，Toast 提示「设备已确认」并重拉设置数据
      • state: rejected / timed_out / failed → 失败，Toast 提示对应失败文案
      • 429 / 5xx / 网络故障 → 在预算内自动重试
      • 401 / 404 → 异常配置立即中断抛出
```

---

## 7. 错误码与界面处理

`services/request.js` 解析后端信封为 `ApiError{ code, statusCode, message }`：

| HTTP | code | 界面处理 |
|---|---|---|
| 400 | `invalid_request` | 提示「查询参数有误」 |
| 401 | `unauthenticated` | 提示未授权 |
| 403 | `forbidden` | 提示无操作权限 |
| 404 | `device_not_found` | 提示设备不存在；注意 `telemetry/latest` 404 是**空态**而非异常 |
| 409 | `version_conflict` | 提示「版本冲突，请刷新后重试」 |
| 422 | `invalid_threshold` | 提示阈值参数非法 |
| 429 | `rate_limited` | 提示操作频繁，在轮询预算内重试 |
| 500 | `internal_error` | 提示服务异常，稍后重试 |
| 503 | `broker_unavailable` | 提示「命令未能下发到设备，请稍后重试」 |
