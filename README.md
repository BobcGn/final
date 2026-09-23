# Project Overview

这是一个由 5 人团队完成的软硬件综合实训项目。系统最终由硬件、服务端和客户端三层组成：

```text
Hardware
   ↓
Backend
   ↓
Client
```

客户端保留两套相互独立的实现方案：

- A. KMP Client：以 Kotlin Multiplatform 共享可复用的客户端业务逻辑。
- B. WeChat Native Client：使用微信原生小程序技术实现，作为对照基线。

后续工程对照应基于相同需求、相同 Backend API、相同硬件数据源、尽可能相同的 UI/UX 和相同验收场景。两套客户端的内部实现保持独立，避免为了对齐目录或代码而引入隐式耦合。

## Repository Structure

```text
hardware/          STM32、传感器、显示、报警及设备通信实现
backend/           Go 服务端（MQTT、PostgreSQL、REST 与 WebSocket）
client-kmp/        Kotlin Multiplatform 客户端方案
client-wx-native/  微信原生小程序基线方案
docs/              协议契约、实施方案、联调与启动手册
deploy/            本地集成编排（EMQX 与 ACL）
```

## Current Phase

当前阶段：**首轮软硬件联调与验收**。

项目主题为“智慧机房/实验室微环境动环监控与早期火情预警系统”。目标链路为：

```text
Sensors → STM32 local safety loop → ESP8266/MQTT → EMQX
        → Go Backend → REST/WebSocket → WeChat/KMP Clients
```

当前仓库已完成真实硬件 MQTT 遥测上行、EMQX、Go Backend 与 PostgreSQL 落库的首轮联调。硬件仍使用 STM32F10x Standard Peripheral Library（非 HAL）。2026-09-22 起，真实设备的远程命令/ACK 与断网自治（停 Broker 场景）已完成实机闭环验收，证据见 [hardware/README.md](hardware/README.md)「实机闭环验收记录」；DHT11 故障注入、MQ135 标定与真实拔电验收仍未完成，不得按已验收能力对待。

本地启动真实硬件、EMQX、`postgres-dev` 和 Go Backend 请按 [本地启动手册](docs/local-runbook.md) 执行。

设计事实源：

- [完整实现方案](docs/implementation-plan.md)
- [设备 MQTT 协议（冻结）](docs/device-protocol.md)
- [Backend OpenAPI（冻结）](docs/api/openapi.yaml)
- [Backend API 详细契约](backend/docs/api.md)
- [微信小程序界面与 API 对照表](backend/docs/wx-ui-api-mapping.md)

当前仍不进行未评审的 API/设备协议破坏性变更、客户端隐式耦合、CGO 集成或大规模重构。

## Collaboration Workflow

首次仓库基线建立后，所有变更采用分支和 Pull Request 协作：

```sh
git clone <repository-url>
cd final
git switch main
git pull --ff-only
git switch -c feat/backend-telemetry-query

# 修改后执行所属模块 README 中的检查
git add <files>
git commit -m "feat(backend): add telemetry query contract"
git push -u origin feat/backend-telemetry-query
```

随后创建 PR，填写关联 Multica issue、变更范围、接口/协议影响、验证结果、风险与回退方式。PR 经相关模块负责人审核、检查通过并批准后才能合并；合并后删除功能分支并在 Multica 中把最终 PR/commit 同步到 issue。

分支格式为 `<type>/<module>-<complete-description>`：

- 类型：`feat`、`fix`、`docs`、`test`、`refactor`、`chore`
- 模块：`hardware`、`backend`、`kmp`、`wx`、`repo`、`docs`
- 示例：`feat/hardware-mqtt-telemetry`、`fix/backend-offline-timeout`、`docs/repo-api-contract`

禁止直接在 `main` 开发或使用 `dev`、`test1`、姓名等含糊分支名。完整约束见 [AGENTS.md](AGENTS.md)。

## CI/CD

GitHub Actions 在所有指向 `main` 的 PR 及 `main` 合并结果上执行：

- `Contracts`：项目事实文件、OpenAPI YAML 和空白错误检查。
- `Backend`：Go 格式、`vet` 和 race-enabled tests。
- `Hardware`：Arm GNU Toolchain 交叉编译并上传 ELF/HEX/BIN/MAP。
- `KMP Android`：共享逻辑测试、Android Debug APK 构建及制品上传。
- `WeChat Native`：JavaScript 语法与 JSON 校验。

当前 CD 是“持续交付构建制品”而非自动部署：固件和 APK 在 Actions 中保留 14 天。尚未定义生产设备 OTA、应用商店或小程序发布目标，因此流水线不会擅自发布到真实环境。

`main` 是受保护分支，任何人（包括管理员）都不能直接推送、强推或删除。所有修改必须经 PR、至少一次其他成员批准、全部必需检查通过并解决讨论后合并。
