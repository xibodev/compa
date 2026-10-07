---
name: hardware
description: Read and control I2C and SPI peripherals on Sipeed boards (LicheeRV Nano, MaixCAM, NanoKVM).
homepage: https://wiki.sipeed.com/hardware/en/lichee/RV_Nano/1_intro.html
metadata: {"compa":{"emoji":"🔧","os":["linux"],"requires":{"tools":["i2c","spi"]}}}
---

# Hardware (I2C / SPI)

Use the `i2c` and `spi` tools to interact with sensors, displays, and other peripherals connected to the board.

## Quick Start

```
# 1. Find available buses
i2c detect

# 2. Scan for connected devices
i2c scan  (bus: "1")

# 3. Read from a sensor (e.g. AHT20 temperature/humidity)
i2c read  (bus: "1", address: 0x38, register: 0xAC, length: 6)

# 4. SPI devices
spi list
spi read  (device: "2.0", length: 4)
```

## Before You Start — Pinmux Setup

Some I2C/SPI pins are shared with WiFi on Sipeed boards and need pinmux setup before use.
Setup changes system state, so explain the steps and let the user run them. Never stop WiFi,
write registers with `devmem` or run anything as root unless the user explicitly asks you to.

See `references/board-pinout.md` for board-specific details.

**Common steps:**
1. Prefer buses that need no pinmux change (software I2C/SPI, or buses not shared with WiFi)
2. Load the i2c-dev module: `modprobe i2c-dev` (needs admin rights; ask the user)
3. Shared pins only: pinmux changes stop WiFi on that board until reboot; the user decides
4. Verify with `i2c detect` and `i2c scan`

## Safety

- **Write operations** require `confirm: true` — always confirm with the user first
- I2C addresses are validated to 7-bit range (0x03-0x77)
- SPI modes are validated (0-3 only)
- Maximum per-transaction: 256 bytes (I2C), 4096 bytes (SPI)

## Common Devices

See `references/common-devices.md` for register maps and usage of popular sensors:
AHT20, BME280, SSD1306 OLED, MPU6050 IMU, DS3231 RTC, INA219 power monitor, PCA9685 PWM, and more.

## Troubleshooting

| Problem | Solution |
|---------|----------|
| No I2C buses found | `modprobe i2c-dev` and check device tree |
| Permission denied | Ask the user to add their account to the `i2c` group; don't run as root |
| No devices on scan | Check wiring, pull-up resistors (4.7k typical), and pinmux |
| Bus number changed | I2C adapter numbers can shift between boots; use `i2c detect` to find current assignment |
| WiFi stopped working | I2C-1/SPI-2 share pins with WiFi SDIO; can't use both simultaneously |
| `devmem` not found | Pinmux changes are the user's call; point them to `references/board-pinout.md` |
| SPI transfer returns all zeros | Check MISO wiring and device power |
| SPI transfer returns all 0xFF | Device not responding; check CS pin and clock polarity (mode) |
