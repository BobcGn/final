cmake_minimum_required(VERSION 3.20)

project(STM32_Project1 LANGUAGES C ASM)

set(CMAKE_C_STANDARD 11)
set(CMAKE_C_STANDARD_REQUIRED ON)
set(CMAKE_C_EXTENSIONS ON)

set(MCU_FLAGS
    -mcpu=cortex-m3
    -mthumb
)

file(GLOB STD_PERIPH_SOURCES CONFIGURE_DEPENDS
    "${CMAKE_CURRENT_SOURCE_DIR}/STM32_Project1/Library/*.c"
)

add_executable(${PROJECT_NAME}
    STM32_Project1/main.c
    STM32_Project1/Start/startup_stm32f103c8_gcc.S
    STM32_Project1/Start/system_stm32f10x.c
    STM32_Project1/User/stm32f10x_it.c
    STM32_Project1/User/flash_config.c
    STM32_Project1/System/delay/delay.c
    ADC/adc.c
    core/text_format.c
    core/env_monitor.c
    core/display_model.c
    core/json_writer.c
    core/telemetry_json.c
    core/command_json.c
    core/mqtt_packet.c
    core/control_link.c
    core/session_dispatch.c
    core/threshold_store.c
    core/boot_id.c
    dht11/dht11.c
    Esp8266/esp8266.c
    LED/led.c
    HW/HW.c
    OLED/OLED.c
    OLED_DATA/OLED_Data.c
    ${STD_PERIPH_SOURCES}
)

# The imported bitmap tables intentionally use flat initializers.
set_source_files_properties(OLED_DATA/OLED_Data.c PROPERTIES
    COMPILE_OPTIONS -Wno-missing-braces
)

target_include_directories(${PROJECT_NAME} PRIVATE
    STM32_Project1/Start
    STM32_Project1/User
    STM32_Project1/Library
    STM32_Project1/System/delay
    ADC
    core
    dht11
    Esp8266
    LED
    HW
    OLED
    OLED_DATA
)

target_compile_definitions(${PROJECT_NAME} PRIVATE
    STM32F10X_MD
    USE_STDPERIPH_DRIVER
)

target_compile_options(${PROJECT_NAME} PRIVATE
    ${MCU_FLAGS}
    -ffunction-sections
    -fdata-sections
    -ffreestanding
    -fno-builtin
    -fno-tree-loop-distribute-patterns
    -fno-common
    -Wa,--noexecstack
    -Wall
    -Wextra
)

set(LINKER_SCRIPT
    "${CMAKE_CURRENT_SOURCE_DIR}/STM32_Project1/Linker/STM32F103C8Tx_FLASH.ld"
)

target_link_options(${PROJECT_NAME} PRIVATE
    ${MCU_FLAGS}
    -nostdlib
    "-T${LINKER_SCRIPT}"
    "-Wl,-Map=${CMAKE_CURRENT_BINARY_DIR}/${PROJECT_NAME}.map"
    -Wl,--gc-sections
    -Wl,--no-warn-execstack
    -Wl,--print-memory-usage
)

# Compiler helper routines (integer division, shifts, and similar intrinsics).
target_link_libraries(${PROJECT_NAME} PRIVATE gcc)

set_target_properties(${PROJECT_NAME} PROPERTIES
    SUFFIX ".elf"
)

add_custom_command(TARGET ${PROJECT_NAME} POST_BUILD
    COMMAND ${CMAKE_OBJCOPY} -O ihex
            $<TARGET_FILE:${PROJECT_NAME}>
            ${CMAKE_CURRENT_BINARY_DIR}/${PROJECT_NAME}.hex
    COMMAND ${CMAKE_OBJCOPY} -O binary
            $<TARGET_FILE:${PROJECT_NAME}>
            ${CMAKE_CURRENT_BINARY_DIR}/${PROJECT_NAME}.bin
    COMMAND ${CMAKE_SIZE} $<TARGET_FILE:${PROJECT_NAME}>
    COMMENT "Generating HEX/BIN files and printing firmware size"
)
