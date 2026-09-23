# Backend

该目录是项目的 Go 服务端边界，负责衔接硬件设备与两套客户端。

当前已实现完整链路：**MQTT 接入 → 校验与去重 → 持久化 → 复合火情预警 → REST 查询/控制 → WebSocket 推送**。事实源为 [`../docs/api/openapi.yaml`](../docs/api/openapi.yaml)（接口）与 [`../docs/device-protocol.md`](../docs/device-protocol.md)（设备协议）；面向开发者的详细说明见 [`docs/api.md`](docs/api.md)。三者由 `contract_test.go` 交叉校验。

## 运行

需要与真实硬件、EMQX 和 `postgres-dev` 一起启动时，优先使用根目录的 [本地启动手册](../docs/local-runbook.md)；本节保留 Backend 单模块运行方式。

```sh
cp .env.example .env.local
# Edit .env.local and replace CHANGE_ME with the postgres-dev password.
go run .
```

启动时直接读取 `backend/.env.local`，缺失时拒绝启动并提示从
`.env.example` 复制。`.env.example` 是已提交的完整模板；`.env.local` 包含本机
密码与地址，已被 Git 忽略。默认监听 `:8080`。

最小可用（无数据库、无 Broker，仅提供 HTTP 与健康检查）：

```sh
go run .
curl http://localhost:8080/healthz
```

接入 PostgreSQL 与 EMQX 时编辑 `.env.local`：

```dotenv
DATABASE_URL=postgres://postgres:<existing-password>@localhost:5432/lab?sslmode=disable
MQTT_BROKER_URL=localhost:1883
MQTT_USERNAME=backend
MQTT_PASSWORD=backend-secret
AUTH_MODE=none
```

完整字段表见 [`docs/api.md`](docs/api.md) §13。**未知、重复或非法字段会导致
启动失败，不会静默回退。** 部署或测试可仅用
`BACKEND_CONFIG_FILE=/path/to/file go run .` 选择其他文件；它不覆盖文件内字段。

启动日志会对以下情况记录 WARN，因为它们在本地可用但不适合无人值守部署：`AUTH_MODE=none`、使用内存存储、未配置 Broker。

## 在现有 postgres-dev 容器初始化数据库

`database/bootstrap.sql` 包含专用 `lab` 数据库的创建语句，以及与服务内嵌迁移完全一致的完整建表、约束和索引语句。脚本使用 `psql` 的 `\gexec` 与 `\connect`，可重复执行；它不会修改 `postgres-dev` 中其他数据库。执行前请确认容器已启动，并备份需要保留的数据：

```sh
docker start postgres-dev
docker exec -i postgres-dev psql -U postgres -d postgres -v ON_ERROR_STOP=1 < backend/database/bootstrap.sql
docker exec postgres-dev psql -U postgres -d lab -c '\dt'
```

在仓库根目录运行上述命令。`DATABASE_URL` 指向 `localhost:5432/lab`，密码使用已有 `postgres-dev` 的凭据，不写入仓库。服务启动时仍会执行内嵌的幂等迁移；`internal/store/database_script_test.go` 防止人工脚本与迁移内容漂移。

`pgtmp` 是之前测试使用的独立容器，不是运行依赖。当前已删除该容器；原测试库为空，不能依赖其匿名数据卷作为备份。正式数据请使用 `postgres-dev` 的现有持久化目录并另行定期备份。

## 质量检查

```sh
go fmt ./...
go vet ./...
go test -race -coverprofile=coverage.out ./...
go tool cover -func=coverage.out
```

PostgreSQL 集成套件由 `TEST_DATABASE_URL` 启用，指向一个**可被销毁**的数据库（套件会创建并删除自己的 schema）：

```sh
TEST_DATABASE_URL='postgres://postgres:password@localhost:5432/lab_test?sslmode=disable' \
  go test -race ./internal/store/
```

未设置该变量时套件跳过，因此 CI 必须提供它，否则数据库路径的实际覆盖为零。

内置 MQTT 测试 Broker 的生命周期回归用例可单独运行：`go test -race -count=20 ./internal/mqtt/mqtttest`。它覆盖关闭时并发接入且客户端未发送 CONNECT 的情况，防止测试清理阶段无限等待。

会话保活与 QoS 1 确认的可靠性用例在 `internal/mqtt/keepalive_test.go`。它们针对 SHIXUN-25 的两条硬性要求：会话在多个保活周期内稳定、以及**没有收到 PUBACK 的发布绝不能被记成已发布**。测试 Broker 因此提供了 `Pings()`、`WithholdPubAcks()`、`StopAnswering()`、`AnswerWithPubAck()` 与 `TakeOver` 五个开关，分别对应「ping 是否真的发出」「Broker 收下消息但不确认」「Broker 彻底沉默」「旧会话的确认」与「同 clientId 顶号」。用例全部跑在毫秒级保活上（`KeepAlive=1s`、`PingAfter` 可下调），不靠 `sleep 30 秒` 硬堆。

CI 要求可测试代码总行覆盖率 ≥ 80%，新增/修改的核心逻辑目标 ≥ 90%。覆盖率文件 `coverage.out` 是生成物，不提交。

## 结构与边界

```text
backend/
├── main.go                    读取配置、安装信号处理、调用组合根
├── contract_test.go           与 docs/api/openapi.yaml、docs/device-protocol.md 交叉校验
└── internal/
    ├── app/                   组合根：装配全部依赖并运行
    ├── config/                dotenv 配置加载与校验
    ├── domain/                纯领域模型与范围校验（无 I/O 依赖）
    ├── protocol/              MQTT 报文编解码（严格模式）
    ├── mqtt/                  标准库 MQTT 3.1.1 子集客户端
    │   └── mqtttest/          进程内 MQTT Broker，供集成测试使用
    ├── store/                 持久化边界：Store 接口 + 内存实现 + PostgreSQL 实现 + 迁移
    ├── alert/                 复合火情预警状态机（纯逻辑）
    ├── liveness/              基于遥测到达时间的在线判定
    ├── events/                实时事件信封与类型
    ├── ingest/                遥测接入编排：校验、去重、乱序、告警应用
    ├── command/               控制命令生命周期与 ACK 状态机
    └── api/                   HTTP 路由、鉴权、分页与 WebSocket 集线器
```

边界规则：

- 业务代码**不得**直接依赖具体传感器、GPIO 或硬件驱动实现。
- `domain`、`protocol`、`alert`、`liveness` 是纯逻辑包，不导入数据库、Broker 或 HTTP。
- 数据库与 Broker 通过接口接入（`store.Store`、`command.Publisher`），`internal/app` 之外没有包 import 具体驱动。
- Web 层使用标准库 `net/http` 的 method-pattern 路由，未引入 Web Framework。
- 后续 C 能力必须经清晰可记录的边界接入；当前未加入 CGO。

### 依赖选择

| 依赖 | 用途 | 理由 |
| --- | --- | --- |
| `github.com/jackc/pgx/v5` | PostgreSQL 驱动 | `docs/implementation-plan.md` §6.1 的首选数据库 |
| `github.com/gorilla/websocket` | WebSocket | RFC 6455 实现成熟，握手与关闭语义经过验证 |
| `gopkg.in/yaml.v3` | 契约测试 | 测试中使用真实 YAML 解析器读取 OpenAPI，避免自制扫描器漏判 |
| 自研 `internal/mqtt` | MQTT 客户端 | 见下 |

**为什么自研 MQTT 客户端**：本项目只需要 CONNECT、SUBSCRIBE、PUBLISH（QoS 0/1）、PUBACK、PING 与 DISCONNECT。自研子集让帧格式可审计、依赖面最小，并且不需要为一个后续可能替换的传输引入完整客户端库。超出该子集的能力（retain、will、QoS 2、MQTT 5 属性）会被**显式拒绝**而不是半实现。若后续需要 TLS 客户端证书、自动重连策略或 MQTT 5，应重新评估引入成熟客户端。

## 路由

| Method | Path | Purpose |
| --- | --- | --- |
| GET | `/healthz` | 进程健康检查（不需要鉴权） |
| GET | `/api/v1/devices/{deviceId}/status` | 设备在线状态与告警状态 |
| GET | `/api/v1/devices/{deviceId}/telemetry/latest` | 最新遥测 |
| GET | `/api/v1/devices/{deviceId}/telemetry` | 历史遥测 |
| GET | `/api/v1/devices/{deviceId}/alerts` | 历史告警 |
| GET/PUT | `/api/v1/devices/{deviceId}/thresholds` | 查询或修改阈值 |
| GET | `/api/v1/devices/{deviceId}/commands/{requestId}` | 查询命令状态 |
| GET | `/ws/v1/devices/{deviceId}/telemetry` | WebSocket 实时流 |

## 会话保活、断线检测与重连

MQTT 会话的健康由 `internal/mqtt` 维护，三条规则决定了控制链路是否可靠。

### 保活：空闲驱动，且不卡在边界上

MQTT 3.1.1 §3.1.2.10 要求同一方向上任意两个控制包之间**不超过** 1.5 倍 keep-alive。因此客户端**不**在
keep-alive 整点上发 PINGREQ：一次调度抖动就会让间隙越过 1.5 倍，Broker 随即关闭连接。实际节奏是
**空闲驱动**的：

- 任何本端发出的报文都是保活报文，因此应用层的 PUBLISH 会把下一次 PINGREQ 推后，不会在其后紧跟一个多余的 ping；
- 只有当本端空闲超过 `KeepAlive / 2`（`MQTT_KEEPALIVE_SECONDS` 默认 30 秒 → 15 秒）才发 PINGREQ，等于给链路留下两倍于协议要求的余量；
- 入站流量不计作保活：客户端只需负责自己这一侧的发送。
- `KeepAlive` 不得小于 1 秒——CONNECT 字段是整秒，亚秒值会被协商成 0（即完全不保活），而客户端仍会按分数周期 ping，两者就对不上了。

PINGRESP 到达只作为对端仍在读的旁证；缺失的判断交给读超时（`2 × KeepAlive`）。连续两次周期没有入站即视为对端不在。

### 断线检测

读超时（`2 × KeepAlive`）与 TCP EOF 都会终止一次会话，二者都会把所有在途的 QoS 1 等待者以失败结束（见下），
随后按退避重连。一次会话建立前的失败（拨号失败、CONNACK 被拒、SUBACK 被拒）与一次**已建立后**掉线的处理不同：
后者立即把退避复位到 `ReconnectMin`，前者才会累积到 `ReconnectMax`。长期故障不会让后来的小抖动也付出 30 秒的代价。

### 重连与订阅

`Connected` 表示 **CONNECT 与 SUBSCRIBE 都已完成**，不是「CONNECT 已确认」。因此 Broker 重启后客户端会重新订阅
`device/telemetry` 与 `device/command-ack`，而不会在订阅仍处于途中时就报告就绪。同一 clientId 被另一连接顶掉
（部署重叠、手工起第二个进程）会被当作一次会话丢失处理并自动重连。

### QoS 1 发布的四种结局

控制命令是 QoS 1 的，必须区分下面四种，**任何一种失败都不能算作已发布**：

| 结局 | 含义 |
| --- | --- |
| `nil` | Broker 对本连接、本 packet id 回了 PUBACK。消息自此由 Broker 负责。 |
| `ErrPublishTimeout` | 约定时间内没有任何确认。消息**可能**已被接收。 |
| `ErrConnectionLost` | 套接字在收到确认前结束。消息**可能**已被接收。 |
| `ctx.Err()` | 调用方停止等待。 |

等待者按 **(packet id, 连接 epoch)** 两个键登记：一个 epoch 上的 PUBACK 绝不会去确认另一个 epoch 上的发布。
重连时所有旧 epoch 的等待者一律以 `ErrConnectionLost` 结束，绝不静默记为成功。

## 运维要点

- **控制命令的 202 只表示 Broker 已确认接收这次 publish，不代表设备已执行。** 只有 `device/command-ack`
  才会把命令推进到 `applied`；客户端必须等待确认或超时结果。
- **未配置 Broker 时控制路由返回 `503 broker_unavailable`**，命令被记录为 `publish_failed` 而不是假装已下发。
- **过期命令不会被重发。** 设备重连后，`expiresAt` 已过的命令只会被标记为 `timed_out`。
- **`/healthz` 不检查依赖。** 数据库或 Broker 故障时它仍返回 200；需要 readiness 语义时应新增独立路由。
- **设备白名单**：首期主题固定，主题本身无法表达"哪个设备允许发布"，因此可用 `DEVICE_ALLOWLIST` 作为应用层限制，直到按设备凭据与 ACL 就位。

## Collaboration Workflow

```sh
git clone <repository-url>
cd final
git switch main
git pull --ff-only
git switch -c feat/backend-telemetry-query

cd backend
go fmt ./...
go vet ./...
go test -race -coverprofile=coverage.out ./...
go tool cover -func=coverage.out
cd ..

git add backend docs/api/openapi.yaml docs/device-protocol.md
git commit -m "feat(backend): add telemetry query"
git push -u origin feat/backend-telemetry-query
```

分支必须采用 `<type>/backend-<complete-description>`，例如 `fix/backend-offline-timeout`。PR 必须关联 Multica issue 并列出路由、Schema、迁移影响和测试结果；API/MQTT 契约变化需要 Hardware 与客户端负责人审核。所有检查通过并获批准后才能合并。
