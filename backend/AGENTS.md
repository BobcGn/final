# Backend Agent Rules

- Go 是 Backend 的主体语言，遵循标准 Go 风格并保持可执行、可测试。
- 当前优先使用标准库；路由契约已冻结并全部实现，PostgreSQL（pgx）、自研 MQTT 客户端与 WebSocket 已按需引入——新增依赖仍须先有明确需求，不得为预想功能引入框架。
- 不在需求出现前创建 Controller、Handler、DTO、Repository、Service 或复杂分层。
- 后续 C 能力必须通过清晰、可记录的边界接入；不得提前加入 CGO。
- 业务代码不得直接依赖具体传感器、GPIO 或硬件驱动实现。
- Backend 是客户端与设备之间的系统边界；任何协议变化都必须与 `hardware` 显式协调和记录。
- 路由事实源为 `../docs/api/openapi.yaml`，设备主题与 Payload 事实源为 `../docs/device-protocol.md`；实现与文档变更必须同步。
- Backend 目标职责包括遥测入库、设备在线判定、复合预警、REST 查询、控制指令发布和 WebSocket 秒级推送。
- 复合预警必须同时考虑气体突增与温升速率，并记录触发证据；不得用单次采样直接产生复合火警。
- 全部路由 Handler 均为真实实现（2026-09-22），不存在 `501 not_implemented` 占位；未实现能力必须以显式错误码表达（见 `docs/api.md` §1.6），不得伪造数据库或 Broker 结果。
- 负责 Backend 的成员/Agent 必须依照根规则使用 `multica` CLI，同步路由/Schema 变更、MQTT 与数据库决策、测试结果和跨模块依赖。
- Backend 修改遵守根 Git/PR 流程，分支使用 `feat/backend-...`、`fix/backend-...` 等完整名称；API 或 MQTT Schema PR 必须同步契约文档并请求客户端/硬件负责人审核。

## Documentation, Comments, and Tests

- 导出的 Go 标识符必须有符合 GoDoc 规范的注释；注释以标识符开头并描述契约、并发安全性、单位、错误和副作用，而不是复述实现。
- 每条 HTTP 路由必须同时更新 `docs/api.md` 与根目录 `../docs/api/openapi.yaml`；MQTT Payload 变化还必须更新 `../docs/device-protocol.md`。
- Handler、校验、复合预警、设备在线判定和命令状态机必须有表驱动单元测试，覆盖正常、边界、非法、重复、乱序、超时与失败路径。
- HTTP 使用 `httptest` 做路由/序列化集成测试；MQTT 与数据库接入后必须使用可重复的 Broker/数据库测试环境验证消费、持久化、事务、重连和幂等。
- 必须执行 `go test -race -coverprofile=coverage.out ./...`。可测试 Backend 包总行覆盖率至少 80%，新增/修改核心逻辑目标至少 90%，复合预警和权限/控制关键分支必须全部覆盖。
- 不得为了覆盖率直接测试私有实现细节；优先通过公开行为、接口边界和稳定输出断言。
- PR 必须列出 `go fmt`、`go vet`、race test、覆盖率和集成测试结果。接口文档与实现不一致时禁止合并。
