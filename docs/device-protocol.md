# Device MQTT Protocol

状态：`v2.0.0`（原 `v1.0.0-frozen` 中的远程静音能力已删除，属破坏性变更；后续修改必须走 §9 变更流程）。

**v1.0.0 → v2.0.0 迁移说明**：远程静音能力已从 Hardware、Backend、KMP 和微信客户端全部移除。MQTT `device/control` 主题现在只接受 `set_thresholds` 命令；旧的 `set_mute` 命令会被设备明确拒绝（`bad_request_type`）。遥测中的 `buzzerMuted` 字段已删除。本地蜂鸣器报警仅由设备自身的气体报警逻辑（`gas_high` / `rapid_gas_rise`）控制，服务器和客户端无权远程静音。调用已删除的 `POST .../commands/mute` REST 端点将收到标准 404。

本文是设备与 Backend 之间 MQTT 报文的事实源。当前固件已使用 ESP8266 TCP 透传 MQTT 3.1.1。2026-09-23 已重新实机验证 v2.0.0 的 CONNECT、SUBSCRIBE、QoS 1 遥测、阈值下发/ACK、Broker 停启后自动重连和气体阈值驱动蜂鸣器；证据与边界见 `hardware/README.md`。

## 1. Transport

| Direction | Topic | QoS | Retain |
| --- | --- | --- | --- |
| Device → Cloud | `device/telemetry` | 1 | false |
| Device → Cloud | `device/command-ack` | 1 | false |
| Cloud → Device | `device/control` | 1 | false |

首期使用固定主题，通过 `deviceId` 区分设备。Broker ACL 必须限制设备发布/订阅方向：设备只允许发布到 `device/telemetry`、`device/command-ack`，只允许订阅 `device/control`。控制消息不得 retain，避免设备重连后执行过期命令。

MQTT 连接参数：协议 3.1.1（冻结；当前固件仅实现 3.1.1，不支持 5.0），`clientId` 必须等于 `deviceId`，`keepAlive` 30 秒，`cleanSession = true`，CONNECT 携带 `username = device`、无密码。Backend 不得依赖 Broker 的会话状态判断设备在线，只依赖 §5 的应用层时间。

## 2. 通用编码规则

- 编码 UTF-8；JSON 字段名与枚举值区分大小写（lower camel case / 小写下划线枚举）。
- 所有消息必须包含 `schemaVersion`，当前为 `1`。Backend 收到不等于已支持版本的报文时记 `schema_unsupported` 并丢弃，不得猜测字段语义。
- 时间戳使用 UTC Unix 毫秒。设备时间未同步时 `timestamp` 必须为 `null`（不得填 0 或本地猜测值），此时必须提供 `uptimeMs`；Backend 始终额外记录 `receivedAt`。
- 报文字段一律使用明确类型。整数字段不得发送小数或字符串；`null` 只允许出现在契约声明为 nullable 的字段上。
- Backend 对超过 4 KiB 的报文（实现为 4096 字节硬限制）、非法 JSON、未知 `messageType`、越界数值一律拒绝并记录，不得部分入库。
- 当前固件没有任何可信时间源，因此遥测与 ACK 的 `timestamp` 恒为 `null`（见 §3、§6 示例）；`issuedAt`/`expiresAt` 由 Backend 生成，恒为整数。

## 3. Telemetry Payload

主题 `device/telemetry`。

```json
{
  "schemaVersion": 1,
  "messageType": "telemetry",
  "deviceId": "MCU001",
  "bootId": "9f3ac21b",
  "sequence": 42,
  "timestamp": null,
  "uptimeMs": 125000,
  "temperatureC": 28.0,
  "humidityRh": 61.0,
  "gasAdcRaw": 1350,
  "gasAdcFiltered": 1328,
  "gasPpm": 25.0,
  "gasCalibrated": false,
  "localAlarm": true,
  "alarmCauses": ["gas_high"],
  "network": "online",
  "thresholdVersion": 3,
  "sensorFault": false
}
```

### 3.1 字段

| Field | Type | Required | Unit / Range | Notes |
| --- | --- | --- | --- | --- |
| `schemaVersion` | integer | yes | `1` | 固定值 |
| `messageType` | string | yes | `telemetry` | 与 ACK 报文区分 |
| `deviceId` | string | yes | `^[A-Za-z0-9_-]{1,32}$` | 必须与 `clientId` 一致 |
| `bootId` | string | yes | `^[A-Za-z0-9]{1,16}$` | 每次上电生成的新标识，见 §5.1 |
| `sequence` | integer | yes | 0 – 4294967295 | 本次启动内单调递增，见 §5.1 |
| `timestamp` | integer \| null | yes | UTC 毫秒 | 未同步时钟时为 `null` |
| `uptimeMs` | integer | yes | ≥ 0 毫秒 | 自本次上电起，始终有效 |
| `temperatureC` | number | yes | −40 – 80 °C | DHT11 整数分辨率，一位小数以内 |
| `humidityRh` | number | yes | 0 – 100 %RH | DHT11 整数分辨率 |
| `gasAdcRaw` | integer | yes | 0 – 4095 | 最近一次 ADC 原始值 |
| `gasAdcFiltered` | integer | yes | 0 – 4095 | 滑动窗口滤波后的 ADC 值 |
| `gasPpm` | number \| null | yes | ≥ 0 ppm | 估算浓度；未标定时客户端的精度约束见 §3.3 |
| `gasCalibrated` | boolean | yes | | `false` 表示 `gasPpm` 来自未标定曲线 |
| `localAlarm` | boolean | yes | | 设备本地综合判断结果 |
| `alarmCauses` | string[] | yes | 见下 | 为空数组表示无本地告警 |
| `network` | string | yes | `online` \| `reconnecting` | 设备自身网络状态 |
| `thresholdVersion` | integer | yes | ≥ 1 | 设备当前生效的阈值版本，见 §4.3 |
| `sensorFault` | boolean | yes | | 任一传感器读取失败时为 `true` |

`alarmCauses` 枚举（冻结）：`temperature_high`、`humidity_high`、`gas_high`、`rapid_temperature_rise`、`rapid_gas_rise`、`sensor_fault`。

### 3.2 语义规则

- `localAlarm = true` 时，LED、OLED 标识和上报仍然保持告警。
- `gasAdcRaw` 与 `gasAdcFiltered` 必须同时上报，以便在未标定阶段用 ADC 做安全分级并保留校准证据。
- `sensorFault = true` 时，`temperatureC`/`humidityRh` 使用最后一次有效值，并同时给出 `alarmCauses: ["sensor_fault"]`；不得用 0 冒充有效读数。
- Backend 只依据 `receivedAt` 与 `timestamp`（若存在）判断在线，不接受设备自报的 `network` 作为在线依据。
- 当前固件只在 MQTT 会话在线时才发布遥测，因此实际发出的 `network` 恒为 `online`；`reconnecting` 是契约保留值，当前实现不会产生。

### 3.3 未标定阶段的气体展示约束

`gasPpm` 在 `gasCalibrated = false` 时是未标定曲线估算值，**不得**用于定量结论。契约要求：

- 客户端在 `gasCalibrated = false` 时必须优先展示 ADC 分级（例如「安全 / 关注 / 超限」）而不是精确 ppm。
- Backend 的阈值同样以该估算值比较，单位与 `gasPpm` 一致；不得混用 ADC 与 ppm 两种单位。
- 完成预热、负载电阻确认与标准气体标定后，设备置 `gasCalibrated = true`，客户端可展示 ppm 数值。

## 4. Control Payload

主题 `device/control`。

修改阈值：

```json
{
  "schemaVersion": 1,
  "messageType": "control",
  "deviceId": "MCU001",
  "requestId": "01K5H0PN0M1N9NB8B7RBTVWT8P",
  "issuedAt": 1790246400000,
  "expiresAt": 1790246460000,
  "type": "set_thresholds",
  "payload": {
    "thresholdVersion": 4,
    "temperatureHighC": 30.0,
    "humidityHighRh": 80.0,
    "gasHighPpm": 80.0
  }
}
```

### 4.1 设备校验顺序（冻结）

前提是报文能被完整解析：只有解析成功并取得 `requestId` 的报文才进入以下校验并保证回执。无法解析的帧（JSON 畸形、必填字段缺失或为空、字段语法/类型非法、`requestId` 超过 32 字符）**不产生 command-ack**——回执必须携带 `requestId`，取不出就不能答——它们只计入设备侧计数器（见 §4.5），QoS 1 的 PUBACK 仍照常发送。

以下校验任一步失败立即以 `rejected` 回执（`errorCode` 见 §6），不得执行副作用：

1. `schemaVersion` 等于设备支持版本，且 `messageType` 为 `control`；
2. `deviceId` 与自身一致（防止错投）；
3. `requestId` 非空（1–32 字符）且未在本机去重表中出现过（见 §4.2）；
4. `expiresAt` 未过期（设备时钟不可信时，以「收到命令的本地单调时间 + 允许窗口」为判据，见 §4.4）；
5. `type` 是受支持类型；
6. 数值字段类型与范围合法（见 §4.3、§4.4）；
7. （仅 `set_thresholds`）`thresholdVersion` 严格大于当前生效版本（见 §4.3）。

### 4.2 requestId 去重

- 设备保留最近 N 条 `requestId` 的**处理结果**，用于重复命令回执 `duplicate`（当前固件 N = 8，足以覆盖 Backend 60 秒有效窗口内的 QoS 1 重投递）。
- 重复命令回执 `duplicate` 并**不重新执行**、不重复写 Flash；首次处理的实际结果（`applied`/`rejected` 等）以首次回执为准、由 Backend 持有，重复回执不重传该状态（见 §6）。
- 去重表在重启后允许丢失；丢失后重复命令按新命令处理是允许的，但设备时钟可判时超出 `expiresAt` 的命令仍必须拒绝为 `expired`（见 §4.4）。

### 4.3 阈值范围与版本（冻结）

| Field | Range | Unit |
| --- | --- | --- |
| `thresholdVersion` | 1 – 2147483647 | 无 |
| `temperatureHighC` | 0 – 80 | °C |
| `humidityHighRh` | 0 – 100 | %RH |
| `gasHighPpm` | 1 – 999 | ppm（估算值，与 `gasPpm` 同单位） |

- `thresholdVersion` 必须严格大于设备当前版本才接受；小于或等于当前版本的命令回执 `rejected`。
- **版本 1 定义为编译期默认阈值**（对应 `hardware/STM32_Project1/User/app_config.h`）。设备上电后 `thresholdVersion` 至少为 1，因此遥测中的 `thresholdVersion` 永不为 0。
- 只有 Flash 校验写入成功后才更新生效版本；写入失败必须回执 `failed` 并继续使用上一次有效配置。
- 阈值故意包含湿度：固件对湿度超限同样驱动声光报警，若云端无法配置该阈值，本地告警就存在无法远程收敛的盲区。

**设备端分辨率（固件实现约束，非契约放宽）**：DHT11 只有 1 °C / 1 %RH 分辨率，气体估算只到整 ppm，因此设备把收到的小数阈值**四舍五入到整数**（°C、%RH、ppm）后执行（30.5 → 31），且范围校验在取整之后进行。选择四舍五入而不是截断，是为了避免所有阈值被静默下移最多一个单位。`thresholdVersion` 仍然记录"配置已下发并写入 Flash"这一事实；客户端不应假设设备按小数位精确执行。若将来更换为分辨率更高的传感器，该舍入应当重新评估。

### 4.4 过期判据

`expiresAt` 是 UTC 毫秒，Backend 下发的有效窗口为 60 秒。窗口本身必须为正且不超过 24 小时（`COMMAND_MAX_WINDOW_MS`）：`expiresAt ≤ issuedAt` 或窗口超过 24 小时的报文在解析阶段即回执 `rejected`（`out_of_range`），该检查先于去重与过期判据（见 §4.5）。设备时钟未同步时无法比较绝对时间，因此：

- 设备在收到命令时记录本地单调时间 `receivedUptimeMs`；
- 若设备时钟已同步，直接比较 `expiresAt`；
- 若未同步，设备使用 Backend 在命令中给出的窗口长度（`expiresAt - issuedAt`）作为允许窗口，从 `receivedUptimeMs` 起算；
- 设备重启会清空去重表与任何在途状态，**不存在跨重启的命令队列**：重启前未执行的命令不会被「补执行」。重投递到达时按 §4.2 作为新命令重新校验——时钟可判时已过 `expiresAt` 的拒绝为 `expired`；时钟未同步时从新的 `receivedUptimeMs` 重新起算窗口，仍在窗口内的重投递命令会被正常执行（这正是 QoS 1 重投递的目的）。

### 4.5 设备端命令执行实现（固件约束，非契约放宽）

固件在 `hardware/core/control_link.c` 中实现本节的接收路径，以下为与实现强绑定的事实：

**校验顺序**：§4.1 的顺序为 schema/messageType → deviceId → requestId 去重 → expiresAt → type → 范围 → 阈值版本。固件按此顺序执行，以下三点是实现层面的可观测细节：

- 解析层（`hardware/core/command_json.c`）在返回前已完成 type、数值范围与窗口合法性校验，因此一条**同时**越界且已过期的命令回执为 `rejected`（`out_of_range`）而不是 `expired`。这类拒绝都不产生副作用，语义上都是「未执行」。
- 去重先于版本比较：重投递的阈值命令回执 `duplicate` 并携带当前生效的 `thresholdVersion`，而不是 `stale_version`。这是刻意的——Backend 已经记录了首次结果，回 `stale_version` 会被读成一次新的失败。已被去重表记住的 `requestId`，只要报文仍可解析，无论首次结果如何都回 `duplicate`。
- 解析失败（取不出 `requestId`）不产生 command-ack，只计入 `unaddressable_frames`；QoS 1 帧的 PUBACK 照常发送（前提见 §4.1）。

**ACK 的 `sequence`**：与遥测共用同一个「本次启动单调递增」计数器，因此遥测与 ACK 的 `sequence` 落在同一号段内，可用于排序两条流。控制路径中计数器**仅在 ACK 生成成功后自增**（遥测在发布成功后自增），无法生成 ACK 的帧不占号。

**QoS 0 的 `device/control`**：冻结契约规定该主题为 QoS 1。若收到 QoS 0，固件仍会校验并执行，并照常回 command ACK，只是不产生 PUBACK——QoS 0 没有可确认的投递。该分支是防御性的，不是受支持模式。

**离线期间收到的 PUBLISH**：未建立会话（未收到 SUBACK 或 TCP 断开）时，PUBLISH 只回 PUBACK，**不执行**。此类帧在设备上单独计数（`offline_frames`）。当同一个 TCP 接收缓冲中依次包含有效 SUBACK 与控制 PUBLISH 时，固件在确认 SUBACK 有效（状态处于等待 SUBACK、未携带 0x80 失败码、packetId 吻合）后立即切入在线会话，紧随其后的 PUBLISH 立即按在线执行并回复 command ACK，不会被误判为离线丢弃。

#### 4.5.1 接收缓冲的已知限制（不静默丢包）

ESP8266 驱动（`hardware/Esp8266/esp8266.c`）只有一个 TCP 接收缓冲、没有队列。因此：

| 情形 | 行为 |
| --- | --- |
| 一个 `+IPD` 内含多个 MQTT 帧 | **支持**。`MqttForEachPacket` 逐帧扫描整个缓冲，codec 拒绝的帧跳过而不终止扫描。 |
| `+IPD` 载荷长于接收缓冲（640 B） | 只保留前缀，计为 `truncated_frames`；该帧无法解码，靠 QoS 1 重投递。 |
| 前一帧尚未取走时又到一个 `+IPD` | 丢弃**新**帧并计为 `discarded_frames`（保留较早帧：它可能是会话在等的 CONNACK/SUBACK）。不覆盖、不静默。 |
| 一个 MQTT 帧被拆到两个 `+IPD` 中 | **不支持**。驱动不保留半帧状态，第二个分片到达时前一帧已按新帧处理；结果是一个尾部残帧，计为 `partial_frames`。 |

驱动层的 `discarded_frames`、`truncated_frames` 通过 `ESP8266_GetReceiveStats` 读取，非零时显示在 OLED 网络页（`RX Dxxx TRxxx`）；`partial_frames`、`offline_frames` 属 `ControlLink` 计数器（`hardware/core/control_link.h`），不经该接口、不上 OLED。**丢失只靠 `device/control` 的 QoS 1 重投递恢复**，这也是该主题必须是 QoS 1 的实现层理由。

## 5. 去重、排序与在线判定

### 5.1 遥测唯一性与顺序

- `sequence` 是本次启动期间单调递增的无符号计数，每次上电从 0 重新开始。
- `bootId` 在每次上电时重新生成，设备内唯一即可（例如基于备份寄存器复位计数与 `uptimeMs` 的短哈希，最多 16 个 ASCII 字符）。
- **Backend 的唯一键与去重键为 `(deviceId, bootId, sequence)`**。仅使用 `sequence` 会在设备重启后把合法新数据误判为重复。
- 同一 `(deviceId, bootId, sequence)` 的重复投递必须幂等：只入库一次，重复报文只计入重复计数。
- 乱序投递（`sequence` 回退但 `bootId` 相同）按到达顺序入库，但不得回退 `latest` 指针与告警窗口；告警计算以事件时间为序，见 Backend 文档。

### 5.2 心跳与离线判定

- **遥测报文即心跳**，不单独定义心跳主题。契约上报周期 5 秒（Backend `liveness` 冻结时序与 `device-sim` 均按 5 秒实现）。
- Backend 在连续 3 个上报周期（默认 15 秒，`OFFLINE_AFTER_SECONDS` 可配置、且不得小于 3 个上报周期）未收到有效遥测时判定 `offline`。
- 恢复收到合法遥测后立即置 `online` 并产生状态变化事件。
- 告警状态变化时设备应立即补报一次，不等下一个上报周期。**实现状态**：当前固件未实现变化即报，仅按固定周期上报。
- Broker 连接状态不得替代应用层最后遥测时间。
- **已知实现偏差（2026-09-22）**：固件的名义上报周期为 1 秒（`UPLINK_PERIOD_TICKS = 10` × `LOCAL_TICK_MS = 100 ms`，见 `hardware/STM32_Project1/main.c`），快于契约的 5 秒。更频繁的上报不影响 15 秒离线判定，但与 Backend/模拟器的 5 秒假设不一致，待固件侧收敛；实机主循环的实际耗时会拉长该周期（见 `hardware/README.md`）。

## 6. Command Acknowledgement

主题 `device/command-ack`（**契约冻结决策**：从草案的「复用 `device/telemetry`」改为独立主题，避免遥测与 ACK 两种 Schema 混在同一流上，便于 Broker ACL 与校验分离）。

```json
{
  "schemaVersion": 1,
  "messageType": "command_ack",
  "deviceId": "MCU001",
  "bootId": "9f3ac21b",
  "sequence": 43,
  "timestamp": null,
  "uptimeMs": 126000,
  "requestId": "01K5H0PN0M1N9NB8B7RBTVWT8P",
  "status": "applied",
  "thresholdVersion": 4,
  "errorCode": null
}
```

`status` 枚举（冻结）：

| Status | Meaning |
| --- | --- |
| `applied` | 命令已执行并生效（阈值命令同时表示 Flash 写入成功） |
| `rejected` | 校验失败，未产生副作用；`errorCode` 必填 |
| `expired` | 超过 `expiresAt`，未执行 |
| `duplicate` | `requestId` 已处理过，未重新执行；首次结果以首次回执为准，本回执不携带该状态 |
| `failed` | 校验通过但执行失败（例如 Flash 写入或校验失败）；`errorCode` 必填 |

`errorCode` 枚举（冻结）：`schema_unsupported`、`device_mismatch`、`bad_request_type`、`out_of_range`、`stale_version`、`flash_write_failed`、`flash_verify_failed`。

`thresholdVersion`：仅 `set_thresholds` 且结果为 `applied`/`duplicate` 时给出设备当前生效版本；其余结果为 `null`。已废弃的 `set_mute` 会被拒绝，不产生版本。

REST 控制接口返回 202 只表示命令已被 Backend 接受并进入发布流程，**不代表设备已执行**。客户端必须等待 ACK 或超时结果。

## 7. 从 TCP 文本帧到 MQTT 的迁移状态

迁移前的固件经 `hardware/Esp8266/esp8266.c` 的 `ESP8266_Task` 发送换行结尾的文本帧：

```text
REG|MCU001
APP001|<temperature>|<humidity>|<gasPpm>
```

其中 `APP001` 是硬编码帧标签，不是设备号；`<temperature>`、`<humidity>` 为整数，`<gasPpm>` 为整数估算值。该文本帧路径**保留在驱动中但已无调用方**（全仓库没有代码再调用 `ESP8266_Task`），当前主循环发送的是 MQTT CONNECT → SUBSCRIBE → PUBLISH/PING。

迁移规则（冻结）：

1. 主循环已停止调用旧 `APP001` 文本帧任务，改为 CONNECT → SUBSCRIBE → PUBLISH/PING 状态机。
2. 2026-09-23 的 v2 固件已在手机热点下复测 EMQX → Go → PostgreSQL 链路、`set_thresholds` ACK、Broker 停启后自动重连、气体阈值 30→80→30 对 `gas_high` 与蜂鸣器的影响，以及人工重新供电/Reset 后的 Flash version 10 保持。2026-09-22 的 v1 固件曾实测远程静音/解除；此能力已于 v2 删除，不能把历史结果当作当前功能。尚未进行受控掉电时刻注入与拔掉热点 AP 的测试。
3. 禁止在同一次未经验证的修改中同时迁移 HAL、重写传感器驱动并切换 MQTT。
4. 迁移期间字段映射：文本帧的 `<temperature>` → `temperatureC`（整数部分）、`<humidity>` → `humidityRh`、`<gasPpm>` → `gasPpm`；文本帧缺少的 `bootId`、`sequence`、`gasAdcRaw`、`gasAdcFiltered`、`thresholdVersion` 等字段必须在 MQTT 路径中补齐，不能靠 Backend 猜测。

## 8. 契约冻结决策记录

| ID | 决策 | 理由 |
| --- | --- | --- |
| FD-1 | ACK 拆分为独立主题 `device/command-ack` | 遥测与 ACK Schema 不同，混流会削弱校验与 ACL 表达能力；草案已允许拆分并要求同步契约 |
| FD-2 | 新增 `bootId`，去重键为 `(deviceId, bootId, sequence)` | `sequence` 每次重启归零，仅用 `sequence` 会丢数据 |
| FD-3 | 阈值新增 `humidityHighRh` | 固件湿度超限同样声光报警，缺少该字段会造成云端无法收敛的告警盲区 |
| FD-4 | 气体阈值与 `gasPpm` 同单位（估算 ppm），新增 `gasCalibrated` | 避免 ADC 与 ppm 混用；未标定时在契约层强制降级为分级展示 |
| FD-5 | 遥测新增 `messageType`、`sensorFault`；`network` 仅由设备自报 | 与 ACK 分流后需要报文类型标识；传感器故障必须有显式字段而非伪造 0 值 |
| FD-6 | `thresholdVersion = 1` 定义为编译期默认阈值 | 使「设备从未收到过阈值命令」有明确表示，避免 0/未定义 |
| FD-7 | 遥测即心跳，5 秒周期，15 秒判离线 | 避免为心跳引入独立主题与额外状态 |
| FD-8 | 保留 `rapid_temperature_rise`、`rapid_gas_rise` 枚举 | 固件本地快速通道与 Backend 复合预警都需要表达该类告警原因 |
| FD-9 | Backend 侧 `acknowledged` 告警状态移出首期冻结范围 | 首期没有告警确认接口，保留该状态会形成无法产生的契约；作为二期扩展值记录在 Backend 文档 |
| FD-10 | 阈值范围 `temperatureHighC` 0–80、`humidityHighRh` 0–100、`gasHighPpm` 1–999 | 与固件 `uint8` 采样能力及 APP001 帧整数取值范围一致，留出余量 |
| FD-11 | 新增 `GET /api/v1/devices/{deviceId}/commands/{requestId}` | 客户端断线重连后必须能查询命令最终结果，否则 `accepted`/`applied` 无法区分，超时结果不可见 |
| FD-12 | 复合火情的证据字段使用 `gasAdcRise` / `gasAdcRiseThreshold`（ADC 码），不使用 ppm | 增量是差值，未标定时仍然有效；`gasAdcFiltered` 恒有值而 `gasPpm` 可为 `null`。若用 ppm 表达，未标定设备将无法产生可解释的告警证据 |
| FD-13 | 实现层不保留 `not_implemented` 错误码 | 首期路由全部实现，保留一个不会被产生的错误码会形成无法验证的契约；实时流未配置改为启动期错误而非运行期响应 |

## 9. 变更流程

修改本契约必须同时：

1. 更新本文件与 `docs/api/openapi.yaml`；
2. 更新 `backend/docs/api.md` 与 Handler、模型、测试；
3. 在 Multica 中于 Hardware / Backend / 客户端各自 issue 交叉引用，说明兼容性、迁移与版本策略；
4. 请求 Hardware、KMP、微信端负责人评审；
5. `schemaVersion` 不兼容变化必须递增，并记录旧版本的处理方式（拒绝并记录，不静默降级）。
