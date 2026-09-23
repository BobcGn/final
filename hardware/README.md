# STM32F103C8 环境监测、阈值报警与 Wi-Fi 通信

> 需要与本机 EMQX、`postgres-dev` 和 Go Backend 完成真实链路启动时，请按 [本地启动手册](../docs/local-runbook.md) 执行。本 README 保留固件专属的接线、构建、烧录和测试说明。

本项目是一个基于 STM32F103C8T6 的裸机环境监测程序：通过 DHT11 采集温湿度，通过 ADC 采集 MQ135 的模拟输出，在 128×64 OLED 上轮播显示读数、气体详情、报警状态与网络状态。所有本地告警都保留 LED、OLED 与遥测状态；当前蜂鸣器只对气体超限/气体突增以间歇方式发声。ESP8266/ESP8285 通过 Wi-Fi 建立 TCP 透传连接，由 STM32 编码 MQTT 3.1.1 遥测帧上传至 EMQX。

本地判断逻辑（滤波、阈值、突增、静音优先级、页面内容）位于不依赖硬件的 `core/` 模块中，可在主机上完整测试；见「本地逻辑与主机测试」。

- 目标 MCU：STM32F103C8T6（Cortex-M3）
- 存储器配置：64 KiB Flash、20 KiB RAM
- 库：STM32F10x Standard Peripheral Library
- 构建系统：CMake 3.20+ + Arm GNU Toolchain
- 下载/调试接口：ST-LINK（SWD）

## 功能说明

主循环以 **100 ms** 为一个节拍，各任务按节拍分频执行：

| 任务 | 周期 | 说明 |
| --- | --- | --- |
| MQ135 ADC 采样 | 100 ms | 原始值进入 10 点滑动窗口 |
| DHT11 温湿度 | 1 s | 器件要求的最小采样间隔 |
| 本地安全判断 | 100 ms | 不等待网络；断网时照常执行 |
| OLED 刷新 | 500 ms | 每 2 s 轮播下一页 |
| 遥测上报（MQTT QoS 1） | 1 s | `device/telemetry` JSON |

**本地报警**在每次判断时综合以下条件，任一条成立即点亮 `PA4`：

1. 温度达到 `TEMP_HIGH_THRESHOLD_C`；
2. 湿度达到 `HUMIDITY_HIGH_THRESHOLD_RH`；
3. 滤波后气体估算值达到 `GAS_HIGH_THRESHOLD_PPM`；
4. 温度突增：60 秒窗口内累计上升达到 `TEMP_RISE_THRESHOLD_C`；
5. 气体突增：60 秒窗口内 `gasAdcFiltered` 增量达到 `GAS_RISE_THRESHOLD_ADC`（ADC 码）；
6. DHT11 读取失败：上报 `sensor_fault`，并保持最后一次有效读数。

阈值全部集中在 `STM32_Project1/User/app_config.h`，默认为 30 ℃、80 %RH、20 ppm、3 ℃ 突增、150 ADC 突增。
`hardware/core/env_monitor.h` 直接从该头文件取默认值，因此**只需要改一个地方**，设备行为与后台上报的阈值版本 1 会同步变化。

**蜂鸣器语义**：只有 `gas_high` 或 `rapid_gas_rise` 会驱动 PA8，以 200 ms 发声 + 800 ms 停止的**名义**周期间歇告警（实现是「每 10 节拍响 2 节拍」的比例，实际时长随主循环迭代耗时伸缩，见已知限制第 14 项）；温度、湿度和传感器故障只保留 LED/OLED/遥测告警。远程静音只抑制蜂鸣器，不改变其他告警状态。

**滤波**：`gasAdcRaw` 与 `gasAdcFiltered` 同时保留。滤波使用固定 10 点环形窗口的算术平均，窗口未填满时按已采样本数取平均，因此上电后第一秒即可用，而不是等窗口填满才输出。气体浓度估算值由**滤波后**的 ADC 换算，保证同一帧里的 `gasAdcFiltered` 与 `gasPpm` 描述同一个采样。

## 硬件与接线

### 所需硬件

- STM32F103C8T6 开发板（例如 Blue Pill）
- 0.96 英寸 128×64 I²C OLED（常见 SSD1306，7 位地址 `0x3C`）
- DHT11 温湿度传感器
- MQ135 气体传感器模块（模拟输出）
- LED 和有源蜂鸣器（如开发板未集成）
- ST-LINK/V2 或兼容的 SWD 调试器
- USB 数据线和若干杜邦线

### 模块接线

| 模块 | 模块引脚 | STM32 引脚 | 说明 |
| --- | --- | --- | --- |
| OLED | VCC | 3.3V | 请以实际模块允许电压为准 |
| OLED | GND | GND | 共地 |
| OLED | SCL | PB8 | 软件模拟 I²C 时钟 |
| OLED | SDA | PB9 | 软件模拟 I²C 数据 |
| DHT11 | VCC | 3.3V | 供电 |
| DHT11 | GND | GND | 共地 |
| DHT11 | DATA | PA5 | 数字温湿度数据 |
| MQ135 | VCC | 按模块要求 | 常见加热器模块需 5V，以实物为准 |
| MQ135 | GND | GND | 必须与 STM32 共地 |
| MQ135 | AO | PA1 | ADC 输入必须限制在 0～3.3V |
| LED | 控制端 | PA4 | 高电平报警 |
| 有源/无源蜂鸣器 | 控制端 | PA8 / TIM1_CH1 | 2 kHz PWM 报警，频率由实际定时器时钟推导 |
| ESP TX | PA10 / USART1 RX | ESP 发送、STM32 接收 |
| ESP RX | PA9 / USART1 TX | STM32 发送、ESP 接收 |
| ESP GND | GND | 必须共地 |
| ESP VCC/EN | 稳定 3.3V | 不要接 5V，电源需要留有充足的瞬时电流余量 |

## Wi-Fi 与 MQTT Broker 配置

Wi-Fi 名称、密码、MQTT Broker 地址、端口和设备编号使用本机配置。设备连接手机热点时，`SERVER_IP` 必须是 Mac 在该热点的局域网 IP，不能是 `localhost`/`127.0.0.1`；默认 MQTT 端口为 `1883`。

```sh
cp Esp8266/esp8266_config.example.h Esp8266/esp8266_config.local.h
```

填写 `esp8266_config.local.h` 后重新编译。该文件已被 Git 忽略，禁止提交真实 Wi-Fi 或服务器凭据；未创建本机配置时，`esp8266.h` 的安全占位值只保证工程可编译，不能连接真实网络。

EMQX 应在配置的 IP/1883 监听。STM32 连接后使用 `DEVICE_ID` 作为 MQTT clientId，订阅 `device/control`，并周期向 `device/telemetry` 发布 JSON。完整启动顺序见 [本地启动手册](../docs/local-runbook.md)。

OLED 轮播四页，每 2 秒切换，每 500 ms 刷新：

```text
Temp: 25C          ADC raw:1350      Alarm: ---         Linked:Lab
Humi: 050%         ADC flt:1328      Buzzer: off        msg:<第一条消息...>
Gas: 012ppm        Est: 012ppm*      Sensor: ok         <第二条消息...>
Thr: v1            Level: OK*        State: clear       <第三条消息...>
```

- 第 1 页：温度、湿度、气体估算值、当前生效的阈值版本。
- 第 2 页：原始与滤波后的 ADC 值、估算浓度、气体安全等级。`*` 表示估算来自**未标定**曲线，此时应以等级而不是精确 ppm 作为判断依据。
- 第 3 页：报警原因字母（`T` 温度、`H` 湿度、`G` 气体、`t` 温度突增、`g` 气体突增、`F` 传感器故障）、蜂鸣器实际状态、传感器状态、总状态。静音时显示 `State: MUTED`，**同时仍然显示报警原因**，不会把告警显示成正常。传感器故障时该行带上 DHT11 驱动的状态码（例如 `Sensor: F5`），用于区分"器件无应答"和"器件有应答但数据无法解析"；状态码含义见下表。
- 第 4 页：Wi-Fi 状态与服务器下发的消息（沿用原来的 `msg:` 行为，最多 44 个 ASCII 字符，超出部分省略）。

Wi-Fi 初始化期间第 4 页顶部显示 `Linking:<SSID>`，完成后切换为 `Linked:<SSID>` 或 `Link fail`。
页面渲染逻辑位于 `hardware/core/display_model.c`，可在主机上测试，见下文「本地逻辑与主机测试」。

### DHT11 故障状态码

`DHT11_Read_Data` 的返回值会显示在第 3 页的 `Sensor: F<code>` 上。状态码区分了不同的现场故障，接线问题与位时序问题的排查方向完全不同：

| 状态码 | 含义 | 常见原因 |
| --- | --- | --- |
| 0 | 读取成功 | — |
| 1 | 释放总线后器件始终没有拉低 | 器件未供电、DATA 未接到 PA5、接线松动、器件损坏 |
| 2 | 应答（80 us 低 + 80 us 高）没有在超时内结束 | 同上，或总线上有其他器件在驱动 |
| 3 | 读位时数据线一直是高电平（帧提前结束） | 轮询太慢导致整位漏读、供电瞬时跌落、器件中途停止发送 |
| 4 | 读位时数据线一直没有回到高电平 | 同上，或帧在低电平处中断 |
| 5 | 校验和不匹配 | 位宽判定错误：采样点偏出 28~70 us 窗口、上拉过弱、线过长 |
| 6 | 数值超出 DHT11 合理范围（湿度 > 100 %RH 或温度 > 60 °C） | 接的是 DHT22/AM2302 等不同器件，或位时序整体错位 |
| 7 | 调用时输出指针为空 | 固件内部错误，不会出现在正常路径上 |

`F1`/`F2` 指向接线与供电；`F3`/`F4` 说明器件有应答但位流没有跟完，通常是软件侧时序；`F5`/`F6` 说明整帧读到了但内容不可信，优先怀疑上拉、线长和器件型号。

#### 现场实测记录（2026-09-21）

实测板一度稳定返回 `F3`。用 DWT 周期计数器把数据线的每次跳变连同时间戳记录下来（见 `dht11.h` 的 `DHT11_TRACE_ENABLE`）后确认：器件本身完好，发出的是完整且校验和正确的帧（40 %RH、30 °C，5 字节校验和 0x47 吻合）。

根因在软件：`DHT11_WaitWhileLevel` 每轮循环都调用一次 `delay_us(1)`，在 8 MHz、`-O0` 的构建下单轮开销约十几微秒，已经超过一个 0 位高电平（26 us）的一半宽度，于是整位漏读、逐步错位，最终跑出帧外并停在空闲高电平上超时 —— 正好是状态码 3。

修复方式是把「固定延时 40 us 后采样」改成「测量高电平宽度后按门限判位」（26~28 us 为 0、70 us 为 1，门限 48 us，两侧各留 20 us 余量），并把等待循环改成不含函数调用的紧循环 + 周期计数器超时。宽度判定的容差是固定采样点的二十余倍，因此不再依赖主频与优化级别。

### ST-LINK SWD 接线

| ST-LINK | STM32 |
| --- | --- |
| SWDIO | PA13 |
| SWCLK | PA14 |
| GND | GND |
| 3.3V / VTref | 3.3V |
| NRST（建议） | NRST |

> 接线和通电前请先核对开发板与仿真器标识。不要将 ST-LINK 的 5V 直接到 3.3V 引脚。

## 通用环境要求

下列命令需要能在终端中直接执行，即其 `bin` 目录已加入 `PATH`：

```text
cmake                 3.20 或更高版本
arm-none-eabi-gcc     Arm GNU bare-metal 交叉编译器
arm-none-eabi-objcopy 随 Arm GNU Toolchain 提供
arm-none-eabi-size    随 Arm GNU Toolchain 提供
```

macOS 上的现有 CMake 预设还需要 GNU Make；Windows PowerShell 流程则使用 Ninja。

安装完成后可检查：

```sh
cmake --version
arm-none-eabi-gcc --version
arm-none-eabi-objcopy --version
```

如果其中任意命令提示“找不到”，请先修正安装或 `PATH`，再配置项目。

## macOS 环境与运行

### 1. 安装构建工具

先安装 Xcode Command Line Tools：

```sh
xcode-select --install
```

如果已安装 [Homebrew](https://brew.sh/)，可用它安装 CMake 和 Arm GNU Toolchain：

```sh
brew install cmake arm-none-eabi-gcc
```

也可分别使用 [CMake 官方安装包](https://cmake.org/download/) 和 [Arm GNU Toolchain 官方发行版](https://developer.arm.com/Tools%20and%20Software/GNU%20Toolchain)。Apple Silicon Mac 需要选择与当前主机架构兼容的工具链；如使用 x86_64 版，则还需要 Rosetta 2。

### 2. 获取项目并进入根目录

```sh
git clone <项目仓库地址>
cd <项目目录>
```

如果项目由压缩包解压得到，直接在终端中 `cd` 到本 README 所在的根目录即可。

### 3. 编译固件

（以下命令在 `hardware/` 目录执行。）

Debug 构建：

```sh
cmake --preset debug
cmake --build --preset debug
```

Release 构建：

```sh
cmake --preset release
cmake --build --preset release
```

### 4. 烧录并运行

安装跨平台的 [STM32CubeProgrammer](https://www.st.com/en/development-tools/stm32cubeprog.html)，然后：

1. 按上表连接 ST-LINK 与开发板，再将 ST-LINK 接入 Mac。
2. 打开 STM32CubeProgrammer，选择 `ST-LINK` 和 `SWD`，点击 **Connect**。
3. 选择 `build/debug/STM32_Project1.hex`（或 Release 目录中的文件）。
4. 启用烧录后校验，执行下载，然后复位开发板。

如果 `STM32_Programmer_CLI` 已加入 `PATH`，也可使用：

```sh
STM32_Programmer_CLI -c port=SWD -w build/debug/STM32_Project1.hex -v -rst
```

## Windows 环境与运行

### 1. 安装构建工具

安装以下软件：

1. [CMake 3.20+](https://cmake.org/download/)：安装时选择将 CMake 加入 `PATH`。
2. [Arm GNU Toolchain](https://developer.arm.com/Tools%20and%20Software/GNU%20Toolchain)：选择 Windows 主机、`arm-none-eabi` 裸机目标的版本，并将工具链的 `bin` 目录加入 `PATH`。
3. [Ninja](https://ninja-build.org/)：安装后将 `ninja.exe` 所在目录加入 `PATH`。
4. [STM32CubeProgrammer](https://www.st.com/en/development-tools/stm32cubeprog.html)：用于通过 ST-LINK 烧录；安装包中也提供 Windows ST-LINK USB 驱动。

> 安装或修改 `PATH` 后，请关闭并重新打开 PowerShell，再执行前文的版本检查命令。

另外检查 Ninja：

```powershell
ninja --version
```

### 2. 在 PowerShell 中编译

```powershell
git clone <项目仓库地址>
Set-Location <项目目录>

cmake -S . -B build/windows-debug -G Ninja `
  -DCMAKE_TOOLCHAIN_FILE=cmake/arm-none-eabi-gcc.cmake `
  -DCMAKE_BUILD_TYPE=Debug
cmake --build build/windows-debug
```

如需 Release 固件：

```powershell
cmake -S . -B build/windows-release -G Ninja `
  -DCMAKE_TOOLCHAIN_FILE=cmake/arm-none-eabi-gcc.cmake `
  -DCMAKE_BUILD_TYPE=Release
cmake --build build/windows-release
```

> 仓库中的 `debug` / `release` 预设固定使用 `Unix Makefiles`，主要面向 macOS 等 Unix 环境。为避免 Windows 原生 PowerShell 下的生成器差异，上述命令显式使用 Ninja。

### 3. 烧录并运行

1. 用 SWD 连接 ST-LINK 和开发板，将 ST-LINK 接入电脑。
2. 打开 STM32CubeProgrammer，选择 `ST-LINK` / `SWD` 并连接。
3. 选择 `build\windows-debug\STM32_Project1.hex`。
4. 执行擦除、烧录和校验，然后复位开发板。

已将 CLI 加入 `PATH` 时，可在 PowerShell 中执行：

```powershell
STM32_Programmer_CLI -c port=SWD -w build\windows-debug\STM32_Project1.hex -v -rst
```

## 构建产物

以 Debug 为例，macOS 预设的文件位于 `build/debug/`，Windows Ninja 流程的文件位于 `build/windows-debug/`：

| 文件 | 用途 |
| --- | --- |
| `STM32_Project1.elf` | 带调试信息的可执行固件，适合 GDB/调试器 |
| `STM32_Project1.hex` | Intel HEX 固件，推荐用于烧录 |
| `STM32_Project1.bin` | 原始二进制固件，烧录起始地址为 `0x08000000` |
| `STM32_Project1.map` | 链接映射，用于分析符号和存储器占用 |

清理某个配置的构建结果：

```sh
cmake --build --preset debug --target clean
```

如果工具链路径变更后 CMake 仍使用旧路径，删除对应的 `build/debug` 或 `build/release` 目录后重新配置。

## 预期现象

**上电自检**：PA8 蜂鸣器已通过实机验收，因此提交版已将 `HARDWARE_SELFTEST_ON_BOOT` 置 0，上电不再额外鸣叫。需要重新 bring-up 时可临时启用 300 ms 自检。

固件启动后，OLED 第 1 页显示 `Temp: <温度>C`、`Humi: <%RH>%`、`Gas: <ppm>ppm`、`Thr: v1`，随后每 2 秒轮播到下一页并回到第 1 页。

报警时第 3 页的 `Alarm:` 后出现对应字母，`State` 显示 `ALARM`。`Buzzer` 行显示当前瞬时输出：气体告警间歇发声时交替显示 `ON`/`off`；非气体告警保持 `off`。静音不改变报警字母、LED 或上报。

## 常见问题

### CMake 找不到 Arm 编译器

典型提示为 `Could not find CMAKE_C_COMPILER using arm-none-eabi-gcc`。先确认：

```sh
arm-none-eabi-gcc --version
```

如命令不可用，将 Arm GNU Toolchain 的 `bin` 目录加入 `PATH`，重开终端，并删除已生成的构建目录后再试。

### CMake 找不到构建程序

macOS 预设生成器是 `Unix Makefiles`，因此需要 `make`；Windows 命令使用 `Ninja`，因此需要 `ninja.exe`。两者不可互相替代。请按所在平台执行 `make --version` 或 `ninja --version` 进行确认。

### ST-LINK 无法连接

- 检查 SWDIO、SWCLK、GND 和 3.3V/VTref，最好同时连接 NRST。
- 确认目标板已供电，且 ST-LINK 驱动/固件已正确安装。
- 尝试降低 SWD 频率，或选择 **Connect under reset** 再连接。

### OLED 无显示

- 检查 PB8/PB9 是否接反，以及电源和共地。
- 本驱动默认使用 128×64 OLED 和地址 `0x3C`。
- 默认显示方向与 SSD1306 配置对应；其他控制器或尺寸的屏幕可能需要修改 `OLED/OLED.c`。

### 气体浓度不准确

- 当前 `ADC/adc.c` 使用 `RL=1 kΩ`、`Ro=10 kΩ` 和简化拟合曲线，显示值为估算值。
- 实际使用前需要充分预热 MQ135，并根据传感器、负载电阻和标准气体校准 `Ro` 与拟合参数。
- 保证 `PA1` 电压不超过 3.3V；必要时在 MQ135 模块 AO 与 STM32 之间加入分压。

## 项目结构

```text
.
├── CMakeLists.txt                 # 固件构建目标与源文件
├── CMakePresets.json             # Debug/Release 构建预设
├── cmake/arm-none-eabi-gcc.cmake # Arm GNU 交叉编译工具链
├── core/                          # 纯逻辑：无 STM32 依赖，可在主机测试
│   ├── env_monitor.[ch]          # 滤波、阈值、突增判断、告警状态与静音
│   ├── display_model.[ch]        # OLED 轮播页面内容
│   ├── text_format.[ch]          # 无 libc 的整数/定点转文本
│   ├── json_writer.[ch]          # 无 libc 的 JSON 输出与转义
│   ├── telemetry_json.[ch]       # 遥测 Payload 构造
│   ├── command_json.[ch]         # 控制命令解析、requestId 去重、ACK 构造
│   ├── mqtt_packet.[ch]          # MQTT 3.1.1 报文编解码
│   ├── control_link.[ch]         # 下行控制链路：校验、去重、执行与 ACK 构造
│   ├── session_dispatch.[ch]     # MQTT 会话状态机、SUBACK 校验与多帧分发
│   ├── threshold_store.[ch]      # 阈值记录格式、CRC32、双槽读写
│   └── boot_id.[ch]              # 启动标识格式化
├── tests/                         # 主机单元测试（独立 CMake 工程）
├── scripts/host_coverage.sh       # 核心逻辑行覆盖率
├── STM32_Project1/               # 主程序、启动文件、外设库和链接脚本
├── ADC/                           # PA1 ADC 采样与 MQ135 浓度换算
├── dht11/                         # DHT11 温湿度驱动
├── Esp8266/                       # USART1 AT指令、Wi-Fi、TCP和下行消息解析
├── OLED/                          # OLED 驱动
├── OLED_DATA/                     # ASCII/中文字模
├── LED/                           # LED/蜂鸣器辅助驱动
├── HW/                            # 板级输入（PA0，编入固件）
└── 字模提取PCtoLCD2002/          # Windows 字模提取工具及配置示例
```

## 本地逻辑与主机测试

`hardware/core/` 下不依赖 STM32、GPIO 或任何驱动的模块只做"数值/字节到决策"的转换。所有安全关键判断（阈值边界、突增判定、传感器故障、静音优先级、蜂鸣器可闻原因与节奏、页面内容、下行命令的校验/去重/执行/回执）都在这里，因此可以在主机上完整测试，不需要开发板在桌上。

主机测试是独立的 CMake 工程，使用主机编译器；固件仍由交叉编译器构建，两者共用同一份 `core/` 源码。下列命令在**仓库根目录**执行（路径带 `hardware/` 前缀；固件构建见 §3，需在 `hardware/` 目录执行）。

```sh
cmake -S hardware/tests -B hardware/build/host-tests
cmake --build hardware/build/host-tests
./hardware/build/host-tests/host_tests
```

行覆盖率（阈值 80%，低于阈值脚本以非零码退出）：

```sh
cmake -S hardware/tests -B hardware/build/host-tests-coverage -DENABLE_COVERAGE=ON
cmake --build hardware/build/host-tests-coverage
./hardware/scripts/host_coverage.sh hardware/build/host-tests-coverage
```

该脚本使用 clang 的 profile 格式与 `llvm-profdata`/`llvm-cov`：macOS 上 clang 写出的文件名是 `<name>.c.gcno`，而 `gcov` 查找 `<name>.gcno`，因此 `--coverage` + gcov 无法读取自己产生的数据。

最新一次本机结果（macOS / Apple clang，2026-09-22）：1118 项断言全部通过；`core/` 聚合 **91% 行覆盖**。本轮新增/修改的部分：`core/control_link.c` 95.3% 行 / 100% 函数，`core/mqtt_packet.c` 91.6%（`MqttForEachPacket` 全分支），`core/env_monitor.c` 97.8%（`EnvMonitorBuzzerDrive` 全分支），`core/display_model.c` 100%。`core/command_json.c` 全文件 86.6%，其中本轮改动的区域 100%——其余未覆盖行是既有的解析错误路径。逐模块数字见脚本输出。

**主机测试不能替代实机验证。** 它证明的是判断逻辑本身正确，不能证明 DHT11 时序、MQ135 预热与标定、OLED 刷新、ESP8266 连接或电气连接在现场可用。见下文「已知限制」。

## Keil 工程说明

`STM32_Project1/Project.uvprojx` 是仓库中保留的旧 Keil 工程文件，但当前其源文件组为空，未完整登记现有代码。因此，Windows 下也建议使用上述 CMake + Arm GNU Toolchain 流程。若需使用 Keil µVision，应先重建工程分组、源文件、头文件路径、预处理宏和启动文件，不应直接依赖该文件构建当前固件。

## MQTT 接入设计

`../docs/device-protocol.md` 冻结的 MQTT 契约由两个部分实现：

- **报文编解码在 MCU 上完成**（`core/mqtt_packet.c`）。CONNECT、SUBSCRIBE、PUBLISH（QoS 0/1）、PUBACK、PINGREQ 由设备发出；CONNACK、SUBACK、PUBLISH、PINGRESP 由设备接收。超出该子集的（retain、will、QoS 2、MQTT 5）在发送端被拒绝，在接收端被拒绝并计数，而不是半处理。
- **遥测与控制命令的 Payload** 分别由 `core/telemetry_json.c` 与 `core/command_json.c` 构造和解析。

**为什么在 MCU 上实现而不是用 AT 的 MQTT 指令集**：ESP8266 不同 AT 固件版本的 MQTT 指令集不一致，而在这里无法确认目标模组的版本；透明 TCP 透传（`AT+CIPSTART`/`AT+CIPSEND`）是现有驱动已经在用的能力，与固件版本无关。实施方案 §4.5 把这条路列为备选，而它是两条路里唯一可以在拿到模组之前验证的——主机测试覆盖了帧格式、QoS 1 报文标识符、以及每种拒绝路径，其中 CONNECT 用逐字节的期望向量核对，因为一个只和自己一致的编解码器仍然可能发出 Broker 不接受的东西。

**实机状态（2026-09-22）**：ESP8266 驱动已按长度交付二进制 `+IPD` 数据，发送及接收缓冲可容纳 640 字节 MQTT 帧，主循环已完成 CONNECT、SUBSCRIBE、QoS 1 PUBLISH 与 PING。`MCU001 → EMQX → Go Backend → postgres-dev` 遥测链路已验证。下行命令路径（PUBLISH 解析 → 校验 → 去重 → 执行 → `device/command-ack` QoS 1 回执，以及 `set_thresholds` 的 Flash 写入与读回校验）已实现、通过主机测试并完成**实机闭环验收**——远程静音、解除静音、阈值下发/掉电保持与断网自治的逐项证据见「实机闭环验收记录」。

### 下行控制路径（`core/control_link.c`）

发送路径按固定的顺序处理一个接收缓冲：

1. `MqttForEachPacket` 逐帧扫描（一个 TCP 段可能含多帧），codec 拒绝的帧跳过并计数，尾部残帧计为 partial。
2. 主题按**字节长度**精确比对 `device/control`——PUBLISH 的主题在线上是带长度的字段、不以 NUL 结尾，因此不能假定有终止符。
3. QoS 1 的 PUBLISH **一律**回 PUBACK（传输层确认，与 payload 内容无关）；command ACK 是另一种确认，不能互相替代。
4. payload 交给 `CommandJsonParse`；校验 schemaVersion、deviceId、类型与数值范围。
5. 按 §4.1 的顺序去重（`CommandDedup`）→ 有效窗口 → 阈值版本 → 执行。
6. 能识别 requestId 的命令生成 ACK，以 QoS 1 发到 `device/command-ack`；无法识别 requestId 的 payload 不回 ACK（ACK 必须携带 requestId），但仍回 PUBACK。

`set_mute` 只写 `EnvMonitorSetMuted`：不清除 `localAlarm`，不关闭 LED、OLED 或遥测。`set_thresholds` 先经 `ThresholdStoreSave` 写入并读回校验，成功后才 `EnvMonitorSetThresholds` 生效；任一步失败回 `failed` 与对应 `errorCode`，旧配置继续生效。

接收缓冲的已知限制（多帧 / 截断 / 覆盖 / 分片）以及 OLED 上的丢帧计数见 `../docs/device-protocol.md` §4.5.1。

| 迁移前状态 | 已落地改造 |
| --- | --- |
| `ESP8266_SEND_BUFFER_SIZE` / `ESP8266_MESSAGE_BUFFER_SIZE` 均为 64 字节 | 一帧遥测 PUBLISH 约 430 字节：接收缓冲扩到 640 字节；发送改为 `ESP8266_SendBytes` 流式发送，MQTT 帧先在 640 字节的 `mqttTx` 中组装 |
| `+IPD` 正文按 NUL 结尾的文本处理 | MQTT 帧内含 0x00 字节（如 16 位长度的低字节），需要按长度而不是按字符串交付 |
| 主循环只调用 `ESP8266_Task` 维护 TCP 文本帧 | 需要一个由节拍驱动的会话状态机：CONNECT → SUBSCRIBE → 发布/心跳 |
| 收到 PUBLISH 只判断类型后丢弃 | 需要一个下行命令路径：主题比对 → 解析 → 去重 → 执行 → ACK |

上表四项已落地：前三项已通过实机遥测验收，第四项（下行命令路径）已通过实机闭环验收，见下文「实机闭环验收记录」。

## 实机闭环验收记录（2026-09-22）

板卡 STM32F103C8T6，ST-LINK V2（J37S7）+ OpenOCD 0.12.0，Arm GNU Toolchain 15.3.1，release 构建烧录（`program … verify reset`，Verify OK）。设备 `MCU001` 连手机热点 → 本机 EMQX 5.8（`:1883`）→ Go Backend（`:8080`）→ `postgres-dev`。

### 远程静音闭环：通过

| 步骤 | 证据 |
| --- | --- |
| `POST /commands/mute {"muted":true}`（带 Idempotency-Key） | 202 `pending`，requestId `01M33E9JS6E0GPMSJG5C1WYJR3` |
| 设备执行并回 `device/command-ack` | Backend 日志 `command acknowledgement received … status:"applied"`；命令资源 `state:"applied"`，accepted→completed 约 1 秒 |
| 下一次遥测 | `buzzerMuted:true`，同时 `localAlarm:true`、`alarmCauses:["temperature_high","gas_high"]`——静音没有清除报警 |
| OLED | 人工确认可见 MUTED 标识，且报警原因仍在 |
| 解除静音 | `{"muted":false}` → `state:"applied"`，遥测 `buzzerMuted:false` |
| 蜂鸣器人工听感 | 能听到，解静音后恢复间歇鸣叫；**音量偏弱**（硬件侧） |

### 接收丢帧计数：无丢帧

SWD 直读 `s_discardedFrames` / `s_truncatedFrames` 均为 0；本轮所有命令均在首次投递即被执行，未出现需要 QoS 1 重投递的帧。

### 阈值下发与掉电保持：通过

| 步骤 | 证据 |
| --- | --- |
| `PUT /thresholds {35,85,25}` | `state:"applied"`，`desiredVersion:3`、`confirmedVersion:3`、`confirmationState:"confirmed"` |
| 复位后重新上线 | 遥测 `alarmCauses` 由 `["temperature_high","gas_high"]` 变为 `["gas_high"]`，与 35 ℃ 上限一致 |
| SWD 直读 Flash | `0x0800F800` = `5248544c 00000301 19552300 00960300 92c872d6 …`：magic "LTHR"、schema 1、version 3、温度 35、湿度 85、气体 25，上升阈值保持编译期默认 3/150；`0x0800FC00` 全 `0xFF`（另一槽未动）。记录在复位后仍在，说明来自 Flash 而非 RAM |
| 恢复正式阈值 | `PUT /thresholds {30,80,20}` → `state:"applied"`、version 5 confirmed |

### 断网自治：通过（机器验证）

停止 EMQX 容器后（设备失去 Broker），SWD 直读 `GPIOA_ODR` 的 PA4 = 1（LED 报警常亮），`TIM1_CCER` 的 CC1E 在被采样的 20 次中始终为 1（蜂鸣器输出仍在驱动）。即 Broker 不可达时本地采样、判断与声光报警继续工作。重启 EMQX 后设备在无复位的情况下自行重连并恢复上报（`bootId` 未变，`sequence` 续增）。

### 蜂鸣器输出链路：已驱动，节奏为比例而非固定毫秒

SWD 直读确认：`TIM1_ARR=0x1f3`（499）、`TIM1_CCR1=0xfa`（250）、`TIM1_CR1=0x81`（CEN）、`TIM1_BDTR=0x8000`（MOE）、`GPIOA_CRH` 的 PA8 字段为 AF 推挽 50 MHz —— 与 `LED/led.c` 按 8 MHz HSI 推导出的 2 kHz、50% 占空比完全一致。CC1E 在气体报警且未静音时被反复置位/清除，即 `BEEP_On`/`BEEP_Off` 确实在执行。

**已知偏差**：节奏由 `(tick % 10) < 2` 决定，即「每 10 个主循环节拍响 2 个」，是一个**比例**而不是有界的毫秒时长。主循环某次迭代变慢时（例如 ESP8266 AT 命令等待超时、Broker 不可达时反复重试），那一次「响」会持续到该迭代结束，实测出现过 4 s 以上的连续鸣响；用户也听到过长响与短响并存。在标称 100 ms 节拍下这个比例就是 200 ms 开 / 800 ms 关，但实际节拍明显长于 100 ms。若验收要求严格的 200/800 ms 壁钟节奏，需要把节奏判据改成毫秒时基（SysTick），并重新做硬件验证——本轮未做该改动，属于已知偏差。

### 本轮未验证 / 受阻

- **Backend 侧 MQTT 会话每 30 秒断开问题（已由主线修复）**：此前 Backend 因 session 恢复后 backoff 未重置引发的周期性断开（`connection lost: EOF`），已在 PR #21 中合入 main 修复，不再是当前阻塞项。
- 真实断电（拔电）后的阈值保持未做：本轮用复位验证（记录仍在 Flash）。两槽记录设计本身就是为掉电窗口准备的，但真正的拔电验收仍待做。
- OLED 的 MUTED 显示由人工确认，不是自动化断言；其渲染逻辑由 `core/display_model.c` 的主机测试覆盖。

## 阈值掉电保存

阈值保存在保留的两页 Flash 中，链接脚本已把代码区缩短到 62 KiB 以让出这两页（`STM32_Project1/Linker/STM32F103C8Tx_FLASH.ld`），因此固件长到这两页里时链接会直接失败。

记录格式固定 20 字节，字段偏移与字节序固定，不使用结构体内存映像，因此不同编译器与对齐设置下含义不变：

```text
偏移  0  magic "LTHR" (4)
偏移  4  schemaVersion (1)
偏移  5  version (4, 小端)
偏移  9  temperatureHighC (1)
偏移 10  humidityHighRh (1)
偏移 11  gasHighPpm (2, 小端)
偏移 13  temperatureRiseC (1)
偏移 14  gasRiseAdc (2, 小端)
偏移 16  crc32 (4, 小端，覆盖偏移 0..15)
```

CRC 使用反射的 IEEE 802.3 多项式，因此标准工具（`crc32`、Python `zlib.crc32`）可以直接校验 Flash 转储；主机测试用公布的校验值 `0xCBF43926`（输入 `"123456789"`）作为基准。

**写入顺序**（双槽的意义所在）：

1. 目标槽位是**当前槽位之外**的那一个；
2. 擦除目标槽位（此时当前槽位完好）；
3. 写入新记录；
4. **读回并逐字节比较**，再校验 CRC 与版本；
5. 全部通过才切换当前槽位。

任何一步断电，另一个槽位仍保存着上一次可用配置，因此设备只会落在"旧配置"或"新配置"上，不会落在"没有配置"上。读回后的逐字节比较是必要的：CRC 只能证明记录自洽，不能证明这次写入真的生效。主机测试用 RAM 端口模拟了断电截断写入、擦除失败、写入被拒、单字节翻转与两槽同时损坏。两槽都不可用时 `ThresholdStoreLoad` 返回 false，调用方回退到编译期默认阈值——这是唯一安全的结果，用全零阈值会立刻报警。

**版本严格递增**：不比当前版本新的记录会被拒绝写入，否则一条重放的旧命令就能悄悄撤销较新的配置。

**接线状态（2026-09-22 更新）**：写入路径由控制命令处理驱动，命令链路已接入主循环并完成实机闭环验收——阈值下发、复位后读取生效均已逐项验证（见「实机闭环验收记录」与已知限制第 11 项）。上电时 `ThresholdStoreLoad` 读取最近有效记录；两槽均不可用时才回退到编译期默认阈值。

## 已知限制与待验证项

以下记录区分已经完成的首轮实机验收与仍需后续验证的边界：

1. **DHT11 已实机读数通过（2026-09-21）。** 此前稳定返回时序错误、温湿度恒为 0，根因是软件位读取太慢而非硬件，详见上文「DHT11 故障状态码 / 现场实测记录」。修复后实测板连续读数 `Temp: 30C` / `Humi: 040%`，遥测 JSON 中 `sensorFault=false`。ST-LINK 烧录/校验、OLED、MQ135 ADC、ESP8266 入网与 MQTT 上报同样已通过。
2. **蜂鸣器已实机验收通过。** 根因是固件选错端口：先前的 SWD 测量只证明 PB13/TIM1_CH1N 在输出，不能证明它与蜂鸣器 IO 物理相连。当前蜂鸣器信号端在 GPIOA，固件改用不与 ADC、DHT11、USART1 和 SWD 冲突的 PA8/TIM1_CH1 主输出（`CC1E`）。release 固件烧录后已确认发声，在 `gas_high` 实际告警期间连续采样 TIM1 CCER 也确认输出随报警切换。音量偏弱、以及节奏是比例而非固定毫秒，见「实机闭环验收记录」与下文第 13、14 项。PA13 保留给 SWDIO。
3. **烧录前必须确认固件来自当前源码。** 移动源码目录后必须用 `cmake --preset <debug|release> --fresh` 重新配置（`--fresh` 需 CMake ≥ 3.24；更低版本删除对应 `build/` 目录后重新配置即可），不能把历史构建缓存当成可移植产物。判断方法：`strings build/debug/STM32_Project1.bin | grep device/telemetry` 有输出才说明包含 MQTT 遥测路径。提交版默认关闭上电自检，不应把“上电不响”误判为烧录失败。
4. **系统时钟是 HSI 8 MHz，未启用 PLL。** `STM32_Project1/Start/system_stm32f10x.c` 中本工程（`STM32F10X_MD`）分支的全部 `SYSCLK_FREQ_*` 宏都被注释掉。当前 APB2 不分频，因此 `SystemCoreClock`、PCLK2 与 TIM1 时钟均为 8 MHz；只有 APB2 被分频时，高级定时器时钟才是 PCLK2 的两倍。蜂鸣器频率已改为按实际总线分频推导（`LED/led.c`），不再假设 72 MHz。
5. **气体估算未标定。** `MQ135_EstimatePpm` 使用 `RL=1 kΩ`、`Ro=10 kΩ` 与简化拟合曲线的示例常数，`116.30 / ratio²` 只是近似。在完成预热、负载电阻确认与标准气体标定之前，`gasPpm` 只能作为相对指标，上位机应以 `gasCalibrated=false` 与 ADC 分级呈现。阈值 20 ppm 也只是一个相对限值。
6. **ESP8266 重连仍是阻塞的。** `ESP8266_Init` 与 `ESP8266_WaitFor` 最长可等待数秒，期间主循环不会前进，本地采样会被推迟最多数秒。报警输出（LED/蜂鸣器）是 GPIO 保持状态，因此已有报警不会中断；但**新的**报警最多会被推迟这几秒。把网络状态机改为由节拍驱动的非阻塞实现属于 SHIXUN-8 的范围。停 EMQX 场景的断网自治已通过机器验证（见「实机闭环验收记录」），但阻塞期间**新**告警的延迟未量化、拔掉 AP 的场景未实测；这些补全前结论只能表述为「断网自治（停 Broker 场景）已验证」，不得扩大。
7. **Wi-Fi 与服务器配置**依赖本机 `Esp8266/esp8266_config.local.h`。缺少该文件时固件使用不可联网的占位值，只保证可编译。
8. **阈值和突增参数未现场整定。** 默认值来自实施方案文档，尚未在真实环境中统计误报与漏报。
9. **页面文字为英文。** 现有字模资源包含中文字模，但轮播页面使用 16 字符宽的 ASCII 行；改为中文需要按宽度重新排版并核验字模覆盖范围。
10. **MQTT 上行与下行均已打通。** 驱动支持 640 字节二进制帧，遥测已经由 EMQX 进入 Backend 并落库；`device/control` 的 PUBLISH 已接入命令解析、动作与 `device/command-ack`，并完成实机闭环验收（见「实机闭环验收记录」）。仍需注意：Broker 重启后 Backend 侧的控制命令发布存在独立缺陷（Backend 模块，另立分支）。
11. **阈值写入路径已接线。** Flash 双槽读写、CRC 与断电恢复有主机测试（含写入截断、擦除失败、单字节翻转）；写入由控制命令驱动，并已在实机确认记录落到 `0x0800F800` 且复位后仍生效。真正的拔电验收仍待补做。
12. **阈值小数被舍入到 1 度。** DHT11 只有 1 °C 分辨率，后台允许下发 30.5 这类小数；设备四舍五入到整度执行（30.5 → 31）。这是分辨率限制而非实现选择，已记录在 `../docs/device-protocol.md`。
13. **蜂鸣器音量偏弱。** 输出链路已被 SWD 证实正确（PA8/TIM1_CH1，2 kHz、50% 占空比、MOE 使能、CC1E 随报警切换），人工听感也能区分长响与短响，但音量偏小。属于硬件侧（蜂鸣器型号、驱动电流或串阻），未在本轮改动。
14. **发声节奏是有界性的比例而非固定毫秒。** 见「蜂鸣器输出链路」一节：`(tick % 10) < 2` 保证的是 2:8 的占空比，单次鸣响时长取决于当次主循环迭代耗时，实测可被拉长到数秒。严格 200/800 ms 壁钟节奏需要毫秒时基，属后续改动。

## Reuse Assessment

本节基于当前目录中的源码、配置、文档、资源和已有构建产物进行初始化审计。审计未修改固件行为、GPIO、传感器配置或通信协议。

### Inventory

- 自研/项目源码：`STM32_Project1/main.c` 与 `User/`（配置、Flash 端口、中断），`core/`（十一个纯逻辑模块）、`tests/`（主机单元测试），以及 ADC/MQ135、DHT11、ESP8266、OLED、LED/蜂鸣器、HW/按键输入和 delay 模块。
- 平台依赖：STM32F10x Standard Peripheral Library、CMSIS/启动文件和 STM32F103C8 链接脚本。
- 构建配置：CMake 工程、Debug/Release presets、Arm GNU Toolchain 文件，以及一个未完整登记源码的旧 Keil 工程。
- 文档与资源：本 README、`BUILDING.md`、OLED 字模数据，以及随项目保存的 Windows 字模提取工具和配置。
- 生成物：`build/`、`STM32_Project1/Objects/` 与 `STM32_Project1/Listings/` 中的编译缓存、ELF/HEX/BIN、目标文件和映射文件均为生成物（当前检出可能不含这些目录），不应被视为可移植源码事实源。

### Directly Reusable

- STM32F103C8 启动文件、链接脚本、Standard Peripheral Library 与 CMake 编译目标。
- DHT11 读取、超时与校验和处理。
- OLED SSD1306 软件 I²C 驱动、显示缓存和字模资源。
- LED 与蜂鸣器驱动，以及按节拍分频的主循环。原有「1 秒循环 + 三阈值比较」已由 `core/env_monitor` 取代：判定逻辑被抽出为纯函数，新增滤波、突增判断与传感器故障处理，并可在主机上测试。
- USART1/ESP8266 AT 通信、TCP 连接与断线状态处理已沿用并扩展：`+IPD` 正文已改为按长度交付二进制数据以承载 MQTT 帧（文本解析是迁移前形态），上/下行链路均已通过实机验收。
- 已有 HEX/BIN 可用于追溯历史结果，但重新烧录前应从当前源码重新构建。

按当前可辨识的 8 个固件功能单元（主流程、平台/构建、DHT11、ADC/MQ135、OLED、LED/蜂鸣器、ESP8266/TCP、辅助输入）统计，6 个可直接沿用，2 个需要环境配置或实机校准后沿用，即约 75% 可直接复用、25% 可经少量修改复用；没有发现必须整体重写的单元。该比例是功能单元口径，不是代码行数口径，也不代表已经完成实机验收。

### Reusable With Minor Changes

- Wi-Fi、服务器地址、端口和设备 ID 通过被 Git 忽略的 `Esp8266/esp8266_config.local.h` 按部署环境调整；仓库仅保留无敏感信息的示例。
- MQ135 估算使用固定 `RL=1 kΩ`、`Ro=10 kΩ` 和简化曲线；代码可复用，但最终测量需要预热、分压确认和实物标定。
- 阈值集中在 `STM32_Project1/User/app_config.h`，结构可沿用，数值需以最终需求为准。
- 若团队坚持使用 Keil，需要补齐工程分组和源文件配置；当前推荐继续使用已可工作的 CMake 工程。

### Needs Follow-up

- `device/control` 的 PUBLISH 已接入命令解析、静音/阈值动作、Flash 持久化与 `device/command-ack`，并已完成实机闭环验收（见「实机闭环验收记录」）。
- 仍待处理：蜂鸣器音量为硬件侧问题（能听到但偏弱）；发声节奏是「每 10 个节拍响 2 个」的**比例**，不是有界的毫秒时长，主循环某次迭代变慢时会拉长单次鸣响（见「蜂鸣器输出链路」一节与已知限制第 14 项）；Broker 重启后的控制命令发布在不修 Backend 的前提下不可靠（Backend 侧缺陷，见 Multica）。
- DHT11、OLED、MQ135、LED/蜂鸣器输出链路与 MQTT 遥测落库已通过实机验收。
- 设备协议已冻结为 v1.0.0（`../docs/device-protocol.md`）：含 `schemaVersion`、字段/单位表、错误码与变更流程。鉴权、TLS 与更严格的 Broker 边界仍属部署期工作（见 `../docs/integration-testing.md` §8）；协议后续变化必须走 §9 变更流程，同时记录并协调 Hardware 与 Backend。
- 实机/烧录验收已有可重复记录（「实机闭环验收记录」，2026-09-22）；自动化硬件测试仍缺——主机测试不能替代 HIL，后续需补可重复的自动化烧录与验收脚本。

### Known Risks / Environment Dependencies

- 网络部署值依赖本机 `Esp8266/esp8266_config.local.h`；缺少该文件时固件只能使用不可联网的安全占位值。
- GPIO、外设和器件型号均与当前接线强绑定：DHT11 PA5、MQ135 PA1、LED PA4、蜂鸣器 PA8、OLED PB8/PB9、ESP8266 USART1 PA9/PA10。
- 需要 CMake 3.20+、Arm GNU Toolchain、Make（presets）以及烧录时的 ST-LINK/STM32CubeProgrammer。
- 移动项目后，`build/debug` / `build/release` 中若残留旧源码路径的 CMake 缓存，直接复用会发生路径冲突（当前检出可能不含这些目录）。应重新配置构建目录，而不是把历史缓存当作可移植构建环境。
- `字模提取PCtoLCD2002/` 包含 Windows 可执行程序和运行库，仅适合受控的 Windows 环境；其来源、许可与安全性需由团队确认。
- DHT11 时序、ESP AT 固件差异、MQ135 供电/分压与预热均依赖真实硬件环境。

## Collaboration Workflow

```sh
git clone <repository-url>
cd final
git switch main
git pull --ff-only
git switch -c feat/hardware-mqtt-telemetry

# 修改并完成交叉编译；有条件时执行烧录与实机验证
git add hardware docs/device-protocol.md
git commit -m "feat(hardware): publish mqtt telemetry"
git push -u origin feat/hardware-mqtt-telemetry
```

分支必须采用 `<type>/hardware-<complete-description>`，例如 `fix/hardware-dht11-timeout`。提交 PR 时关联 Multica issue，记录板卡/接线、工具链、构建结果、烧录与实机验证状态；协议、引脚或阈值变化必须请求 Backend 及相关负责人审核。检查通过并获批准后才能合并。
