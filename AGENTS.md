# Repository Agent Rules

1. 本项目是团队软硬件综合实训项目，不得擅自改变既定技术路线。
2. 优先采用能够完成当前任务的最小改动。
3. 未经需求确认，不得进行大规模重构。
4. 不得为了所谓“最佳实践”引入当前阶段不需要的框架、目录、抽象或依赖。
5. `hardware`、`backend`、`client-kmp` 与 `client-wx-native` 必须保持清晰边界。
6. 跨一级目录修改前必须说明原因、影响和涉及的事实源。
7. API、设备协议与 Domain Model 是跨模块事实源，当前为 `docs/api/openapi.yaml` 与 `docs/device-protocol.md`（均为 v2.0.0，远程静音已删除）；修改必须走各自变更流程并同步受影响模块，不得单方面扩展或假定契约。
8. `hardware` 现有代码默认视为高复用资产，优先保留和验证。
9. `client-kmp` 与 `client-wx-native` 必须保持独立，不得互相复制内部实现形成隐式耦合。
10. 当前已进入实现与验收阶段：PostgreSQL 持久化、MQTT 接入、WebSocket 推送与两套客户端业务页面均已实现；新增能力必须带文档与测试，不得回退到占位实现。
11. 项目主题为智慧机房/实验室微环境动环监控与早期火情预警，目标链路为 Hardware → ESP8266/MQTT → EMQX → Go Backend → Client。
12. 必须区分仓库现状与目标态：现有硬件使用 STM32F10x Standard Peripheral Library 与 DHT11，设备端已用 MCU 侧 MQTT 3.1.1 编解码经 ESP8266 TCP 透传接入 EMQX，远程阈值持久化已实机验收（2026-09-22）；HAL 迁移不是当前目标，旧 TCP 文本帧仅为遗留代码。

## Multica Collaboration

- 所有团队成员及其 Agent 必须使用 `multica` CLI 跟进自己负责的内容；不得只在本地文件、聊天或口头沟通中保留进度。
- 开始工作前，应在 Multica 中查找或创建对应 issue，将状态更新为 `in_progress`，并确认负责人和模块范围。
- 工作过程中，应通过 `multica issue comment add` 同步关键决策、接口变化、验证结果、风险和阻塞；发生阻塞时将 issue 状态设为 `blocked` 并说明解除条件。
- 提交评审前，将 commit、涉及文件、已运行验证及剩余风险同步到对应 issue，并将状态设为 `in_review`；验收后才可设为 `done`。
- Hardware、Backend、KMP、微信端之间的协议/API 变更必须在各自 issue 中交叉引用，不能只更新单个模块。
- 若 Multica 服务、认证或网络不可用，应在当前工作记录中明确说明，保留待同步摘要，并在连接恢复后补录；不得把同步失败视为任务已完成。
- 不在 AGENTS.md 中硬编码 workspace、project 或 issue ID；执行时通过 `multica workspace`、`multica project` 和 `multica issue` 查询当前上下文。

## Git and Pull Request Workflow

1. 首次基线建立后，所有成员从远程仓库重新克隆或更新本地 `main`；禁止长期在过期分支上开发。
2. 开工前根据实际内容新建分支，格式统一为 `<type>/<module>-<complete-description>`，使用小写英文和连字符。
3. `type` 只能从 `feat`、`fix`、`docs`、`test`、`refactor`、`chore` 中选择；`module` 使用 `hardware`、`backend`、`kmp`、`wx`、`repo` 或 `docs`。
4. 分支名必须完整表达工作内容，例如 `feat/backend-telemetry-query`、`fix/hardware-dht11-timeout`、`docs/repo-mqtt-contract`；禁止 `dev`、`test1`、姓名或只有模块名的含糊命名。
5. 一个分支只处理一个可评审目标。开始、关键进展、阻塞和评审状态必须同步到对应 Multica issue。
6. 修改完成后先运行模块 README 要求的格式化、静态检查和测试，再提交语义清楚的 commit；不得提交密码、本机配置、缓存或生成物。
7. 将分支推送到远程并创建 Pull Request。PR 必须说明关联 Multica issue、背景、变更范围、接口/协议影响、验证证据、风险和回退方式。
8. 跨模块 API/协议变更必须请求受影响模块负责人审核。不得未经审核直接合并到 `main`。
9. 审核意见处理完成、必需检查通过且获得批准后方可合并；合并后删除远程功能分支，并在 Multica issue 中同步结果和最终 commit/PR。
10. 紧急修复同样使用 `fix/...` 分支和 PR，不以紧急为由跳过审计；确需特殊处理时必须在 PR 和 Multica 中记录原因。

## Protected Main Branch

- 严禁任何成员或 Agent 直接在 `main` 上编辑、提交或推送；管理员也不例外。
- 开始任何修改前必须确认当前不在 `main`，并从最新 `origin/main` 创建符合命名规范的分支。
- `main` 只接受经过 Pull Request、至少一名其他成员批准、全部必需 CI 检查通过且讨论已解决的合并。
- 禁止 force push、删除 `main`、绕过保护规则或通过管理员权限直接提交。
- 必需检查的稳定名称为：`Contracts`、`Backend`、`Hardware`、`KMP Android`、`WeChat Native`。修改这些 job 名称前必须同步更新 GitHub 分支保护规则。
- CI 配置、CODEOWNERS 和分支保护相关修改本身也必须通过独立分支与 PR。

## Documentation, Comments, and Test Quality

- 所有 Agent 产出的代码必须同时具备与对应技术栈相符的文档、必要注释和自动化测试；缺少其中任一项的功能不得视为完成。
- 文档必须说明行为、边界、输入输出、错误语义、配置方式和验证方法。公开 API、设备协议、引脚/时序或跨模块模型变化必须同步更新事实源文档。
- 注释用于解释设计意图、约束、时序、电气/平台差异和非显然决策，不得逐行翻译代码或用注释掩盖难以理解的实现。
- 每个 bug fix 必须包含能够复现问题并防止回归的测试；每个新功能至少覆盖正常路径、边界输入和失败路径。
- 单元测试负责纯逻辑和模块边界，集成测试负责真实序列化、路由、数据库/Broker、平台 API 或硬件交互边界。不能用大量 mock 替代关键集成链路。
- 可自动统计的核心业务代码行覆盖率不得低于 80%，新增或修改的可测试逻辑目标不得低于 90%；安全关键判断的重要分支必须全部覆盖。各模块可制定更严格要求。
- 覆盖率不得通过排除正常业务文件、空断言、重复测试或只执行代码不验证结果来“达标”。无法自动覆盖的硬件/平台行为必须提供可重复的 HIL、模拟器或人工验收记录。
- PR 必须报告测试命令、覆盖率结果、未覆盖风险和集成验证环境；覆盖率下降、测试被跳过或无法执行时必须阻止合并，除非评审者在 PR 与 Multica 中记录明确例外和补救计划。
