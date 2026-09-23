# WeChat Native Client

该目录是客户端方案 B：使用微信官方原生小程序 UI、生命周期和 API 的独立实现，后续作为 KMP MiniApp 方案的工程对照 baseline。

## Current Status

业务页面已完成并对齐 KMP 方案：包含机房环境总览（dashboard）、历史趋势（trends）、告警记录（alerts）、预警阈值（settings）四个 tab 页。底部导航使用自定义 `custom-tab-bar`，图标位于 `assets/icons/`。

后端全部路由已实现（2026-09-22，见 `backend/README.md`）。当前通过 `config/env.js` 的 `useMock` 开关使用本地 Mock 数据（`services/mock/`），Mock 响应字段与新契约一致（`gasPpm` 可为 null、告警证据用 ADC 码、阈值三字段、控制命令生命周期可用）；后端就绪后将 `useMock` 置为 `false` 即可切换真实接口，页面代码无需改动。

## 与 KMP 方案的对齐（重要）

两端必须对同一份后端数据给出相同的示数与相同的实时行为，因此本端按 `client-kmp` 共享层
（`MonitoringClient` + `MonitoringPresentation`）的分层实现：

| 层 | 文件 | 职责 |
| --- | --- | --- |
| 格式化 | `utils/format.js` | 读数取整（整数、half-up）、百分比按契约量程、`--` 空态、`clockText`、RFC 3339 边界 |
| 量程 | `utils/threshold-limits.js` | 契约量程常量与下发前校验（文案与 KMP 一致） |
| 展示 | `utils/presentation.js` | `dashboard` / `trends` / `alerts` / `settings` / `commandStatus` 视图派生，字段名与 Kotlin data class 一一对应 |
| 数据 | `services/monitoring.js` | 端点路径、查询构造（绝对 `from/to`、`order=desc` 后本地反转）、幂等键、错误映射、命令终态轮询 |

关键约定：

- **示数为整数**：DHT11 分辨率 1 °C / 1 %RH、气体估计 1 ppm，小数是设备产不出的精度；
  取整用 half-up，与固件对阈值取整的规则一致。气体未标定时显示 `--`，不显示 `0`。
- **实时策略**：仪表盘每 **3000 ms** 重新取「status + latest」组成一次原子快照，
  只在本页可见时轮询，`onHide`/`onUnload` 立即停止；刷新失败不中断轮询。
  趋势/告警/设置页按需拉取，不参与轮询。
- **不使用 WebSocket**：KMP 方案不含实时订阅，实时性由 3 秒快照轮询保证；
  后端 `ws/v1` 已实现，但两端若各用一套推送/轮询会再次出现刷新节奏不一致，
  因此本端保持与 KMP 相同的轮询模型；如要接入 WS 应作为独立提案，并保留轮询兜底。
- **控制命令**：入队响应只代表后端已接受，必须轮询 `GET /commands/{requestId}`
  观察设备终态，只有 `applied` 才算成功，超时不得提示成功。

## 测试

纯 JS 业务逻辑（格式化、展示层、数据层、Mock 契约形状、REST 失败路径）均有单元测试，
使用 Node 内置测试器，无第三方依赖：

```sh
node --test tests/*.test.js
node --test --experimental-test-coverage --test-coverage-include="utils/**" --test-coverage-include="services/**" tests/*.test.js
```

## Project Boundary

- 后续与 KMP 客户端实现相同业务能力，并使用相同 Backend API、硬件数据源和验收场景。
- 保持微信原生工程方式和真实开发成本，不为了匹配 KMP 目录结构而人为改造。
- 不依赖 `client-kmp` 的内部实现；跨客户端只共享已确认的外部契约和需求事实。
- 示数与实时行为必须与 KMP 方案一致；页面不自行格式化数值，也不自行发明刷新策略。
- 趋势页折线图由 `utils/trend-chart.js` 在 Canvas 2D 上绘制（各指标独立量程、气体缺失分段），
  数据来自与统计同一批样本；后续如需换成 ECharts 应保持同一数据口径。


使用微信开发者工具打开本目录即可运行。`project.config.json` 中已有项目配置与团队确认的 AppID；`project.private.config.json` 等本机私有文件不得提交。

## Collaboration Workflow

```sh
git clone <repository-url>
cd final
git switch main
git pull --ff-only
git switch -c feat/wx-monitor-dashboard

# 在微信开发者工具中完成编译、模拟器和必要的真机验证
git add client-wx-native
git commit -m "feat(wx): add monitor dashboard"
git push -u origin feat/wx-monitor-dashboard
```

分支必须采用 `<type>/wx-<complete-description>`，例如 `fix/wx-threshold-validation`。PR 关联 Multica issue，说明页面/组件、微信 API、Backend 契约和开发者工具/真机验证结果；经审核和检查通过后方可合并。`project.private.config.json` 等本机私有文件不得提交。
