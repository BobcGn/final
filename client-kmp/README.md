# client-kmp

实验室微环境动环监控与早期火情预警 —— 客户端方案 A（Kotlin Multiplatform）。

本工程让 **Android** 与 **微信小程序** 消费同一套业务运行时：接口约定、状态派生、
校验和错误映射只写一次，两端的标签、颜色语义和数值格式由共享层输出，避免两端各自
解释同一份 Backend 契约而产生分歧。

事实源：

- API 契约：[`../docs/api/openapi.yaml`](../docs/api/openapi.yaml)
- 设备协议：[`../docs/device-protocol.md`](../docs/device-protocol.md)
- 本模块约束：[`AGENTS.md`](./AGENTS.md)，根规则：[`../AGENTS.md`](../AGENTS.md)

---

## 一、架构与 source set 边界

| Source set | 内容 | 禁止内容 |
| --- | --- | --- |
| `commonMain` | 纯业务运行时：模型、解析、状态派生、校验、错误映射 | Compose、WXML/WXSS、任何平台 UI |
| `miniappMain` | 微信/JS Runtime 平台适配与 CommonJS 导出 | 微信 UI、WXML/WXSS |
| `androidMain` | Android 传输适配（`HttpURLConnection`）+ Compose 四页面 | 共享业务规则 |
| `iosMain` | iOS 入口（Compose），保留既有模板 | — |
| `miniApp/`（仓库目录，非 Gradle source set） | 微信 Host UI：WXML/WXSS/JS | 共享业务规则 |

关键约束：

- `commonMain` 只保存真正可共享的业务逻辑，不承载 Compose 或 WXML/WXSS。
- Android UI 与 MiniApp Host UI 完全独立，只共用 `commonMain` 的运行时与 **展示模型**。
- 平台能力（HTTP、幂等键）通过 `MonitoringPlatform` 接口隔离，`commonMain` 不引用任何平台 API。
- 不复制 `client-wx-native` 的内部实现；只参考其视觉与交互语义。

### 共享业务逻辑（两端共同消费）

`shared/src/commonMain/kotlin/org/example/client_kmp/monitoring/`

| 文件 | 职责 |
| --- | --- |
| `Models.kt` | 契约模型：`DeviceStatus`、`TelemetryPoint`、`AlertEvent`、`Thresholds`、`CommandStatus`、错误信封 |
| `Transport.kt` | `HttpRequest` / `HttpResponse` / `MonitoringPlatform` 平台边界（含 `nowMillis()` 时钟）、`MonitoringException` |
| `TimeFormat.kt` | 纪元毫秒 → RFC 3339 UTC 的格式化与时间窗的毫秒换算。不引入 `kotlinx-datetime`：共享层不应为一次区间查询拉进一个平台日期层，而这段整数日历算法可以完全单测 |
| `Presentation.kt` | 仪表盘派生、趋势统计、告警展示模型、阈值校验、命令生命周期措辞、`Tone` 颜色 token |
| `MonitoringClient.kt` | 端点路径、序列化、幂等键、错误信封映射、202 控制闭环轮询 |

两端**只有一处**业务实现：`MonitoringClient` + `MonitoringPresentation`。
Android UI 和 WXML 都只负责排版。

### 平台适配

- Android：`androidMain/.../AndroidMonitoringPlatform.kt`，阻塞 IO 固定在 `Dispatchers.IO`，
  幂等键用 `UUID.randomUUID()`。
- 微信：`miniappMain/.../MiniAppMonitoringExports.kt`，网络走 MiniApp SDK 的
  `MiniAppExports.networkRequest`，幂等键用「毫秒时间戳 + 进程内自增序号」
  （`crypto.randomUUID` 并非所有基础库可用）。

---

## 二、MiniApp bundle 与导出结构

`./gradlew prepareMiniAppHost` 把插件产出的 CommonJS 发行包同步到 `miniApp/kotlin/`。

**实际入口文件（已核对，非假设）：**

```
miniApp/kotlin/client-kmp-shared-miniapp.js
```

文件名来自 Gradle 工程路径 `:shared` → `client-kmp-shared-miniapp`，并由同目录
`package.json` 的 `main` 字段声明。

**实际导出路径（已核对）：**

```js
bundle.org.example.client_kmp.monitoring.LabMonitorExports
```

`@JsExport` object 挂在 Kotlin 包路径下，**不在** bundle 根。`miniApp/runtime.js`
封装了这一路径，页面代码不直接触碰 `./kotlin`。

**导出成员：**

| 成员 | 说明 |
| --- | --- |
| `configure(baseUrl, deviceId)` | 同步，设置后端地址；须在任何数据调用前执行 |
| `selectors()` | 同步、无 I/O。返回两个选择器的选项表（`windows` / `filters`），供页面在首次取数之前画出趋势时间窗与告警筛选 |
| `dashboard()` | 返回 DashboardView JSON |
| `trends(window, limit)` | 返回 TrendsView JSON（按时间升序）。`window` 为 `LAST_HOUR` / `LAST_SIX_HOURS` / `LAST_DAY`，驱动 `from`/`to` 绝对边界；未知值回落到默认窗口 |
| `alerts(filter, limit)` | 返回 AlertsView JSON。`filter` 为 `all` / `fire_warning` / `suspect` / `recovered`；未知值回落到 `all` |
| `settings()` | 返回 SettingsView JSON |
| `commandStatus(requestId)` | 读取命令生命周期 |
| `awaitCommandOutcome(requestId)` | 等待设备确认；超时返回 `null`（未确认，非失败） |
| `mute(muted)` | 下发静音/恢复 |
| `updateThresholds(t, h, g)` | 校验并下发阈值 |

所有 `suspend` 函数在 JS 侧表现为 **Promise**；失败会 reject 成一个普通 `Error`，
`message` 可直接展示，并附带 `code` / `statusCode`。

> `miniApp/kotlin/` 是**生成目录**，已在 `miniApp/.gitignore` 中排除，禁止提交。
> 其中 Compose Multiplatform 插件附带的浏览器渲染资源（`skiko.wasm`、`skiko*.mjs`、
> ES module shim、source map）已由 `prepareMiniAppHost` 排除，并配有校验：
> 一旦这些文件出现或入口文件缺失，构建即失败。

### 编译产物分布

| 位置 | 内容 | 是否交付微信 | 版本控制 |
| --- | --- | --- | --- |
| `miniApp/` | 微信工程：页面/WXSS/JS + `kotlin/` bundle，**自包含** | 是 | 提交（`kotlin/` 除外） |
| `build/js` | Kotlin/JS **测试**工具链：node_modules、测试包 | 否 | 忽略 |
| `build/kotlin-js-store` | Kotlin/JS 的 yarn lock store | 否 | 忽略 |
| `shared/build/miniapp` | 插件产出的 bundle 暂存区 | 否 | 忽略 |
| `shared/build/processedResources/miniapp` | 处理后的资源 | 否 | 忽略 |
| `.kotlin/`、`shared/build/kotlin` | 编译缓存 | 否 | 忽略 |

两点保证：

1. **`client-kmp/` 根目录不再出现任何小程序或 JS 产物。**
   Kotlin/JS 默认把 yarn store 放在 `<root>/kotlin-js-store`，既不是构建目录、
   也不属于微信交付物。已在根 `build.gradle.kts` 中通过
   `YarnRootExtension.lockFileDirectoryProperty` 改到 `build/kotlin-js-store`。
   本项目没有 npm 运行时依赖（`package.json` 的 `dependencies` 为空），
   该 store 只固定跑测试用的 TypeScript 版本，放进 `build/` 无副作用。
2. **`miniApp/` 自包含，可整体拷贝/上传。**
   `prepareMiniAppHost` 会解析 `miniApp/` 下所有 `.js` 的 `require('./…')`，
   若有相对引用落在 `miniApp/` 之外即构建失败——避免「本地能跑、拷走后崩」。

---

## 三、运行与配置

### 后端地址

| 运行环境 | base URL | 原因 |
| --- | --- | --- |
| Android 模拟器 | `http://10.0.2.2:8080` | `10.0.2.2` 是模拟器指向宿主机的固定别名 |
| 微信开发者工具（本机） | `http://127.0.0.1:8080` | 工具与后端同机 |
| 微信真机 | `http://<Mac 在手机热点/局域网中的 IP>:8080` | 真机的 `127.0.0.1` 是手机自己，永远访问不到 Mac |

- Android：`shared/src/androidMain/.../App.kt` 的 `EMULATOR_BASE_URL`。
- 微信：`miniApp/config.js` 的 `baseUrl` / `deviceId`。**局域网 IP 属于部署环境信息，
  不要写进仓库**；真机调试时本地改成宿主机 IP 即可。

### Android

```bash
cd client-kmp
./gradlew --no-configuration-cache :androidApp:assembleDebug
```

产物：`androidApp/build/outputs/apk/debug/androidApp-debug.apk`

安装到已启动的模拟器：

```bash
adb install -r androidApp/build/outputs/apk/debug/androidApp-debug.apk
```

`AndroidManifest.xml` 已声明 `INTERNET` 权限，并允许本地明文 HTTP（仅用于开发调试）。

### 微信小程序

```bash
cd client-kmp
./gradlew --no-configuration-cache prepareMiniAppHost
```

然后在**微信开发者工具**中导入目录：

```
client-kmp/miniApp
```

导入前必须确认：

1. 已执行 `prepareMiniAppHost`，`miniApp/kotlin/` 存在且非空。
2. 「详情 → 本地设置」勾选 **不校验合法域名、web-view（业务域名）、TLS 版本以及 HTTPS 证书**，
   否则开发工具会拦截到 `http://` 明文地址的请求。
3. 真机预览时把 `config.js` 的 `baseUrl` 改成 Mac 的局域网 IP。

生产环境必须使用 HTTPS 域名并在小程序后台配置合法域名；`wx.request` 在非开发者模式下拒绝明文 HTTP。

> **包体积阻塞**：`miniApp/kotlin/` 当前约 2.3 MB，尚未在微信开发者工具中完成
> 真实预览/上传验证。`project.config.json` 虽已开启 `minified`，但在工具确认
> 处理后包体满足限制前，不得将“可上传”视为已验收。若仍超限，需要继续
> 缩减 JS 运行时或按微信规则拆分包。

---

## 四、测试与覆盖率

```bash
cd client-kmp

# 共享逻辑 + Android 平台适配（JVM）
./gradlew --no-configuration-cache :shared:testAndroidHostTest

# 共享逻辑 + MiniApp 平台适配（Node/JS，微信侧运行时）
./gradlew --no-configuration-cache :shared:miniappTest

# MiniApp 运行时不得携带 Compose/Skiko
./gradlew --no-configuration-cache :shared:checkMiniAppHostBoundary

# 微信工程自包含 + 入口文件存在 + 「只有设备 ACK 才算成功」的闸门（root 工程任务）
./gradlew --no-configuration-cache checkMiniAppHostSelfContained

# 覆盖率报告（Kover）；koverVerify 对共享逻辑执行 80% 行覆盖下限
./gradlew --no-configuration-cache :shared:koverXmlReport :shared:koverVerify

# Android 调试包
./gradlew --no-configuration-cache :androidApp:assembleDebug

# 全量（含上面的自包含检查）
./gradlew --no-configuration-cache check
```

> 任务名注意：本仓库**没有** `:shared:jsNodeTest` 任务。JS/Node 侧的测试任务名是
> `:shared:miniappTest`（其执行器是 `:shared:miniappNodeTest`）。另外
> `checkMiniAppHostBoundary` 是插件任务，而更严格的「微信工程自包含 + toast 闸门」
> 检查是 root 工程的 `checkMiniAppHostSelfContained`（它 `dependsOn` `prepareMiniAppHost`；单独执行
> `prepareMiniAppHost` 只做同步，不会运行闸门）。

覆盖率口径：仅统计 `org.example.client_kmp.monitoring`（两端共同的业务规则），
排除编译器生成的嵌套类。Compose 页面、WXML Host 与生成 bundle 属于 Host 代码，
不计入共享规则覆盖率。

### 已验证目标（本轮实际执行）

| 项目 | 结果 |
| --- | --- |
| `:shared:testAndroidHostTest` | 106 tests，0 failures |
| `:shared:miniappTest`（Node/JS） | 107 tests，0 failures |
| `:shared:koverVerify` | PASS（80% 行覆盖下限） |
| 共享业务逻辑行覆盖率 | **527 / 529 行（99.6%）**；本轮新增/修改的类型均为 100% |
| `:shared:checkMiniAppHostBoundary` | PASS |
| `prepareMiniAppHost` + `checkMiniAppHostSelfContained` | PASS |
| `:androidApp:assembleDebug` | PASS，产出 debug APK（约 12.6 MB） |
| `./gradlew check` | PASS |

覆盖率逐类（Kover XML）：`Rfc3339` 39/39、`MonitoringPresentation` 188/188、`TrendWindow`、
`AlertFilter`、`SelectOption`、`SelectorOptions`、`CurveLegend`、`TrendsView`、`MetricSummary`、
`SettingsView`、`DashboardView` 均 100%。唯二未覆盖行在 `MonitoringClient`（81/83），是既有的
分支，与本轮改动无关。

Android 平台测试对真实 loopback HTTP 服务发起请求，覆盖 `HttpURLConnection` 的
`inputStream` / `errorStream` 分支与请求体写出，不使用 mock 替代网络边界。

**Android 模拟器验证（2026-09-22，Pixel_9_Pro / 1280×2856 / 480dpi，连本机 Backend）**：
四个页面均已截图并逐页核对信息结构、标题/副标题、间距、整数示数与底部导航选中态——
见下节「实机/模拟器验证结果」。

**未验证**：微信开发者工具中的实际渲染（本机 DevTools 服务端口未开启，CLI 无法驱动，
详见下节）、真机（手机）网络联通、iOS UI。**不得把「能编译」当成「真机通过」。**

### iOS

- iOS target（`iosArm64`、`iosSimulatorArm64`）保留，未删除。
- 本轮**不验证 iOS UI**：当前 macOS 27.2 环境不作为 iOS 界面验收依据，iOS Compose 页面
  与 `iosApp` 未构建、未运行。
- 唯一与 iOS 有关的已验证项是 `:shared:iosSimulatorArm64Test`，它只跑共享业务逻辑，
  不覆盖任何 iOS UI 行为。

---

## 五、与微信原生 baseline 的对齐

`client-wx-native` 是**视觉与交互 baseline**：四个页面的信息结构、标题/副标题、暗色背景与薄荷绿强调色、卡片圆角与间距、字号层级、底部四项导航与选中态都按它对齐（对齐以 Android 端四页为准；KMP 的 MiniApp 宿主当前只有 `pages/monitor` 一页，见 `miniApp/app.json`）。本轮以它为准的项目：

- 趋势页：`近1小时 / 近6小时 / 近24小时` 时间窗选择器（驱动查询的 `from`/`to` 绝对边界）、温度/湿度/气体三张统计卡（平均为数字、最低、最高、**峰值时间**）、以及**曲线图区**——图例 + 四条网格线 + Canvas 折线图 + 两端轴标签。
- 告警页：`全部 / 火情 / 疑似 / 已恢复` 筛选条；卡片为「状态头 + 触发证据面板（2×2）+ 恢复行」。
- 设置页：温度上限与气体浓度上限两个滑块；期望版本 / 设备确认版本 / 同步状态与保存按钮文案。
- 监控页：风险卡、三张仪表卡、设备状态卡、蜂鸣器控制卡。

### Baseline 仅为参考，不构成代码依赖

本工程**不引用** `client-wx-native` 的任何代码或资源，也不复制它的内部 JS 实现；上述对齐只是页面结构、视觉 token 与交互语义上的对照。`client-wx-native` 不因本工程而修改。

### 有意偏离 baseline 的地方（每条都写明原因）

| 项目 | baseline | 本工程 | 原因 |
| --- | --- | --- | --- |
| 温湿度/气体示数 | 保留一位小数（`toFixed(1)`） | **严格整数** | DHT11 只有 1 ℃ / 1 %RH 分辨率、气体估算只到整 ppm；小数是把整数再格式化出来的，会让人相信一个从未测到的位数。统计里的平均值同样取整，否则卡片上会重新出现小数 |
| 设置页湿度上限 | 未提供 | 同样不提供控件，但**仍随每次下发携带当前值** | 契约要求阈值更新必须带齐三个字段，丢掉湿度会让每次保存被拒。缺一个可编辑的湿度控件属未完成项，见下节 |
| 温度上限滑块步长 | `0.5` | `1` | 设备把小数阈值四舍五入到整度执行（`docs/device-protocol.md` §4.3）；步长 0.5 会让「显示 30.5、设备执行 31」 |
| 告警筛选的第三个 pill | `已确认` | `疑似` | 后端告警状态枚举没有 `acknowledged`（只有 `normal`/`suspect`/`fire_warning`/`recovered`），照搬会让该 pill 永远筛不出任何记录 |
| 趋势曲线 | Canvas 2D 真实折线图 | Android Compose Canvas 与 MiniApp Canvas 2D 真实三指标折线图 | 两端均接入独立 Y 轴缩放（10% padding）、气体缺失（null）打断折线段、时间比例 X 轴分布；`curveReady` 在有数据时为 `true` |

### 运行选择器与取数的关系

时间窗不是在前端过滤已经取回的数据，而是变成查询的绝对边界：

```
GET /api/v1/devices/MCU001/telemetry
    ?from=<now - window>&to=<now>&limit=200&order=desc
```

`order=desc` 是有意的：只给 `limit` 会返回区间内**最早**的那些行，于是「近24小时」描述的是它开头的几分钟，却被当成整段区间。取到之后在共享层反转为时间升序，统计与展示都按升序读。

**已知边界**：契约没有聚合端点，一个窗口内的样本数可能超过一页（按契约 5 秒周期一小时约 720 行、当前固件名义 1 秒则更多——见 `docs/device-protocol.md` §5.2——而页面大小是 200）。因此统计描述的是**该窗口内最近一页**的样本，不是窗口内全部样本；「共 N 条样本」里的 N 是这一页的行数。要覆盖整个窗口需要聚合端点。

### 实机/模拟器验证结果（2026-09-22）

**Android 模拟器**（Pixel_9_Pro，1280×2856，480dpi，`http://10.0.2.2:8080` 连本机 Backend）：
（下表「截图」列为验收时的本地文件名，**未提交入库**——仓库不含 `android-*.png`；核对结论以文字记录为准。）

| 页面 | 截图 | 核对结果 |
| --- | --- | --- |
| 监控 | `android-1-dashboard.png` | 「机房环境总览 / 智慧机房 · 实时动环监测」；风险卡 + 在线 pill；温度 32 / 湿度 42 / 气体 95 **均为整数**；设备状态三行；蜂鸣器控制卡带 baseline 文案「静音不影响环境检测与告警上报」 |
| 趋势 | `android-2-trends.png` | 三个时间窗 pill，「近1小时」为选中态；三张统计卡含最低/最高/峰值时间（如「峰值 02:55:36」）；曲线区是真实折线图（图例 + 网格线 + Canvas 三指标曲线 + 区间起点/此刻），**没有采样列表**；页脚提示与 baseline 一致 |
| 告警 | `android-3-alerts.png` | 「告警记录 / 早期火情预警事件」；四个筛选 pill；卡片为状态 pill + 触发证据 2×2（气体 ADC 上升 1204 / 触发阈值 150 / 温升速率 / 样本数）+ 恢复行 |
| 设置 | `android-4-settings.png` | 仅温度上限与气体浓度上限两个滑块，轨道与滑块为薄荷绿（非 Material 默认紫）；设备确认四行；期望版本 = 设备确认版本 = 5 |

**微信开发者工具**：**未执行**。本机 DevTools 的「服务端口」处于关闭状态，CLI 打开项目时报
`IDE service port disabled`，且该开关只能通过工具 GUI（设置 → 安全设置 → 服务端口）打开，
无法在无 TTY 的自动化环境里确认；`miniprogram-automator` 因此无法连接并逐页截图。
MiniApp 侧目前只有 `:shared:miniappTest`（JS 运行时）与 `checkMiniAppHostSelfContained`
（微信工程自包含、入口文件、toast 闸门）两类证据，**不构成渲染验收**。解除条件：
在 DevTools 中开启服务端口后执行 `automator.launch({projectPath: 'client-kmp/miniApp'})` 即可。

**后端契约状态**：后端主线已完成字段修复（PR #20 已合入 `main`），`GET /alerts` 返回的 `evidence` 对象严格遵循 `docs/api/openapi.yaml` 与 `backend/docs/api.md` 规范输出 camelCase（`gasAdcRise`、`sampleCount`…）。KMP 共享层运行时已按规范对齐并正确解析，代码与契约已解除阻塞；但本轮修复未重新在真机/模拟器进行端到端渲染复验。

## 六、当前不支持 / 未完成

- **实时推送未接入**：契约中 `/ws/v1/...` WebSocket 与 `WsEnvelope` 尚未在客户端实现，
  当前仅 REST 轮询（仪表盘 3 秒）。
- **告警确认（acknowledge）未实现**：阶段一无该端点，`AlertState` 因此不含 `acknowledged`。
- **趋势曲线已接入真实折线图**：Android Compose Canvas 与 MiniApp Canvas 2D 均已接入真实三指标折线图（温度、湿度、气体独立 Y 轴缩放，气体缺失打断，时间比例 X 轴）；受限于单页查询，当前折线图绘制最近一页最多 200 条样本。
- **历史查询无分页**：窗口已用 `from`/`to` 表达（见上节），但只取一页；`cursor` 未暴露，
  窗口内样本多于一页时统计只覆盖最近一页。
- **设置页缺湿度控件**：baseline 只有温度与气体两个滑块，本工程照此实现；湿度值仍随每次
  下发携带，但没有可编辑入口。补一个湿度滑块属未完成项。
- **告警缺少 `acknowledged` 状态**：后端状态枚举没有该值，因此筛选条的第三个 pill 用
  「疑似」替代 baseline 的「已确认」。
- **鉴权未接入**：契约的 `BearerAuth` 为阶段一占位，后端本地以 `AUTH_MODE=none` 运行，
  客户端未发送 Token。
- **小程序包体积**：见上节「包体积」风险。
- **微信开发者工具渲染**：未执行（服务端口未开启，见上节验证结果）。
- **真机（手机）网络联通**：未执行。
- **端到端告警渲染重验**：后端契约虽已在 `main` 修复对齐，但本分支未重新进行真机/模拟器端到端渲染验收。

---

## 七、协作流程

```bash
git switch main && git pull --ff-only
git switch -c feat/kmp-monitor-dashboard

cd client-kmp && ./gradlew check && cd ..

git add client-kmp
git commit -m "feat(kmp): add monitor dashboard state"
git push -u origin feat/kmp-monitor-dashboard
```

分支必须采用 `<type>/kmp-<complete-description>`，例如 `fix/kmp-telemetry-parsing`。
PR 关联 Multica issue，说明受影响 source set、共享边界、契约影响及各目标验证结果。
不得把微信 WXML/WXSS 或平台 UI 放入 `commonMain`，不得在 `main` 上直接提交。

---

了解更多：[Kotlin Multiplatform 文档](https://www.jetbrains.com.cn/en-us/help/kotlin-multiplatform-dev/get-started.html)
