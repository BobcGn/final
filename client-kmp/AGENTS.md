# KMP Client Agent Rules

- `commonMain` 只保存真正可共享的客户端业务逻辑。
- Android UI 与 MiniApp UI 不得放入 `commonMain`。
- 平台能力必须通过清晰的接口或 Adapter 隔离。
- `miniappMain` 用于微信/JavaScript Runtime 适配，不承载微信 UI；该 source set 已投产（监控页导出与 bundle 管线见 README §二）。
- 微信 WXML/WXSS 属于 Host UI，不属于共享 Kotlin 代码。
- 不把共享 SDK 演进成自研跨平台 UI Framework。
- 目标是共享业务行为，而不是强制共享所有代码。
- Android 四页与 MiniApp 监控页宿主已实现；新增业务页面必须同步共享逻辑与测试，不引入自研跨平台 UI 框架。
- 两套客户端共同遵守 `../docs/api/openapi.yaml`，不得各自发明字段、告警等级或阈值单位。
- 目标能力包括实时仪表盘、温湿度/气体趋势、阈值设置和历史告警；Android 四页与 MiniApp 监控页已实现，趋势曲线已接入，部分平台验证待补。
- 负责 KMP 的成员/Agent 必须依照根规则使用 `multica` CLI，同步 source set、平台适配、API 契约消费、测试结果和阻塞。
- KMP 修改遵守根 Git/PR 流程，分支使用 `feat/kmp-...`、`fix/kmp-...` 等完整名称；PR 应列出受影响 source set 与 Android/MiniApp 分别验证的结果。

## Documentation, Comments, and Tests

- `commonMain` 的公开类型、函数、状态模型和平台接口必须使用 KDoc，说明跨平台语义、线程/协程约束、错误和单位。
- `expect/actual`、平台 Adapter 和生命周期桥接必须注释平台差异及选择原因；Compose UI 只为非显然的状态提升、重组或性能约束添加注释。
- API 模型、交互流程或 source set 边界变化必须同步更新 `README.md` 和相关架构/API 文档。
- `commonTest` 覆盖共享业务规则、解析、状态转换和错误映射；Android/MiniApp source set 分别提供平台单元或集成测试，不得只验证一个消费者。
- 可测试共享业务逻辑行覆盖率至少 80%，新增/修改核心逻辑目标至少 90%；阈值确认和告警状态等关键状态转换必须覆盖全部合法与非法分支。
- UI/平台集成使用 Compose/Android 测试、JS 测试或对应模拟器验证；无法自动化的 MiniApp 行为必须记录运行时、版本、步骤和结果。
- PR 必须报告 Gradle 测试任务、覆盖率、Android 构建和 MiniApp 适配验证结果。
