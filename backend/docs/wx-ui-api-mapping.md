# 微信小程序界面 ↔ Backend API 对照表

> 面向对象：Backend / 微信端开发者。用于核对「界面上的每个区块吃哪条接口、哪个字段」。
> 事实源：[`api.md`](api.md)、[`../../docs/api/openapi.yaml`](../../docs/api/openapi.yaml)（v2.0.0）
> 前端实现：`client-wx-native/`，数据统一经 `services/device.js`（REST 门面）与 `services/socket.js`（实时流）
> 当前状态：Backend 全部路由已实现（2026-09-22，见 [`api.md`](api.md) §2）；前端 `config/env.js` 的 `useMock` 仍为 `true`，默认全部走本地 Mock（REST 与实时事件均由 Mock 提供），联调时改为 `false` 即切到真实接口。

---

## 1. 总览：9 条路由 ↔ 前端方法 ↔ 使用页面

| # | 接口 | 前端方法 | 使用页面 | 后端现状 | 前端现状 |
|---|---|---|---|---|---|
| 1 | `GET /healthz` | 未接入 | — | Implemented | — |
| 2 | `GET /api/v1/devices/{id}/status` | `deviceService.getStatus()` | 监控页 | Implemented | 已接入 |
| 3 | `GET /api/v1/devices/{id}/telemetry/latest` | `deviceService.getLatestTelemetry()` | 监控页（补数/兜底） | Implemented | 已接入 |
| 4 | `GET /api/v1/devices/{id}/telemetry` | `deviceService.getTelemetryHistory()` | 趋势页 | Implemented | 已接入 |
| 5 | `GET /api/v1/devices/{id}/alerts` | `deviceService.getAlerts()` | 告警页 | Implemented | 已接入 |
| 6 | `GET /api/v1/devices/{id}/thresholds` | `deviceService.getThresholds()` | 设置页 | Implemented | 已接入 |
| 7 | `PUT /api/v1/devices/{id}/thresholds` | `deviceService.putThresholds()` | 设置页保存 | Implemented | 已接入 |
| 8 | `GET /api/v1/devices/{id}/commands/{requestId}` | 未接入 | 设置页可选命令详情查询 | Implemented | 未接入；当前依赖阈值状态与 WebSocket 确认 |
| 9 | `GET /ws/v1/devices/{id}/telemetry` | `socket.connect()` | 监控页 / 告警页 / 设置页 | Implemented | **已接入** |

路径中的 `{id}` 必须匹配 `^[A-Za-z0-9_-]{1,32}$`（契约 §1.5），当前前端固定使用 `MCU001`。
所有请求体/响应体字段名遵循 lower camel case，时间使用 UTC RFC 3339。

---

## 2. 监控页（实时监控）逐块映射

数据入口：`pages/dashboard/dashboard.js`
- 首次进入：`fetchInitial()` → 并发 **接口 2 + 接口 3**（REST 补数）
- 之后：`subscribeStream()` → **接口 9** 增量驱动
- 断线重连：`socket` 回调 `onResync()` → 再次走 **接口 2 + 接口 3** 补数

### 2.1 页头

| UI 元素 | 接口 | 字段 |
|---|---|---|
| 「机房环境总览 / 智慧机房 · 实时动环监测」 | 无 | 静态文案 |

### 2.2 系统风险状态卡

| UI 元素 | 接口 | 字段 / 取值 | 代码位置 |
|---|---|---|---|
| 小标题「系统风险状态」 | 无 | 静态 | — |
| 圆点颜色 + 大字（环境正常 / 疑似异常 / 火情预警 / 指标已恢复） | **接口 2**，或 **接口 9** `alert.state_changed` 后触发补数 | `alarmState`：`normal｜suspect｜fire_warning｜recovered`（首期无 `acknowledged`，见契约 FD-9；前端映射表中的「告警已确认」为遗留项，后端不会产生） | `ALARM_STATE` 映射表 → `applyStatus()` |
| 右侧胶囊（在线 / 离线 / 未知） | **接口 2**，或 **接口 9** `device.status_changed` 后触发补数 | `connectivity`：`online｜offline｜unknown` | `CONNECTIVITY` 映射表 |
| 下方说明文字 | **接口 2** | 由 `alarmState` 派生的语义说明 | `ALARM_STATE[x].sub` |

> ⚠️ 契约 §4 明确：`localAlarm`（设备本地判断）与 Backend 的 `alarmState`（复合预警）不是同一概念，界面分列显示，不可互相替代。

### 2.3 实时数据三卡（T / H / G）

| UI 元素 | 接口 | 字段 | 换算 |
|---|---|---|---|
| 温度数字 | **接口 3 / 接口 9** `telemetry.updated` | `temperatureC` | 保留 1 位小数 |
| 湿度数字 | 同上 | `humidityRh` | 保留 1 位小数 |
| 气体数字 | 同上 | `gasPpm`（**不是** `gasAdcRaw` / `gasAdcFiltered`） | 保留 1 位小数 |
| 温度进度条宽度 | 同上 | `temperatureC` | 前端展示量程 0–40 °C |
| 湿度进度条宽度 | 同上 | `humidityRh` | 前端展示量程 0–100 %RH |
| 气体进度条宽度 | 同上 | `gasPpm` | 前端展示量程 0–100 ppm |
| 区块右上角提示 | **接口 9** 连接状态（本地事件 `connection.changed`） | `open` → 「实时推送」；`reconnecting` → 「重连中，已切 REST 兜底」 | `describeStream()` |

> 💡 展示量程（40 °C / 100 ppm）与报警阈值（设置页的 `temperatureHighC` / `gasHighPpm`）是两回事，后续可把阈值画成量程条上的刻度线。

### 2.4 设备状态卡

| UI 元素 | 接口 | 字段 |
|---|---|---|
| 设备编号 | 页面常量 + **接口 2** `deviceId` | `MCU001` |
| 在线胶囊 | **接口 2** | `connectivity` |
| 本地报警（正常 / 报警中） | **接口 3 / 接口 9** | `localAlarm` |
| 更新时间 | **接口 3 / 接口 9** | `receivedAt`（`timestamp` 为 null 时兜底；实时事件用 `occurredAt`） |

### 2.5 页面级行为

| 行为 | 实现 |
|---|---|
| 下拉刷新 | `onPullDownRefresh()` → 接口 2 + 接口 3 并发重拉 |
| 进入页面 | 订阅接口 9，并触发一次 REST 补数 |
| 离开页面 | `unsubscribeStream()` → 解绑事件并断开实时连接 |

### 2.7 契约已有、界面暂未使用的字段（可选增强）

| 字段 | 来源 | 可用场景 |
|---|---|---|
| `lastSeenAt` | 接口 2 | 展示「最后心跳时间」 |
| `offlineAfterSeconds`（15s） | 接口 2 | 离线判定提示「超过 15 秒未收到上报」 |
| `thresholdVersion.desired / confirmed` | 接口 2 | 监控页直接提示「阈值待设备确认」 |
| `sequence` | 接口 3 / 接口 9 | 检测丢包（序号不连续） |
| `alarmCauses[]`（`gas_high` 等） | 接口 3 / 接口 9 | 风险卡显示具体触发原因 |
| `network` | 接口 3 | 设备侧网络状态，与 Backend `connectivity` 区分 |
| `gasAdcRaw` / `gasAdcFiltered` | 接口 3 | 调试：对比滤波前后，验证 STM32 端滤波效果 |

---

## 3. 趋势页（历史趋势）逐块映射

数据入口：`pages/trends/trends.js:fetch()` → **接口 4**

| UI 元素 | 接口 | 参数 / 字段 |
|---|---|---|
| 近1小时 / 近6小时 / 近24小时 | **接口 4** | `from = now - N 小时`（RFC 3339）、`limit=60`；`to` 默认 now、`order` 默认 asc |
| 区块右上「共 N 条样本」 | **接口 4** | `items.length` |
| 温度 平均/最低/最高 | **接口 4** | 由 `items[].temperatureC` 前端聚合 `summarize()` |
| 湿度 平均/最低/最高 | **接口 4** | 由 `items[].humidityRh` 聚合 |
| 气体 平均/最低/最高 | **接口 4** | 由 `items[].gasPpm` 聚合 |
| 峰值时刻 | **接口 4** | `findExtremes()` 取 `items[].timestamp`（无则 `receivedAt`） |
| 曲线区（原生 Canvas 2D） | **接口 4** | 同一 `items[]` 三字段画三条线；微信端已接入 Canvas，非 ECharts |

注意事项：
- `cursor` 出现时，`from/to/order` 必须与第一页一致（契约 §6）；
- 单次查询跨度上限 31 天（`MaxQuerySpan`，超出直接返回 400 `invalid_request`）；更长区间应走聚合接口或导出任务。

---

## 4. 告警页（告警记录）逐块映射

数据入口：`pages/alerts/alerts.js:fetch()` → **接口 5**；并订阅 **接口 9** 的 `alert.state_changed`（去抖 800ms 后自动刷新）

| UI 元素 | 接口 | 参数 / 字段 |
|---|---|---|
| 筛选：全部 | **接口 5** | 不带 `state`；**当前为前端本地过滤** |
| 筛选：火情 / 已确认 / 已恢复 | **接口 5** | 服务端过滤用 `state=fire_warning｜recovered`（首期无 `acknowledged`，该枚举值会被 400 拒绝；「已确认」仅为前端遗留本地过滤项） |
| （未使用）只看未结束 | **接口 5** | `active=true` |
| 状态标签 | **接口 5** | `state` → `STATE_META` 文案与配色 |
| 右上开始时间 | **接口 5** | `startedAt` |
| 证据：气体上升 / 触发阈值 / 温升速率 / 样本数 | **接口 5** | `evidence.gasAdcRise`、`gasAdcRiseThreshold`、`temperatureRateCPerMinute`、`sampleCount` |
| 底部「已恢复」时间 | **接口 5** | `endedAt`（可能为 null；首期没有 `acknowledgedAt` 字段） |
| 事件 ID | **接口 5** | `id`，用作列表 key |
| （未展示）温升速率阈值 | **接口 5** | `evidence.temperatureRateThresholdCPerMinute`，建议补齐与「触发阈值」对称 |

> ⚠️ 契约 §7：告警原因必须用后端保存的 `evidence`，客户端不得用当前最新值反推历史告警原因。当前实现遵守该规则。

---

## 5. 设置页（预警阈值）逐块映射

数据入口：`pages/settings/settings.js:fetch()` → **接口 6**；并订阅 **接口 9** 的 `thresholds.confirmed` / `command.status_changed`

| UI 元素 | 接口 | 字段 | 约束 |
|---|---|---|---|
| 温度上限数字 + 滑块 | **接口 6** | `temperatureHighC` | 0–80 °C，步长 0.5 |
| 气体浓度上限数字 + 滑块 | **接口 6** | `gasHighPpm` | 1–999 ppm，步长 1 |
| 湿度上限（页面暂无滑块） | **接口 6** | `humidityHighRh` | 0–100 %RH；保存时必须回传当前值（三字段全必填） |
| 保存并下发 | **接口 7** `PUT /thresholds` | 请求 `{ temperatureHighC, humidityHighRh, gasHighPpm }`（三字段全必填）+ `Idempotency-Key` | 前端先做范围预校验；湿度尚无滑块，读取服务端现值后原样回传；缺字段服务端 400、越界 422 兜底 |
| 下发响应 | **接口 7** | `202 { requestId, status:"pending", desiredVersion, expiresAt }` | 用到 `desiredVersion`；`expiresAt` 可做倒计时提示 |
| 规则同步状态 | **接口 6** | `confirmationState`：`confirmed｜pending｜rejected｜timed_out` | 文案见 `CONFIRM_TEXT` |
| 期望版本 / 设备确认版本 | **接口 6** | `desiredVersion` / `confirmedVersion` | 两者不一致 = 命令在途/失败/设备离线 |
| 更新时间 | **接口 6** | `updatedAt` | 展示为 `MM-DD HH:mm` |
| 确认事件 | **接口 9** | `thresholds.confirmed`、`command.status_changed` | 事件到达即重新拉取接口 6；另保留 8 秒兜底重拉（仅 pending 时） |

> 契约 §8：设备 ack `applied` 后才更新 `confirmedVersion`；超时**不回滚** `desiredVersion`，只标记 `timed_out` 供重试。

---

## 6. 控制类操作时序

### 6.1 阈值下发（接口 7）

```text
拖动滑块 → 点「保存并下发到设备」
  → PUT /thresholds  body: { temperatureHighC, humidityHighRh, gasHighPpm } + Idempotency-Key
  ← 202 { desiredVersion: N+1, status: "pending", expiresAt }
  → 界面：规则同步状态 = 等待设备确认（desiredVersion 已变，confirmedVersion 仍旧值）
  → 设备写 Flash 成功并 ack applied → Backend 更新 confirmedVersion = N+1
  → WebSocket thresholds.confirmed → 前端重新拉取接口 6 → 显示「设备已确认」，两侧版本号一致
失败分支：409 version_conflict / 422 invalid_threshold / 503 broker_unavailable
```

---

## 7. 实时层实现（接口 9）

实现文件：`client-wx-native/services/socket.js`（页面用法：`socket.connect(deviceId, { onResync })` + `socket.on(type, handler)`）

| 契约要求 | 实现方式 |
|---|---|
| Envelope `{ type, eventId, occurredAt, deviceId, data }` | 统一 `envelope()` 生成；Mock 模式同样格式 |
| 服务端 ping / 客户端 pong | `handleMessage()` 收到 `ping` 回 `pong` |
| `eventId` 去重 | `isDuplicate()`，保留最近 200 个 eventId |
| WebSocket 不补历史 | 重连成功后回调 `onResync()` → 页面走 REST 补数 |
| 慢客户端被断开 | 断开即重连，退避 1s→2s→4s→…→15s（上限） |
| 心跳保活 | 40 秒无消息视为死连接，主动断开重连 |
| 弱网兜底 | 连接期间另起 20 秒 REST 兜底轮询（仅补数，不替代实时流） |
| Mock 模式 | 本地定时器投递同格式事件：2 秒一次 `telemetry.updated`；版本变化 → `thresholds.confirmed` + `command.status_changed`；告警原因变化 → `alert.state_changed` |

事件订阅关系（契约 §10 五类事件）：

| 事件 `type` | 携带数据 | 消费页面 | 行为 |
|---|---|---|---|
| `telemetry.updated` | `sequence, temperatureC, humidityRh, gasAdcFiltered, gasPpm, localAlarm` | 监控页 | 直接刷新三卡与设备状态 |
| `device.status_changed` | online/offline/unknown | 监控页 | 重新拉取接口 2 |
| `alert.state_changed` | 复合预警状态 | 监控页 / 告警页 | 监控页重拉状态；告警页去抖 800ms 后刷新列表 |
| `command.status_changed` | 命令确认/拒绝/超时 | 监控页 / 设置页 | 重拉接口 2 + 接口 3（监控）/ 接口 6（设置） |
| `thresholds.confirmed` | `desiredVersion, confirmedVersion` | 设置页 | 重拉接口 6 |

---

## 8. 错误码 → 界面处理建议（契约 §1.6）

`services/request.js` 已把错误信封解析为 `ApiError{ code, statusCode, message }`。建议按下表细化提示：

| HTTP | code | 建议处理 | 涉及接口 |
|---|---|---|---|
| 400 | `invalid_request` | 「查询参数有误」，检查 `from/to` 格式 | 4、5 |
| 401 | `unauthenticated` | 接入小程序登录换 token（契约预留 `Authorization: Bearer`） | 全部 |
| 403 | `forbidden` | 「没有该设备的操作权限」 | 全部 |
| 404 | `device_not_found` | 「设备不存在或不可见」，监控页显示空态而非报错 | 2、3、6 |
| 409 | `version_conflict` | 「版本冲突，请刷新后重试」（幂等键复用但内容不同） | 7 |
| 422 | `invalid_threshold` | 用 `details.field` 定位到对应滑块下方红字提示 | 7 |
| 503 | `rate_limited` | 「实时连接已满」（仅 WebSocket 满员时出现，见 api.md §1.6/§11） | 9 |
| 500 | `internal_error` | 「服务异常，稍后重试」 | 全部 |
| 503 | `broker_unavailable` | 「命令未能下发到设备，请稍后重试」 | 7 |
| 504 | `device_ack_timeout` | 预留，当前不产生；设备确认超时以命令状态 `timed_out` 表达（见 api.md §1.6） | 7 |

---

## 9. 字段一致性核对

| 界面字段 | 契约字段 | 状态 |
|---|---|---|
| 风险状态 | `status.alarmState` | ✅ 4 枚举全支持（首期无 `acknowledged`） |
| 在线状态 | `status.connectivity` | ✅ online / offline / unknown |
| 温度 / 湿度 / 气体 | `telemetry.temperatureC / humidityRh / gasPpm` | ✅ |
| 本地报警 | `telemetry.localAlarm` | ✅ |
| 更新时间 | `telemetry.receivedAt`（`timestamp` 兜底） | ✅ |
| 告警状态与证据 | `alerts[].state`、`alerts[].evidence.*` | ✅ |
| 阈值与版本 | `thresholds.temperatureHighC / humidityHighRh / gasHighPpm / desiredVersion / confirmedVersion / confirmationState` | ✅ |
| 控制命令 | `PUT thresholds` + `Idempotency-Key` | ✅ |
| 实时流 | `ws/v1/...` 五类事件 | ✅ 已接入 |
| `X-Request-ID`（建议头） | 契约 §1.4 | ⚠️ 前端未携带 |
| `Authorization`（生产必须） | 契约 §1.3 | ⚠️ 待鉴权方案确定后接入 |

---

## 10. 联调 checklist（切到真实 Backend）

1. `client-wx-native/config/env.js`：`useMock: false`；`baseUrl` 改真实地址（真机调试用电脑局域网 IP，如 `http://192.168.1.10:8080`），`wsUrl` 同步改为 `ws://…`。
2. 微信开发者工具 → 详情 → 本地设置 → 勾选**「不校验合法域名」**；真机需在微信公众平台配置 `request` 与 `socket` 合法域名（须 HTTPS / WSS）。
3. 确认 `deviceId` 与数据库、MQTT Payload 三处一致。
4. 时间统一 UTC RFC 3339，展示层转本地时间。
5. Backend 需保证 WebSocket 事件字段与 Envelope 和本文第 7 节一致，尤其 `eventId` 必须唯一，否则前端去重会丢事件。
6. 前端待补：`X-Request-ID` 请求头、鉴权、纯 JS 逻辑单测（`summarize` / `findExtremes` / 状态映射 / 幂等键），仓库要求覆盖率 ≥ 80%。
