# 微信小程序界面 ↔ Backend API 对照表

> 事实源：[`api.md`](api.md)、[`../../docs/api/openapi.yaml`](../../docs/api/openapi.yaml)
> 前端实现：`client-wx-native/`，数据经 `services/monitoring.js`（契约 + 网络）与 `utils/presentation.js`（展示派生）
> 对齐基准：`client-kmp` 共享层（`MonitoringClient` + `MonitoringPresentation`），两端对同一份数据的示数与实时行为必须一致
> 当前状态：后端**全部路由已实现**（2026-09-22）；前端 `config/env.js` 的 `useMock` 仍为 `true`，联调时置为 `false`

---

## 1. 总览：路由 ↔ 前端方法 ↔ 使用页面

| # | 接口 | 前端方法（services/monitoring.js） | 使用页面 | 后端现状 |
|---|---|---|---|---|
| 1 | `GET /healthz` | 未接入 | — | Implemented |
| 2 | `GET /api/v1/devices/{id}/status` | `loadDashboard()` | 机房环境总览 | Implemented |
| 3 | `GET /api/v1/devices/{id}/telemetry/latest` | `loadDashboard()`（404 = 空态） | 机房环境总览 | Implemented |
| 4 | `GET /api/v1/devices/{id}/telemetry` | `loadTrends()` | 历史趋势 | Implemented |
| 5 | `GET /api/v1/devices/{id}/alerts` | `loadAlerts()` | 告警记录 | Implemented |
| 6 | `GET /api/v1/devices/{id}/thresholds` | `loadSettings()` | 预警阈值 | Implemented |
| 7 | `PUT /api/v1/devices/{id}/thresholds` | `updateThresholds()` | 预警阈值（保存） | Implemented |
| 8 | `POST /api/v1/devices/{id}/commands/mute` | `setMuted()` | 机房环境总览（远程静音） | Implemented |
| 9 | `GET /api/v1/devices/{id}/commands/{requestId}` | `loadCommandStatus()` / `awaitCommandOutcome()` | 控制命令终态（两个控制入口共用） | Implemented |

`{id}` 固定为 `MCU001`，必须匹配 `^[A-Za-z0-9_-]{1,32}$`。

> 本端**不使用** `GET /ws/v1/devices/{id}/telemetry`：KMP 方案不含实时订阅，实时性由 3 秒快照轮询保证（见第 7 节）。
> 该路由后端已实现；若两端各用一套推送/轮询，刷新节奏会再次不一致，接入需作为独立提案并保留轮询兜底。

---

## 2. 机房环境总览（dashboard）逐块映射

数据入口：`pages/dashboard/dashboard.js`
- 首次进入 `loadDashboard(true)`，之后 `startPolling()` 每 3000 ms `loadDashboard(false)`
- 每次刷新都是一次**原子快照**：接口 2 + 接口 3 一起取，指标与风险状态同帧更新

### 2.1 页头

| UI 元素 | 接口 | 说明 |
|---|---|---|
| 「机房环境总览 / 智慧机房 · 实时动环监测」 | 无 | 静态文案 |

### 2.2 系统风险状态卡

| UI 元素 | 接口 | 字段 / 取值 |
|---|---|---|
| 状态点 + 大字 | **接口 2** | `alarmState`：`normal → 环境正常(mint)`、`suspect → 疑似异常(warning)`、`fire_warning → 火情预警(danger)`、`recovered → 指标已恢复(info)`；未知值回退 `normal` |
| 右侧胶囊（在线 / 离线 / 未知） | **接口 2** | `connectivity`：`online / offline / unknown` |
| 下方说明 | **接口 2** | 与 `alarmState` 对应的固定文案 |

> 契约 §4：`localAlarm`（设备本地判断）与 `alarmState`（Backend 复合预警）是两件事，界面分列显示，不可互相替代。

### 2.3 实时数据三卡

| UI 元素 | 接口 | 字段 / 换算 |
|---|---|---|
| 温度 / 湿度 / 气体示数 | **接口 3** | `temperatureC` / `humidityRh` / `gasPpm` → `reading()` **取整为整数** |
| 气体未标定 | **接口 3** | `gasPpm: null` → 显示 `--`（**不得显示 0**） |
| 三条进度条 | **接口 3** | `percent(v, max)`，量程取契约上限：温度 80 °C、湿度 100 %RH、气体 999 ppm |
| 区块右上角 | — | 「每 3 秒同步」（与轮询节奏一致） |
| 无有效遥测 | **接口 3** 404 | 显示「该设备尚未上报有效遥测数据」，指标为 `--`，风险状态仍真实 |

### 2.4 设备状态卡

| UI 元素 | 接口 | 字段 |
|---|---|---|
| 设备编号 | **接口 2** | `deviceId` |
| 在线胶囊 | **接口 2** | `connectivity` |
| 本地报警（正常 / 报警中） | **接口 3**，缺失时回退 **接口 2** | `localAlarm` |
| 声光提示 | 同上 | `buzzerMuted` → `已静音`；否则 `localAlarm` → `报警策略生效`；否则 `待机` |
| 更新时间 | 同上 | `receivedAt`，缺失回退 `lastSeenAt`，再缺失 `--` |

### 2.5 远程控制（蜂鸣器）

| UI 元素 | 接口 | 说明 |
|---|---|---|
| 按钮文案 | **接口 3** | `buzzerMuted` 取反：`远程静音` / `恢复鸣叫` |
| 点击下发 | **接口 8** | `POST /commands/mute`，body `{ muted }`，**必须带 `Idempotency-Key`** |
| 响应 | **接口 8** | `202 { requestId, status: "pending", expiresAt }` |
| 结果确认 | **接口 9** | `awaitCommandOutcome(requestId)`：10 次 × 1.5 s 轮询，只有 `state = applied` 才算成功；超时/拒绝/重复各有独立文案与配色 |

`set_mute` **只抑制蜂鸣器**：不清除 `localAlarm`、不关 LED/OLED、不停采样与上报。

### 2.6 页面级行为

| 行为 | 实现 |
|---|---|
| 轮询 | 3000 ms 一次快照；上一轮未返回时跳过本轮（防弱网请求叠加） |
| 生命周期 | `onShow` 启动、`onHide`/`onUnload` 停止 |
| 失败 | 写入 `error` 但不中断轮询，下一轮自然重试 |
| 下拉刷新 | 立即刷新一次并结束下拉动画 |

### 2.7 契约已有、界面暂未使用的字段

| 字段 | 来源 | 可增强点 |
|---|---|---|
| `offlineAfterSeconds` | 接口 2 | 离线判定文案「超过 N 秒未收到上报」 |
| `thresholdVersion.desired/confirmed` | 接口 2 | 首页直接提示「阈值待设备确认」 |
| `gasAdcRaw` / `gasAdcFiltered` | 接口 3 | 调试用：对比滤波前后，验证 STM32 端滤波效果 |
| `alarmCauses[]` | 接口 3 | 风险卡显示具体触发原因（`gas_high` 等） |
| `network` | 接口 3 | 设备侧链路状态，与 Backend `connectivity` 区分 |
| `sensorFault` | 接口 3 | 传感器故障提示（当前仅在模型中保留） |
| `sequence` / `bootId` | 接口 3 | 丢包检测与趋势列表 key |

---

## 3. 历史趋势（trends）逐块映射

数据入口：`pages/trends/trends.js:loadTrends()` → **接口 4**

| UI 元素 | 接口 | 参数 / 字段 |
|---|---|---|
| 近1小时 / 近6小时 / 近24小时 | **接口 4** | 窗口 key → `from = now - N 小时`、`to = now`、`limit = 200`、**`order = desc`** |
| 序列顺序 | — | 前端把 `desc` 页**反转为升序**后再统计（保证与 KMP 同一口径） |
| 共 N 条样本 | **接口 4** | `items.length` |
| 温度/湿度/气体的最低·平均·最高 | **接口 4** | 前端 `summarize()`：跳过缺读数样本、`reading()` 取整 |
| 峰值时刻 | **接口 4** | 取最大值的样本 `receivedAt` → `clockText()` |
| 气体样本数不足 | **接口 4** | 显示「N 条样本没有已校准气体读数，未计入气体统计」 |
| 曲线区（ECharts 待接入） | **接口 4** | 同一 `items`，图例 tone 来自共享层（温度 danger / 湿度 info / 气体 mint） |
| 空态 | **接口 4** | 「所选区间内没有遥测样本」 |

备注：契约建议单次跨度 ≤ 31 天；`cursor` 出现时 `from/to/order` 必须与第一页一致。

---

## 4. 告警记录（alerts）逐块映射

数据入口：`pages/alerts/alerts.js:loadAlerts()` → **接口 5**（拉一页，本地切标签）

| UI 元素 | 接口 | 字段 |
|---|---|---|
| 筛选 全部 / 火情 / 疑似 / 已恢复 | **接口 5** | 契约无 `state` 参数，按 `item.state` 本地过滤 |
| 状态胶囊与状态点 | **接口 5** | `state` → 文案与 tone：`fire_warning 火情预警(danger)`、`suspect 疑似异常(warning)`、`recovered 已恢复(info)` |
| 开始时间 | **接口 5** | `startedAt` |
| 气体上升 / 触发阈值 | **接口 5** | `evidence.gasAdcRise` / `gasAdcRiseThreshold`（**ADC 码**，不是 ppm） |
| 温升速率 / 速率阈值 | **接口 5** | `evidence.temperatureRateCPerMinute` / `temperatureRateThresholdCPerMinute`（1 位小数，带 `°C/min`） |
| 样本数 / 窗口 | **接口 5** | `evidence.sampleCount` / `evidence.windowSeconds`（缺失显示 `--`） |
| 底部状态 | **接口 5** | `endedAt` 为空 → 「进行中」，否则「结束于 …」 |

> 契约 §7：告警原因必须读自后端持久化的 `evidence`，客户端不得用当前最新值反推历史原因。
> 契约的告警状态**没有 `acknowledged`**，因此筛选第三项是「疑似」而不是「已确认」。

---

## 5. 预警阈值（settings）逐块映射

数据入口：`pages/settings/settings.js:loadSettings()` → **接口 6**

| UI 元素 | 接口 | 字段 | 约束 |
|---|---|---|---|
| 温度上限 | **接口 6** | `temperatureHighC` | 0–80 °C，步长 0.5 |
| 气体浓度上限 | **接口 6** | `gasHighPpm` | 1–999 ppm，步长 1 |
| 湿度上限 | **接口 6** | `humidityHighRh` | 0–100 %RH；界面上不可编辑，但**下发时必须一起发送** |
| 规则同步状态 | **接口 6** | `confirmationState` + `confirmedVersion >= desiredVersion` | 二者都满足才显示「设备已确认」 |
| 期望版本 / 设备确认版本 | **接口 6** | `desiredVersion` / `confirmedVersion` | 不一致表示命令在途 |
| 更新时间 | **接口 6** | `updatedAt` | 缺失显示 `--` |
| 保存并下发 | **接口 7** | body 三个字段 + `Idempotency-Key` | 本地先按契约范围校验（文案与 KMP 一致） |
| 下发结果 | **接口 9** | 轮询 `commands/{requestId}` | 入队 ≠ 成功；只有 `applied` 才提示成功 |

---

## 6. 控制命令的完整时序

```text
用户操作（静音 / 保存阈值）
  → 前端生成 Idempotency-Key（UUID v4）
  → POST /commands/mute  |  PUT /thresholds
  → Backend 校验后发布 MQTT 命令
  ← 202 { requestId, status: "pending", desiredVersion?, expiresAt }
  → 界面显示「等待设备确认」（不得视为成功）
  → 前端每 1.5 s 轮询 GET /commands/{requestId}，最多 10 次（≈15 s，覆盖一个上报周期）
  ← state: published → 仍等待；applied → 成功；rejected / failed / timed_out / expired → 失败各有文案
  → 成功后立即刷新页面数据（dashboard 或 settings）
失败分支：409 version_conflict / 422 invalid_threshold / 503 broker_unavailable
        429 与 5xx 与网络中断在轮询预算内重试；401/404/结构损坏立即抛出（不把坏配置当"还在等待"）
```

---

## 7. 实时策略（与 KMP 一致）

| 规则 | 取值 |
|---|---|
| 仪表盘轮询间隔 | **3000 ms** |
| 单次刷新内容 | 接口 2 + 接口 3 组成的原子快照 |
| 轮询范围 | 只轮询仪表盘；趋势/告警/设置按需拉取 |
| 生命周期 | `onShow` 启动、`onHide`/`onUnload` 停止 |
| 重复请求保护 | 上一轮未返回则跳过本轮 |
| 失败处理 | 写入 `error`，轮询继续 |
| 不使用 WebSocket | KMP 方案无实时订阅（后端已实现 `ws/v1` 但未采用）；接入需独立提案并保留轮询兜底 |

---

## 8. 错误码 → 界面处理建议

`services/request.js` 已把头统一解析为 `ApiError{ code, statusCode, message }`。

| HTTP | code | 建议处理 | 涉及接口 |
|---|---|---|---|
| 400 | `invalid_request` | 「查询参数有误」，检查 `from/to` 格式 | 4、5 |
| 401 | `unauthenticated` | 接入小程序登录换 token（契约预留 `Authorization: Bearer`） | 全部 |
| 403 | `forbidden` | 「没有该设备的操作权限」 | 全部 |
| 404 | `device_not_found` | 「设备不存在或不可见」；`telemetry/latest` 的 404 是**空态**不是错误 | 2、3、6 |
| 404 | `not_found` | 命令 ID 不存在（配置类错误，不重试） | 9 |
| 409 | `version_conflict` | 「版本冲突，请刷新后重试」 | 7 |
| 422 | `invalid_threshold` | 用 `details.field` 定位到具体输入项 | 7 |
| 429 | `rate_limited` | 「操作过于频繁，请稍后再试」（轮询预算内重试） | 全部 |
| 500 | `internal_error` | 「服务异常，稍后重试」 | 全部 |
| 503 | `broker_unavailable` | 「命令未能下发到设备，请稍后重试」 | 7、8 |
| 504 | `device_ack_timeout` | 「设备确认超时，请检查设备是否在线」 | 7、8 |

---

## 9. 字段一致性核对（界面用到 vs 契约）

| 界面字段 | 契约字段 | 状态 |
|---|---|---|
| 风险状态 | `status.alarmState`（4 枚举，无 acknowledged） | ✅ |
| 在线状态 | `status.connectivity` | ✅ |
| 三指标示数 | `telemetry.temperatureC / humidityRh / gasPpm` | ✅ 取整后展示 |
| 气体可空 | `telemetry.gasPpm: null` + `gasCalibrated` | ✅ 显示 `--` |
| 本地报警 / 静音 | `telemetry.localAlarm` / `buzzerMuted` | ✅ |
| 告警状态与证据 | `alerts[].state`、`alerts[].evidence.gasAdcRise` 等 | ✅ ADC 口径 |
| 阈值 | `thresholds.temperatureHighC / humidityHighRh / gasHighPpm` | ✅ 三字段 |
| 版本与确认 | `desiredVersion / confirmedVersion / confirmationState` | ✅ |
| 控制命令 | `commands/mute`、`PUT thresholds` + `Idempotency-Key` | ✅ |
| 命令终态 | `GET /commands/{requestId}` | ✅ 轮询 10 × 1.5 s |
| `X-Request-ID`、`Authorization` | 契约 §1.4 / §1.3 | ⚠️ 待接入（鉴权方案确定后） |

---

## 10. 联调 checklist

1. `client-wx-native/config/env.js`：`useMock: false`；`baseUrl` 改真实地址（真机调试用电脑局域网 IP）。
2. 微信开发者工具 → 详情 → 本地设置 → 勾选**「不校验合法域名」**；真机需在公众平台配置 `request` 合法域名（HTTPS）。
3. 确认 `deviceId` 与数据库、MQTT Payload 三处一致。
4. Backend 必须实现 `GET /commands/{requestId}`，否则控制命令无法达成终态（前端会停在「等待设备确认」）。
5. 时间统一 UTC RFC 3339；前端只展示，不转换时区。
6. 前端待补：`X-Request-ID` 请求头与鉴权。
