# 本地启动手册：硬件与 Go 服务端

本手册用于在同一局域网中启动真实 STM32/ESP8266、EMQX、`postgres-dev` 和 Go Backend。建议顺序是：**数据库 → Broker → Backend → 硬件**。

## 1. 前置条件

- Docker Desktop 或 OrbStack，并已存在 `postgres-dev` 容器。
- Go 1.25+、CMake 3.20+、GNU Make 和 Arm GNU Toolchain。
- ST-LINK 与 STM32F103C8T6 已通过 SWD 连接。
- ESP8266 与运行 EMQX 的电脑处于同一 Wi-Fi/手机热点。

在仓库根目录执行本文命令。不要将 Wi-Fi 密码或 PostgreSQL 密码写入受 Git 管理的文件。

## 2. 启动 PostgreSQL 和 EMQX

```sh
docker start postgres-dev
docker exec -i postgres-dev psql -U postgres -d postgres -v ON_ERROR_STOP=1 \
  < backend/database/bootstrap.sql
docker compose -f deploy/compose.yaml up -d emqx
docker compose -f deploy/compose.yaml ps
```

`bootstrap.sql` 可重复执行，只初始化 `lab` 数据库所需表、约束和索引。EMQX 对外使用 `1883`，Dashboard 使用 `18083`。

## 3. 启动 Go Backend

先确认 `postgres-dev` 的现有密码，再创建本机配置：

```sh
cd backend
cp .env.example .env.local
# 编辑 .env.local，将 DATABASE_URL 中的 CHANGE_ME 改为 postgres-dev 密码
go run .
```

Backend 启动时直接读取 `.env.local`，不需要再 `export` 每个字段。该文件已被
Git 忽略，不得提交数据库或 Broker 密码。

`AUTH_MODE=none` 只用于受信任的本地联调网络。成功时日志会出现 `connected to postgres and applied migrations`、`mqtt connected` 和 `http server listening`。

在另一个终端验证：

```sh
curl http://localhost:8080/healthz
curl http://localhost:8080/api/v1/devices/MCU001/status
curl http://localhost:8080/api/v1/devices/MCU001/telemetry/latest
```

省略 `DATABASE_URL` 时将使用内存存储，进程退出后数据丢失，不算完整验收。

## 4. 配置硬件网络

```sh
cp hardware/Esp8266/esp8266_config.example.h \
   hardware/Esp8266/esp8266_config.local.h
```

编辑被 Git 忽略的 `esp8266_config.local.h`：

```c
#define WIFI_SSID       "your-hotspot-name"
#define WIFI_PASSWORD   "your-hotspot-password"
#define SERVER_IP       "192.168.x.x"
#define SERVER_PORT     "1883"
#define DEVICE_ID       "MCU001"
```

`SERVER_IP` 必须是运行 EMQX 的 Mac/PC 在当前 Wi-Fi 或手机热点中的局域网 IP，**不能**填 `localhost` 或 `127.0.0.1`。macOS 可查找 Wi-Fi 接口后查询 IPv4：

```sh
networksetup -listallhardwareports
ipconfig getifaddr en0
```

如 Wi-Fi 对应的不是 `en0`，请替换为实际接口名。

## 5. 编译、烧录并运行固件

```sh
cd hardware
cmake --preset debug
cmake --build --preset debug
```

**先确认 hex 来自当前源码。** `build/debug` 的 CMake 缓存可能在工程迁移前生成（缓存内的源目录指向旧路径），此时 `cmake --preset debug` 会直接报错，而目录里残留的旧 hex 也不包含最近的功能改动。报错时用 `--fresh` 重新配置：

```sh
cmake --preset debug --fresh
cmake --build --preset debug
```

烧录前用这一条命令核对产物确实是当前固件 —— 有输出才说明构建里包含了 MQTT 遥测路径：

```sh
strings hardware/build/debug/STM32_Project1.bin | grep -c 'device/telemetry'
```

使用 STM32CubeProgrammer 选择 `hardware/build/debug/STM32_Project1.hex`，通过 ST-LINK/SWD 烧录、校验并复位。如已安装 OpenOCD，也可在 `hardware` 目录执行：

```sh
openocd -f interface/stlink.cfg -f target/stm32f1x.cfg \
  -c 'program build/debug/STM32_Project1.elf verify reset exit'
```

上电后预期：OLED 轮播数据；完成 Wi-Fi、MQTT CONNACK 和 SUBACK 后显示联网；设备按当前固件节拍约每 1 秒向 `device/telemetry` 发布 QoS 1 JSON（契约周期为 5 秒，该偏差记录在 `docs/device-protocol.md` §5.2）。气体超限或气体突增时，PA8/TIM1_CH1 以 2 kHz PWM 间歇发声（200 ms 响、800 ms 停）；其他告警只保持 LED、OLED 和遥测状态。

## 6. 联调检查与停止

```sh
docker exec lab-monitoring-emqx-1 emqx ctl clients list
docker exec postgres-dev psql -U postgres -d lab \
  -c "SELECT device_id, boot_id, sequence, received_at FROM telemetry ORDER BY received_at DESC LIMIT 5;"
```

正常时 EMQX 同时存在 `MCU001` 和 `lab-backend`，REST 返回 `connectivity=online`，PostgreSQL 的遥测序号持续增加。

停止 Backend 可按 `Ctrl-C`。停止本项目 EMQX：

```sh
docker compose -f deploy/compose.yaml down
```

该命令不会删除或停止独立的 `postgres-dev`。

## 7. 当前已知限制

- 2026-09-23 的 v2 固件已重新实测遥测上行、阈值写入与设备 ACK：气体阈值 30→80→30 时，`gas_high`/`localAlarm` 随之消失并恢复，命令均为 `applied`，版本升至 10；SWD 读到 PA8/TIM1 通道间歇使能，现场确认蜂鸣器正常发声。人工重新供电/Reset 后新 `bootId` 上报、设备恢复在线，Flash 双槽仍保留 version 10、40/80/30 阈值；Broker 完全停启后同值重发得到 version 11 `applied`/confirmed，当前阈值仍为 40/80/30。远程静音已从 v2 删除；受控写入中断电、拔掉热点后的恢复仍待验收。
- DHT11 已在实测板持续读数通过；若第 3 页后续出现 `Sensor: F<code>`，按 `hardware/README.md` 的状态码表检查 PA5、上拉电阻、3.3 V/GND 和传感器型号。
- 提交版默认关闭上电蜂鸣器自检。蜂鸣器不响时先确认 OLED 报警原因包含 `G` 或 `g`；只有气体超限/突增才会让 PA8/TIM1_CH1 间歇输出。需要隔离检查输出链路时，可临时把 `HARDWARE_SELFTEST_ON_BOOT` 置 1，验收后必须恢复为 0。
- MQ135 ppm 尚未现场标定，验收时应同时观察原始/滤波 ADC。
