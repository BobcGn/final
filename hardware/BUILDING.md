# macOS 构建说明

## 依赖

- CMake 3.20 或更新版本
- Arm GNU Toolchain（命令名为 `arm-none-eabi-gcc`）
- Make（macOS 安装 Xcode Command Line Tools 后自带）

## Debug 构建

```sh
cmake --preset debug
cmake --build --preset debug
```

生成文件位于 `build/debug/`：

- `STM32_Project1.elf`：调试符号和可执行固件
- `STM32_Project1.hex`：Intel HEX 固件
- `STM32_Project1.bin`：原始二进制固件
- `STM32_Project1.map`：链接映射

## Release 构建

```sh
cmake --preset release
cmake --build --preset release
```

本构建目标为 STM32F103C8（Cortex-M3、64 KiB Flash、20 KiB RAM）。

`STM32_Project1/Start/core_cm3.c` 是 2009 年 CMSIS 为旧编译器提供的兼容实现，其中的
独占访问内联汇编与新版 GCC 15 不兼容，因此 GCC 构建不编译该文件。
当前工程使用的 Cortex-M3 核心定义仍由 `STM32_Project1/Start/core_cm3.h` 提供。
