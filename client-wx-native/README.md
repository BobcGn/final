# WeChat Native Client

该目录是客户端方案 B：使用微信官方原生小程序 UI、生命周期和 API 的独立实现，后续作为 KMP MiniApp 方案的工程对照 baseline。

## Current Status

业务页面包含实时监控（dashboard）、历史趋势（trends）、告警记录（alerts）、阈值设置（settings）四个 tab 页，以及公共服务层（services/）与表现逻辑/工具函数（utils/）。底部导航图标位于 assets/icons/。

- **示数与逻辑严格对齐 client-kmp**：
  - 传感器示数 DHT11 / MQ135 统一四舍五入取整显示（无小数位），避免伪精度；
  - 燃气未校准或未测量（`gasPpm: null`）严格显示占位符 `--`，绝不静默回退为 0；
  - 仪表盘百分比量程严格按契约上限归一化：温度 80 °C、湿度 100 %RH、烟雾 999 ppm；
  - 告警记录按 4 种类型筛选（全部、火警预警、疑似异常、已恢复），告警证据展示 ADC 读数增量（`gasAdcRise`）；完全移除远程静音与已确认逻辑；
  - 阈值设置严格提交完整三字段（含 `humidityHighRh`），校验范围 0-80 / 0-100 / 1-999；设置后采用 `GET /commands/{requestId}` 轮询下发状态直至生效。
- **实时监控刷新**：仅监控大屏按 3000ms 周期获取 `status + latest` 原子快照（串行守卫防止请求重叠）；趋势页按需加载历史数据。最新遥测 404 时降级为空状态提示。
- **Mock 与联调**：当前通过 `config/env.js` 的 `useMock` 开关使用本地 Mock 数据（`services/mock/`），Mock 响应字段与 OpenAPI 契约及状态机一致；联调时将 `useMock` 置为 `false` 即可切换真实接口。

## Project Boundary

- 与 KMP 客户端实现完全相同的业务能力与交互语义，并使用相同 Backend API、硬件数据源和验收场景。
- 保持微信原生工程方式和真实开发成本，不为了匹配 KMP 目录结构而人为改造。
- 不依赖 `client-kmp` 的内部实现，跨客户端只共享已确认的外部契约和需求事实。
- 趋势页使用原生 Canvas 2D 折线图；纯逻辑、数据转换与异步状态机收敛于 `utils/` 与 `services/`，由 Node.js 自动化测试覆盖。微信开发者工具视觉验收与真机验证仍需单独进行。

## Automated Tests

运行本地单元测试与覆盖率统计（Node.js >= 18）：

```sh
# 运行全部微信端测试
node --test client-wx-native/tests/*.test.js

# 检查 utils 与 services 的代码覆盖率
node --test --experimental-test-coverage \
  --test-coverage-include="client-wx-native/utils/**" \
  --test-coverage-include="client-wx-native/services/**" \
  client-wx-native/tests/*.test.js
```

使用微信开发者工具打开本目录即可运行。`project.config.json` 中已有项目配置与团队确认的 AppID；`project.private.config.json` 等本机私有文件不得提交。

## Collaboration Workflow

```sh
git clone <repository-url>
cd final
git switch main
git pull --ff-only
git switch -c fix/wx-monitor-parity

# 运行自动化测试与静态检查
node --test client-wx-native/tests/*.test.js

# 在微信开发者工具中完成编译、模拟器和必要的真机验证
git add client-wx-native
git commit -m "fix(wx): align presentation with kmp specification"
git push -u origin fix/wx-monitor-parity
```

分支必须采用 `<type>/wx-<complete-description>`，例如 `fix/wx-client-kmp-parity`。PR 关联 Multica issue，说明页面/组件、微信 API、Backend 契约和开发者工具/真机验证结果；经审核和检查通过后方可合并。`project.private.config.json` 等本机私有文件不得提交。
