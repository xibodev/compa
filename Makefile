.PHONY: all build kernel shell product build-linux-arm build-linux-arm64 \
	build-linux-mipsle build-android-arm64 build-shell-android-arm64 build-android-bundle build-pi-zero \
	build-all install uninstall uninstall-all clean vet test fmt fmt-check lint lint-docs fix deps \
	update-deps check run build-macos-app help

# Build variables
# THE TWO SHIPPABLE PRODUCTS. compa is the FULL thing (the shell the user
# launches); compa-kernel is that minus the web shell -- a complete
# agentic harness that runs on its own.
SHELL_NAME=compa
KERNEL_NAME=compa-kernel
BUILD_DIR=build
CMD_DIR=cmd/$(KERNEL_NAME)
EXT=

ifeq ($(OS),Windows_NT)
	POWERSHELL=powershell -NoProfile -Command
	# make runs commands with sh when it finds one (Git Bash, MSYS2), which
	# makes SHELL a full path, and with cmd otherwise; to sh, NUL is a file.
	DEVNULL:=$(if $(findstring /,$(SHELL)),/dev/null,NUL)
	WINDOWS_GOARCH_RAW:=$(strip $(shell go env GOARCH 2>$(DEVNULL)))
endif

# Version. Computed once per make run, so every binary one run builds --
# `make product` builds both -- carries the same version, commit and build
# time. The leading "v" of a tag is dropped: release builds stamp v1.2.3 as
# 1.2.3, and a local build of that tag says the same.
ifeq ($(OS),Windows_NT)
	VERSION_RAW:=$(patsubst v%,%,$(strip $(shell git describe --tags --always --dirty 2>$(DEVNULL))))
	GIT_COMMIT_RAW:=$(strip $(shell git rev-parse --short=8 HEAD 2>$(DEVNULL)))
	BUILD_TIME_RAW:=$(strip $(shell powershell -NoProfile -Command "Get-Date -Format 'yyyy-MM-ddTHH:mm:ssK'"))
	GO_VERSION_RAW:=$(strip $(shell go env GOVERSION 2>$(DEVNULL)))
else
	VERSION_RAW:=$(patsubst v%,%,$(strip $(shell git describe --tags --always --dirty 2>/dev/null)))
	GIT_COMMIT_RAW:=$(strip $(shell git rev-parse --short=8 HEAD 2>/dev/null))
	BUILD_TIME_RAW:=$(strip $(shell date +%FT%T%z))
	GO_VERSION_RAW:=$(strip $(shell go env GOVERSION 2>/dev/null))
endif
VERSION?=$(if $(VERSION_RAW),$(VERSION_RAW),dev)
GIT_COMMIT=$(if $(GIT_COMMIT_RAW),$(GIT_COMMIT_RAW),dev)
BUILD_TIME=$(if $(BUILD_TIME_RAW),$(BUILD_TIME_RAW),dev)
GO_VERSION=$(if $(GO_VERSION_RAW),$(firstword $(GO_VERSION_RAW)),unknown)
CONFIG_PKG=github.com/xibodev/compa/v3/pkg/config
LDFLAGS=-X $(CONFIG_PKG).Version=$(VERSION) -X $(CONFIG_PKG).GitCommit=$(GIT_COMMIT) -X $(CONFIG_PKG).BuildTime=$(BUILD_TIME) -X $(CONFIG_PKG).GoVersion=$(GO_VERSION) -s -w

# Go variables
GO?=go
WEB_GO?=$(GO)
CGO_ENABLED?=0
GO_BUILD_TAGS?=goolm,stdjson
# Not GOFLAGS: the go command also reads that from the environment, where a
# value of its own would replace these tags.
GO_BUILD_FLAGS?=-tags $(GO_BUILD_TAGS)
export CGO_ENABLED
comma:=,
empty:=
space:=$(empty) $(empty)
GO_BUILD_TAGS_NO_GOOLM:=$(subst $(space),$(comma),$(strip $(filter-out goolm,$(subst $(comma),$(space),$(GO_BUILD_TAGS)))))
GO_BUILD_FLAGS_NO_GOOLM?=-tags $(GO_BUILD_TAGS_NO_GOOLM)

# Go keeps its build and module caches where it always does; LOCAL_CACHE=1
# keeps them in .cache/ in this checkout instead.
ifeq ($(LOCAL_CACHE),1)
export GOCACHE:=$(CURDIR)/.cache/go-build
export GOMODCACHE:=$(CURDIR)/.cache/go-mod
endif

# Patch MIPS LE ELF e_flags (offset 36) for NaN2008-only kernels (e.g. Ingenic X2600).
#
# Bytes (octal): \004 \024 \000 \160  →  little-endian 0x70001404
#   0x70000000  EF_MIPS_ARCH_32R2   MIPS32 Release 2
#   0x00001000  EF_MIPS_ABI_O32     O32 ABI
#   0x00000400  EF_MIPS_NAN2008     IEEE 754-2008 NaN encoding
#   0x00000004  EF_MIPS_CPIC        PIC calling sequence
#
# Go's GOMIPS=softfloat emits no FP instructions, so the NaN mode is irrelevant
# at runtime — this is purely an ELF metadata fix to satisfy the kernel's check.
# patchelf cannot modify e_flags; dd at a fixed offset is the most portable way.
#
# Ref: https://codebrowser.dev/linux/linux/arch/mips/include/asm/elf.h.html
define PATCH_MIPS_FLAGS
	@if [ -f "$(1)" ]; then \
		printf '\004\024\000\160' | dd of="$(1)" bs=1 seek=36 count=4 conv=notrunc 2>/dev/null || \
		{ echo "Error: failed to patch MIPS e_flags for $(1)"; exit 1; }; \
	else \
		echo "Error: $(1) not found, cannot patch MIPS e_flags"; exit 1; \
	fi
endef

# Golangci-lint
GOLANGCI_LINT?=golangci-lint

# Installation
INSTALL_PREFIX?=$(HOME)/.local
INSTALL_BIN_DIR=$(INSTALL_PREFIX)/bin
INSTALL_TMP_SUFFIX=.new

# Compa's data folder, which uninstall-all deletes.
COMPA_HOME?=$(HOME)/.compa

LNCMD=ln -sf

# OS detection
ifeq ($(OS),Windows_NT)
	UNAME_S=Windows
	ifeq ($(WINDOWS_GOARCH_RAW),amd64)
		UNAME_M=x86_64
	else ifeq ($(WINDOWS_GOARCH_RAW),arm64)
		UNAME_M=arm64
	else ifeq ($(WINDOWS_GOARCH_RAW),386)
		UNAME_M=x86
	else
		UNAME_M=$(if $(WINDOWS_GOARCH_RAW),$(WINDOWS_GOARCH_RAW),x86_64)
	endif
else
	UNAME_S?=$(shell uname -s)
	UNAME_M?=$(shell uname -m)
endif

# Platform-specific settings
ifeq ($(UNAME_S),Linux)
	PLATFORM=linux
	ifeq ($(UNAME_M),x86_64)
		ARCH=amd64
	else ifeq ($(UNAME_M),aarch64)
		ARCH=arm64
	else ifeq ($(UNAME_M),armv81)
		ARCH=arm64
	else ifeq ($(UNAME_M),loongarch64)
		ARCH=loong64
	else ifeq ($(UNAME_M),riscv64)
		ARCH=riscv64
	else ifeq ($(UNAME_M),mipsel)
		ARCH=mipsle
	else
		ARCH=$(UNAME_M)
	endif
else ifeq ($(UNAME_S),Darwin)
	PLATFORM=darwin
	# The shell's tray icon (fyne.io/systray) needs cgo on macOS; without it
	# the shell runs with no tray. macOS 12 is Go's own minimum.
	WEB_GO=CGO_LDFLAGS="-mmacosx-version-min=12.0" CGO_CFLAGS="-mmacosx-version-min=12.0" CGO_ENABLED=1 $(GO)
	ifeq ($(UNAME_M),x86_64)
		ARCH?=amd64
	else ifeq ($(UNAME_M),arm64)
		ARCH?=arm64
	else
		ARCH?=$(UNAME_M)
	endif
else
	PLATFORM=$(UNAME_S)
	ifeq ($(UNAME_M),x86_64)
		ARCH?=amd64
	else
		ARCH?=$(UNAME_M)
	endif
	# Detect Windows (Git Bash / MSYS2)
	IS_WINDOWS:=$(if $(findstring MINGW,$(UNAME_S)),yes,$(if $(findstring MSYS,$(UNAME_S)),yes,$(if $(findstring CYGWIN,$(UNAME_S)),yes,no)))
	ifeq ($(IS_WINDOWS),yes)
		EXT=.exe
		LNCMD=cp
	else ifeq ($(UNAME_S),windows) # failsafe for force windows build in other OS using UNAME_S=windows
		EXT=.exe
	endif
endif

ifeq ($(OS),Windows_NT)
	PLATFORM=windows
	ifeq ($(UNAME_M),x86_64)
		ARCH?=amd64
	else ifeq ($(UNAME_M),arm64)
		ARCH?=arm64
	else
		ARCH?=$(UNAME_M)
	endif
	EXT=.exe
endif

ifneq ($(strip $(GOOS)),)
	PLATFORM:=$(GOOS)
endif

ifneq ($(strip $(GOARCH)),)
	ARCH:=$(GOARCH)
endif

ifeq ($(PLATFORM),windows)
	EXT=.exe
endif

BINARY_PATH=$(BUILD_DIR)/$(KERNEL_NAME)-$(PLATFORM)-$(ARCH)

# uninstall-all deletes your data, so it asks first, before anything runs.
ifneq ($(filter uninstall-all,$(MAKECMDGOALS)),)
ifneq ($(CONFIRM),1)
$(error uninstall-all deletes $(COMPA_HOME): your settings, keys, chats and workspace. Run 'make uninstall-all CONFIRM=1' to do that)
endif
endif

# Default target
all: build

## kernel: Build compa-kernel, the harness without the web shell
#
# This is the full product MINUS the browser: agent loop, message bus, every
# channel transport, tools, skills, hooks, sessions, the module host, the CLI
# and the TUI. It runs headless, as a TUI, or as a gateway serving channels.
#
# It is the SAME binary the full product ships beside the shell, under the
# same name, so a standalone install and a bundled one cannot drift.
kernel:
	@echo "Building $(KERNEL_NAME)$(EXT) for $(PLATFORM)/$(ARCH)..."
ifeq ($(OS),Windows_NT)
	@$(POWERSHELL) "New-Item -ItemType Directory -Force -Path '$(BUILD_DIR)' | Out-Null"
	@$(GO) build $(GO_BUILD_FLAGS) -ldflags "$(LDFLAGS)" -o "$(BUILD_DIR)/$(KERNEL_NAME)$(EXT)" ./$(CMD_DIR)
else
	@mkdir -p "$(BUILD_DIR)"
	@GOOS=$(PLATFORM) GOARCH=$(ARCH) $(GO) build $(GO_BUILD_FLAGS) -ldflags "$(LDFLAGS)" -o "$(BUILD_DIR)/$(KERNEL_NAME)$(EXT)" ./$(CMD_DIR)
endif
	@echo "Built $(BUILD_DIR)/$(KERNEL_NAME)$(EXT) -- runs standalone, no shell required"

## shell: Build compa, the web shell, with the UI compiled in
#
# THE FRONTEND BUILD IS NOT OPTIONAL and is why this is one target rather than
# two. The UI compiles into web/backend/dist and is embedded at LINK time, so
# building the Go binary without that step produces something that starts,
# serves, and looks fine while shipping a stale interface. Nothing reports it.
# The frontend uses pnpm (the version packageManager pins in package.json);
# run `pnpm install` in web/frontend once first.
shell:
	@echo "Building the web UI..."
	@cd web/frontend && pnpm run build:backend
	@echo "Linking the shell with the UI embedded..."
ifeq ($(OS),Windows_NT)
	@$(POWERSHELL) "New-Item -ItemType Directory -Force -Path '$(BUILD_DIR)' | Out-Null"
	@cd web/backend && $(GO) build $(GO_BUILD_FLAGS) -ldflags "$(LDFLAGS) -H windowsgui" -o "../../$(BUILD_DIR)/$(SHELL_NAME)$(EXT)" .
else
	@mkdir -p "$(BUILD_DIR)"
	@cd web/backend && GOOS=$(PLATFORM) GOARCH=$(ARCH) $(WEB_GO) build $(GO_BUILD_FLAGS) -ldflags "$(LDFLAGS)" -o "../../$(BUILD_DIR)/$(SHELL_NAME)$(EXT)" .
endif
	@echo "Built $(BUILD_DIR)/$(SHELL_NAME)$(EXT) -- needs a kernel beside it"

## product: Build the full product, compa and the compa-kernel it runs, into build/
#
# Both binaries in one directory, which is the coupling: the shell locates the
# kernel BESIDE ITS OWN EXECUTABLE. Ship one without the other and the shell
# starts, serves a login page, and reports that the harness exited -- exactly
# what a crash looks like from the outside. One make run computes LDFLAGS
# once, so both carry the same version (override with VERSION=1.2.3).
product: kernel shell
	@echo ""
	@echo "Full product $(VERSION) in $(BUILD_DIR)/:"
	@echo "  $(SHELL_NAME)$(EXT)   <- the user launches this"
	@echo "  $(KERNEL_NAME)$(EXT)  <- it supervises this"

## build: Build compa-kernel for the current platform (platform-suffixed, plus a plain copy)
build:
	@echo "Building $(KERNEL_NAME)$(EXT) for $(PLATFORM)/$(ARCH)..."
ifeq ($(OS),Windows_NT)
	@$(POWERSHELL) "New-Item -ItemType Directory -Force -Path '$(BUILD_DIR)' | Out-Null"
	@$(GO) build $(GO_BUILD_FLAGS) -ldflags "$(LDFLAGS)" -o "$(BINARY_PATH)$(EXT)" ./$(CMD_DIR)
	@$(POWERSHELL) "Copy-Item -LiteralPath '$(BINARY_PATH)$(EXT)' -Destination '$(BUILD_DIR)/$(KERNEL_NAME)$(EXT)' -Force"
else
	@mkdir -p "$(BUILD_DIR)"
	@GOOS=$(PLATFORM) GOARCH=$(ARCH) $(GO) build $(GO_BUILD_FLAGS) -ldflags "$(LDFLAGS)" -o "$(BINARY_PATH)$(EXT)" ./$(CMD_DIR)
	@echo "Build complete: $(BINARY_PATH)$(EXT)"
	@$(LNCMD) "$(KERNEL_NAME)-$(PLATFORM)-$(ARCH)$(EXT)" "$(BUILD_DIR)/$(KERNEL_NAME)$(EXT)"
endif
	@echo "Build complete: $(BUILD_DIR)/$(KERNEL_NAME)$(EXT)"

## build-linux-arm: Build compa-kernel for Linux ARMv7 (e.g. Raspberry Pi Zero 2 W 32-bit)
build-linux-arm:
	@echo "Building for linux/arm (GOARM=7)..."
	@mkdir -p "$(BUILD_DIR)"
	GOOS=linux GOARCH=arm GOARM=7 $(GO) build $(GO_BUILD_FLAGS) -ldflags "$(LDFLAGS)" -o "$(BUILD_DIR)/$(KERNEL_NAME)-linux-arm" ./$(CMD_DIR)
	@echo "Build complete: $(BUILD_DIR)/$(KERNEL_NAME)-linux-arm"

## build-linux-arm64: Build compa-kernel for Linux ARM64 (e.g. Raspberry Pi Zero 2 W 64-bit)
build-linux-arm64:
	@echo "Building for linux/arm64..."
	@mkdir -p "$(BUILD_DIR)"
	GOOS=linux GOARCH=arm64 $(GO) build $(GO_BUILD_FLAGS) -ldflags "$(LDFLAGS)" -o "$(BUILD_DIR)/$(KERNEL_NAME)-linux-arm64" ./$(CMD_DIR)
	@echo "Build complete: $(BUILD_DIR)/$(KERNEL_NAME)-linux-arm64"

## build-linux-mipsle: Build compa-kernel for Linux MIPS32 LE
build-linux-mipsle:
	@echo "Building for linux/mipsle (softfloat)..."
	@mkdir -p "$(BUILD_DIR)"
	GOOS=linux GOARCH=mipsle GOMIPS=softfloat $(GO) build $(GO_BUILD_FLAGS_NO_GOOLM) -ldflags "$(LDFLAGS)" -o "$(BUILD_DIR)/$(KERNEL_NAME)-linux-mipsle" ./$(CMD_DIR)
	$(call PATCH_MIPS_FLAGS,$(BUILD_DIR)/$(KERNEL_NAME)-linux-mipsle)
	@echo "Build complete: $(BUILD_DIR)/$(KERNEL_NAME)-linux-mipsle"

## build-android-arm64: Build compa-kernel for Android ARM64
build-android-arm64:
	@echo "Building for android/arm64..."
	@mkdir -p "$(BUILD_DIR)"
	GOOS=android GOARCH=arm64 $(GO) build -tags stdjson -ldflags "$(LDFLAGS)" -o "$(BUILD_DIR)/$(KERNEL_NAME)-android-arm64" ./$(CMD_DIR)
	@echo "Build complete: $(BUILD_DIR)/$(KERNEL_NAME)-android-arm64"

## build-shell-android-arm64: Build compa, the web shell, for Android ARM64 with the UI compiled in
build-shell-android-arm64:
	@echo "Building the web UI..."
	@cd web/frontend && pnpm run build:backend
	@echo "Building $(SHELL_NAME) for android/arm64..."
	@mkdir -p "$(BUILD_DIR)"
	@cd web/backend && GOOS=android GOARCH=arm64 $(GO) build -tags stdjson -ldflags "$(LDFLAGS)" -o "../../$(BUILD_DIR)/$(SHELL_NAME)-android-arm64" .
	@echo "Build complete: $(BUILD_DIR)/$(SHELL_NAME)-android-arm64"

## build-android-bundle: Build compa-kernel and compa for Android and package them as a universal zip
build-android-bundle: build-android-arm64 build-shell-android-arm64
	@echo "Staging JNI libs..."
	@rm -rf "$(BUILD_DIR)/android-staging"
	@mkdir -p "$(BUILD_DIR)/android-staging/arm64-v8a"
	@cp "$(BUILD_DIR)/$(KERNEL_NAME)-android-arm64" "$(BUILD_DIR)/android-staging/arm64-v8a/lib$(KERNEL_NAME).so"
	@cp "$(BUILD_DIR)/$(SHELL_NAME)-android-arm64" "$(BUILD_DIR)/android-staging/arm64-v8a/lib$(SHELL_NAME).so"
	@cd "$(BUILD_DIR)/android-staging" && zip -r "../$(SHELL_NAME)-android-universal.zip" .
	@rm -rf "$(BUILD_DIR)/android-staging"
	@echo "All Android builds complete: $(BUILD_DIR)/$(SHELL_NAME)-android-universal.zip"

## build-pi-zero: Build compa-kernel for Raspberry Pi Zero 2 W (32-bit and 64-bit)
build-pi-zero: build-linux-arm build-linux-arm64
	@echo "Pi Zero 2 W builds: $(BUILD_DIR)/$(KERNEL_NAME)-linux-arm (32-bit), $(BUILD_DIR)/$(KERNEL_NAME)-linux-arm64 (64-bit)"

## build-all: Build compa-kernel for all Makefile-managed platforms
build-all:
	@echo "Building for multiple platforms..."
	@mkdir -p "$(BUILD_DIR)"
	GOOS=linux GOARCH=amd64 $(GO) build $(GO_BUILD_FLAGS) -ldflags "$(LDFLAGS)" -o "$(BUILD_DIR)/$(KERNEL_NAME)-linux-amd64" ./$(CMD_DIR)
	GOOS=linux GOARCH=arm GOARM=7 $(GO) build $(GO_BUILD_FLAGS) -ldflags "$(LDFLAGS)" -o "$(BUILD_DIR)/$(KERNEL_NAME)-linux-arm" ./$(CMD_DIR)
	GOOS=linux GOARCH=arm64 $(GO) build $(GO_BUILD_FLAGS) -ldflags "$(LDFLAGS)" -o "$(BUILD_DIR)/$(KERNEL_NAME)-linux-arm64" ./$(CMD_DIR)
	GOOS=linux GOARCH=loong64 $(GO) build $(GO_BUILD_FLAGS) -ldflags "$(LDFLAGS)" -o "$(BUILD_DIR)/$(KERNEL_NAME)-linux-loong64" ./$(CMD_DIR)
	GOOS=linux GOARCH=riscv64 $(GO) build $(GO_BUILD_FLAGS) -ldflags "$(LDFLAGS)" -o "$(BUILD_DIR)/$(KERNEL_NAME)-linux-riscv64" ./$(CMD_DIR)
	GOOS=linux GOARCH=mipsle GOMIPS=softfloat $(GO) build $(GO_BUILD_FLAGS_NO_GOOLM) -ldflags "$(LDFLAGS)" -o "$(BUILD_DIR)/$(KERNEL_NAME)-linux-mipsle" ./$(CMD_DIR)
	$(call PATCH_MIPS_FLAGS,$(BUILD_DIR)/$(KERNEL_NAME)-linux-mipsle)
	GOOS=darwin GOARCH=arm64 $(GO) build $(GO_BUILD_FLAGS) -ldflags "$(LDFLAGS)" -o "$(BUILD_DIR)/$(KERNEL_NAME)-darwin-arm64" ./$(CMD_DIR)
	GOOS=windows GOARCH=amd64 $(GO) build $(GO_BUILD_FLAGS) -ldflags "$(LDFLAGS)" -o "$(BUILD_DIR)/$(KERNEL_NAME)-windows-amd64.exe" ./$(CMD_DIR)
	GOOS=netbsd GOARCH=amd64 $(GO) build $(GO_BUILD_FLAGS) -ldflags "$(LDFLAGS)" -o "$(BUILD_DIR)/$(KERNEL_NAME)-netbsd-amd64" ./$(CMD_DIR)
	GOOS=netbsd GOARCH=arm64 $(GO) build $(GO_BUILD_FLAGS) -ldflags "$(LDFLAGS)" -o "$(BUILD_DIR)/$(KERNEL_NAME)-netbsd-arm64" ./$(CMD_DIR)
	@echo "Core builds complete"

## install: Build compa and compa-kernel and install both into INSTALL_PREFIX/bin
install: product
	@echo "Installing $(SHELL_NAME)$(EXT) and $(KERNEL_NAME)$(EXT) into $(INSTALL_BIN_DIR)..."
	@mkdir -p "$(INSTALL_BIN_DIR)"
# Both are copied in before either is replaced, so a failed copy changes nothing.
	@for program in "$(KERNEL_NAME)$(EXT)" "$(SHELL_NAME)$(EXT)"; do \
		cp "$(BUILD_DIR)/$$program" "$(INSTALL_BIN_DIR)/$$program$(INSTALL_TMP_SUFFIX)" && \
		chmod +x "$(INSTALL_BIN_DIR)/$$program$(INSTALL_TMP_SUFFIX)" || exit 1; \
	done
	@for program in "$(KERNEL_NAME)$(EXT)" "$(SHELL_NAME)$(EXT)"; do \
		mv -f "$(INSTALL_BIN_DIR)/$$program$(INSTALL_TMP_SUFFIX)" "$(INSTALL_BIN_DIR)/$$program" || exit 1; \
	done
	@echo "Installed $(INSTALL_BIN_DIR)/$(SHELL_NAME)$(EXT) and $(INSTALL_BIN_DIR)/$(KERNEL_NAME)$(EXT)"

## uninstall: Remove compa and compa-kernel from INSTALL_PREFIX/bin; your data stays
uninstall:
	@rm -f "$(INSTALL_BIN_DIR)/$(SHELL_NAME)$(EXT)" "$(INSTALL_BIN_DIR)/$(KERNEL_NAME)$(EXT)"
	@echo "Removed $(SHELL_NAME)$(EXT) and $(KERNEL_NAME)$(EXT) from $(INSTALL_BIN_DIR)"
	@echo "Your settings and data are still in $(COMPA_HOME); 'make uninstall-all CONFIRM=1' deletes them too."

## uninstall-all: Run uninstall, then delete COMPA_HOME with all your Compa data (needs CONFIRM=1)
uninstall-all: uninstall
	@case "$(COMPA_HOME)" in \
	"" | / | "$(HOME)" | "$(HOME)/") echo "Refusing to delete '$(COMPA_HOME)'; set COMPA_HOME to Compa's data folder."; exit 1 ;; \
	esac
	@rm -rf -- "$(COMPA_HOME)"
	@echo "Deleted $(COMPA_HOME)"

## clean: Remove build artifacts
clean:
	@echo "Cleaning build artifacts..."
ifeq ($(OS),Windows_NT)
	@$(POWERSHELL) "if (Test-Path -LiteralPath '$(BUILD_DIR)') { Remove-Item -LiteralPath '$(BUILD_DIR)' -Recurse -Force }"
else
	@rm -rf -- "$(BUILD_DIR)"
endif
	@echo "Clean complete"

## vet: Run go vet, as CI does
vet:
	@$(WEB_GO) vet $(GO_BUILD_FLAGS) ./...

## test: Run the Go tests one package at a time, as CI does, and the web UI tests
test:
	@$(WEB_GO) test $(GO_BUILD_FLAGS) -p 1 ./...
	@cd web/frontend && pnpm test

## fmt: Format the Go code (changes files)
fmt:
	@$(GOLANGCI_LINT) fmt

## fmt-check: Show Go code that needs formatting, without changing it
fmt-check:
	@$(GOLANGCI_LINT) fmt --diff

## lint: Run golangci-lint with .golangci.yml
lint:
	@$(GOLANGCI_LINT) run

## lint-docs: Check that relative links in the Markdown docs point at files that exist
lint-docs:
	@sh scripts/check-doc-links.sh

## fix: Fix linting issues (changes files)
fix:
	@$(GOLANGCI_LINT) run --fix

## deps: Download and verify dependencies
deps:
	@$(GO) mod download
	@$(GO) mod verify

## update-deps: Update dependencies (changes go.mod and go.sum)
update-deps:
	@$(GO) get -u ./...
	@$(GO) mod tidy

## check: Run deps, go mod tidy -diff, fmt-check, vet, lint, lint-docs and test; changes nothing
check: deps fmt-check vet lint lint-docs test
	@$(GO) mod tidy -diff

## run: Build and run compa-kernel; pass arguments with ARGS="..."
run: build
	@"$(BUILD_DIR)/$(KERNEL_NAME)$(EXT)" $(ARGS)

## build-macos-app: Build build/Compa.app, which runs compa without a terminal window (macOS only)
build-macos-app:
	@if [ "$(UNAME_S)" != "Darwin" ]; then \
		echo "Error: build-macos-app works only on macOS"; \
		exit 1; \
	fi
	@$(MAKE) product
	@VERSION="$(VERSION)" ./scripts/build-macos-app.sh

## help: Show this help
help:
	@echo "Compa Makefile"
	@echo ""
	@echo "Usage: make [target] [VARIABLE=value ...]"
	@echo ""
	@echo "Targets:"
	@grep -E '^## [a-zA-Z0-9_-]+: ' $(MAKEFILE_LIST) | sed 's/^## //' | \
		awk '{ name = $$1; sub(/:$$/, "", name); sub(/^[^ ]+ /, ""); printf "  %-26s %s\n", name, $$0 }'
	@echo ""
	@echo "Variables:"
	@echo "  INSTALL_PREFIX    where install puts bin/ (default: ~/.local)"
	@echo "  COMPA_HOME        Compa's data folder, deleted by uninstall-all (default: ~/.compa)"
	@echo "  CONFIRM=1         lets uninstall-all delete COMPA_HOME"
	@echo "  VERSION           version stamped into the programs (default: git describe, without the leading v)"
	@echo "  GO_BUILD_TAGS     Go build tags (default: goolm,stdjson)"
	@echo "  LOCAL_CACHE=1     keep Go's build and module caches in .cache/ in this checkout"
	@echo ""
	@echo "Current configuration:"
	@echo "  Platform: $(PLATFORM)/$(ARCH)"
	@echo "  Build directory: $(BUILD_DIR)"
	@echo "  Install prefix: $(INSTALL_PREFIX)"
	@echo "  Compa home: $(COMPA_HOME)"
