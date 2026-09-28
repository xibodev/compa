.PHONY: all generate build kernel shell product build-whatsapp-native build-linux-arm build-linux-arm64 \
	build-linux-mipsle build-android-arm64 build-shell-android-arm64 build-android-bundle build-pi-zero \
	build-all install uninstall uninstall-all clean vet test integration-test fmt lint-docs lint fix deps \
	update-deps check run build-macos-app help

# Build variables
# THE TWO SHIPPABLE PRODUCTS. compa is the FULL thing (the shell the user
# launches); compa-kernel is that minus the web shell -- a complete
# agentic harness that runs on its own.
SHELL_NAME=compa
KERNEL_NAME=compa-kernel
BUILD_DIR=build
CMD_DIR=cmd/$(KERNEL_NAME)
MAIN_GO=$(CMD_DIR)/main.go
EXT=

ifeq ($(OS),Windows_NT)
	POWERSHELL=powershell -NoProfile -Command
	WINDOWS_GOARCH_RAW:=$(strip $(shell go env GOARCH 2>NUL))
endif

# Version. Computed once per make run, so every binary one run builds --
# `make product` builds both -- carries the same version, commit and build
# time. The leading "v" of a tag is dropped: release builds stamp v1.2.3 as
# 1.2.3, and a local build of that tag says the same.
ifeq ($(OS),Windows_NT)
	VERSION_RAW:=$(patsubst v%,%,$(strip $(shell git describe --tags --always --dirty 2>NUL)))
	GIT_COMMIT_RAW:=$(strip $(shell git rev-parse --short=8 HEAD 2>NUL))
	BUILD_TIME_RAW:=$(strip $(shell powershell -NoProfile -Command "Get-Date -Format 'yyyy-MM-ddTHH:mm:ssK'"))
	GO_VERSION_RAW:=$(strip $(shell go env GOVERSION 2>NUL))
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
CONFIG_PKG=github.com/xibodev/compa/pkg/config
LDFLAGS=-X $(CONFIG_PKG).Version=$(VERSION) -X $(CONFIG_PKG).GitCommit=$(GIT_COMMIT) -X $(CONFIG_PKG).BuildTime=$(BUILD_TIME) -X $(CONFIG_PKG).GoVersion=$(GO_VERSION) -s -w

# Go variables
GO?=go
WEB_GO?=$(GO)
CGO_ENABLED?=0
GO_BUILD_TAGS?=goolm,stdjson
GOFLAGS?=-v -tags $(GO_BUILD_TAGS)
GOCACHE?=$(CURDIR)/.cache/go-build
GOMODCACHE?=$(CURDIR)/.cache/go-mod
GOTOOLCHAIN?=local
export CGO_ENABLED
export GOCACHE
export GOMODCACHE
export GOTOOLCHAIN
comma:=,
empty:=
space:=$(empty) $(empty)
GO_BUILD_TAGS_NO_GOOLM:=$(subst $(space),$(comma),$(strip $(filter-out goolm,$(subst $(comma),$(space),$(GO_BUILD_TAGS)))))
GOFLAGS_NO_GOOLM?=-v -tags $(GO_BUILD_TAGS_NO_GOOLM)

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
		printf '\004\024\000\160' | dd of=$(1) bs=1 seek=36 count=4 conv=notrunc 2>/dev/null || \
		{ echo "Error: failed to patch MIPS e_flags for $(1)"; exit 1; }; \
	else \
		echo "Error: $(1) not found, cannot patch MIPS e_flags"; exit 1; \
	fi
endef

# Patch creack/pty for loong64 support (upstream doesn't have ztypes_loong64.go)
PTY_PATCH_LOONG64=pty_dir=$$(go env GOMODCACHE)/github.com/creack/pty@v1.1.9; \
	if [ -d "$$pty_dir" ] && [ ! -f "$$pty_dir/ztypes_loong64.go" ]; then \
		chmod +w "$$pty_dir" 2>/dev/null || true; \
		printf '//go:build linux && loong64\npackage pty\ntype (_C_int int32; _C_uint uint32)\n' > "$$pty_dir/ztypes_loong64.go"; \
	fi

# Golangci-lint
GOLANGCI_LINT?=golangci-lint

# Installation
INSTALL_PREFIX?=$(HOME)/.local
INSTALL_BIN_DIR=$(INSTALL_PREFIX)/bin
INSTALL_MAN_DIR=$(INSTALL_PREFIX)/share/man/man1
INSTALL_TMP_SUFFIX=.new

# Workspace and Skills
COMPA_HOME?=$(HOME)/.compa
WORKSPACE_DIR?=$(COMPA_HOME)/workspace
WORKSPACE_SKILLS_DIR=$(WORKSPACE_DIR)/skills
BUILTIN_SKILLS_DIR=$(CURDIR)/skills

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

# Default target
all: build

## generate: Run generate
generate:
	@echo "Run generate..."
ifeq ($(OS),Windows_NT)
	@$(POWERSHELL) "if (Test-Path -LiteralPath './$(CMD_DIR)/workspace') { Remove-Item -LiteralPath './$(CMD_DIR)/workspace' -Recurse -Force }"
	@$(POWERSHELL) "$$env:GOOS=''; $$env:GOARCH=''; $(GO) generate ./..."
else
	@rm -r ./$(CMD_DIR)/workspace 2>/dev/null || true
	@GOOS=$$($(GO) env GOHOSTOS) GOARCH=$$($(GO) env GOHOSTARCH) $(GO) generate ./...
endif
	@echo "Run generate complete"

## kernel: Build compa-kernel -- the harness, with no web shell
##
## This is the full product MINUS the browser: agent loop, message bus, every
## channel transport, tools, skills, hooks, sessions, the module host, the CLI
## and the TUI. It runs headless, as a TUI, or as a gateway serving channels.
##
## It is the SAME binary the full product ships beside the shell, under the
## same name, so a standalone install and a bundled one cannot drift.
kernel: generate
	@echo "Building $(KERNEL_NAME)$(EXT) for $(PLATFORM)/$(ARCH)..."
ifeq ($(OS),Windows_NT)
	@$(POWERSHELL) "New-Item -ItemType Directory -Force -Path '$(BUILD_DIR)' | Out-Null"
	@$(GO) build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(KERNEL_NAME)$(EXT) ./$(CMD_DIR)
else
	@mkdir -p $(BUILD_DIR)
	@GOOS=$(PLATFORM) GOARCH=$(ARCH) $(GO) build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(KERNEL_NAME)$(EXT) ./$(CMD_DIR)
endif
	@echo "Built $(BUILD_DIR)/$(KERNEL_NAME)$(EXT) -- runs standalone, no shell required"

## shell: Build the web shell, with the UI compiled in
##
## THE FRONTEND BUILD IS NOT OPTIONAL and is why this is one target rather than
## two. The UI compiles into web/backend/dist and is embedded at LINK time, so
## building the Go binary without that step produces something that starts,
## serves, and looks fine while shipping a stale interface. Nothing reports it.
## The frontend uses pnpm (the version packageManager pins in package.json);
## run `pnpm install` in web/frontend once first.
shell:
	@echo "Building the web UI..."
	@cd web/frontend && pnpm run build:backend
	@echo "Linking the shell with the UI embedded..."
ifeq ($(OS),Windows_NT)
	@$(POWERSHELL) "New-Item -ItemType Directory -Force -Path '$(BUILD_DIR)' | Out-Null"
	@cd web/backend && $(GO) build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o ../../$(BUILD_DIR)/$(SHELL_NAME)$(EXT) .
else
	@mkdir -p $(BUILD_DIR)
	@cd web/backend && GOOS=$(PLATFORM) GOARCH=$(ARCH) $(WEB_GO) build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o ../../$(BUILD_DIR)/$(SHELL_NAME)$(EXT) .
endif
	@echo "Built $(BUILD_DIR)/$(SHELL_NAME)$(EXT) -- needs a kernel beside it"

## product: Build the full product -- shell plus the kernel it supervises
##
## Both binaries in one directory, which is the coupling: the shell locates the
## kernel BESIDE ITS OWN EXECUTABLE. Ship one without the other and the shell
## starts, serves a login page, and reports that the harness exited -- exactly
## what a crash looks like from the outside. One make run computes LDFLAGS
## once, so both carry the same version (override with VERSION=1.2.3).
product: kernel shell
	@echo ""
	@echo "Full product $(VERSION) in $(BUILD_DIR)/:"
	@echo "  $(SHELL_NAME)$(EXT)   <- the user launches this"
	@echo "  $(KERNEL_NAME)$(EXT)  <- it supervises this"

## build: Build compa-kernel for the current platform (platform-suffixed, plus a plain copy)
build: generate
	@echo "Building $(KERNEL_NAME)$(EXT) for $(PLATFORM)/$(ARCH)..."
ifeq ($(OS),Windows_NT)
	@$(POWERSHELL) "New-Item -ItemType Directory -Force -Path '$(BUILD_DIR)' | Out-Null"
	@$(GO) build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(BINARY_PATH)$(EXT) ./$(CMD_DIR)
	@$(POWERSHELL) "Copy-Item -LiteralPath '$(BINARY_PATH)$(EXT)' -Destination '$(BUILD_DIR)/$(KERNEL_NAME)$(EXT)' -Force"
else
	@mkdir -p $(BUILD_DIR)
	@GOOS=$(PLATFORM) GOARCH=$(ARCH) $(GO) build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(BINARY_PATH)$(EXT) ./$(CMD_DIR)
	@echo "Build complete: $(BINARY_PATH)$(EXT)"
	@$(LNCMD) $(KERNEL_NAME)-$(PLATFORM)-$(ARCH)$(EXT) $(BUILD_DIR)/$(KERNEL_NAME)$(EXT)
endif
	@echo "Build complete: $(BUILD_DIR)/$(KERNEL_NAME)$(EXT)"

## build-whatsapp-native: Build with WhatsApp native (whatsmeow) support; larger binary
build-whatsapp-native: generate
## @echo "Building $(KERNEL_NAME) with WhatsApp native for $(PLATFORM)/$(ARCH)..."
	@echo "Building for multiple platforms..."
	@mkdir -p $(BUILD_DIR)
	GOOS=linux GOARCH=amd64 $(GO) build -tags $(GO_BUILD_TAGS),whatsapp_native -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(KERNEL_NAME)-linux-amd64 ./$(CMD_DIR)
	GOOS=linux GOARCH=arm GOARM=7 $(GO) build -tags $(GO_BUILD_TAGS),whatsapp_native -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(KERNEL_NAME)-linux-arm ./$(CMD_DIR)
	GOOS=linux GOARCH=arm64 $(GO) build -tags $(GO_BUILD_TAGS),whatsapp_native -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(KERNEL_NAME)-linux-arm64 ./$(CMD_DIR)
	GOOS=linux GOARCH=loong64 $(GO) build -tags $(GO_BUILD_TAGS),whatsapp_native -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(KERNEL_NAME)-linux-loong64 ./$(CMD_DIR)
	GOOS=linux GOARCH=riscv64 $(GO) build -tags $(GO_BUILD_TAGS),whatsapp_native -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(KERNEL_NAME)-linux-riscv64 ./$(CMD_DIR)
	GOOS=linux GOARCH=mipsle GOMIPS=softfloat $(GO) build -tags $(GO_BUILD_TAGS_NO_GOOLM),whatsapp_native -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(KERNEL_NAME)-linux-mipsle ./$(CMD_DIR)
	$(call PATCH_MIPS_FLAGS,$(BUILD_DIR)/$(KERNEL_NAME)-linux-mipsle)
	GOOS=darwin GOARCH=arm64 $(GO) build -tags $(GO_BUILD_TAGS),whatsapp_native -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(KERNEL_NAME)-darwin-arm64 ./$(CMD_DIR)
	GOOS=windows GOARCH=amd64 $(GO) build -tags $(GO_BUILD_TAGS),whatsapp_native -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(KERNEL_NAME)-windows-amd64.exe ./$(CMD_DIR)
## @$(GO) build $(GOFLAGS) -tags whatsapp_native -ldflags "$(LDFLAGS)" -o $(BINARY_PATH) ./$(CMD_DIR)
	@echo "Build complete"
##	@ln -sf $(KERNEL_NAME)-$(PLATFORM)-$(ARCH) $(BUILD_DIR)/$(KERNEL_NAME)

## build-linux-arm: Build for Linux ARMv7 (e.g. Raspberry Pi Zero 2 W 32-bit)
build-linux-arm: generate
	@echo "Building for linux/arm (GOARM=7)..."
	@mkdir -p $(BUILD_DIR)
	GOOS=linux GOARCH=arm GOARM=7 $(GO) build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(KERNEL_NAME)-linux-arm ./$(CMD_DIR)
	@echo "Build complete: $(BUILD_DIR)/$(KERNEL_NAME)-linux-arm"

## build-linux-arm64: Build for Linux ARM64 (e.g. Raspberry Pi Zero 2 W 64-bit)
build-linux-arm64: generate
	@echo "Building for linux/arm64..."
	@mkdir -p $(BUILD_DIR)
	GOOS=linux GOARCH=arm64 $(GO) build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(KERNEL_NAME)-linux-arm64 ./$(CMD_DIR)
	@echo "Build complete: $(BUILD_DIR)/$(KERNEL_NAME)-linux-arm64"

## build-linux-mipsle: Build for Linux MIPS32 LE
build-linux-mipsle: generate
	@echo "Building for linux/mipsle (softfloat)..."
	@mkdir -p $(BUILD_DIR)
	GOOS=linux GOARCH=mipsle GOMIPS=softfloat $(GO) build $(GOFLAGS_NO_GOOLM) -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(KERNEL_NAME)-linux-mipsle ./$(CMD_DIR)
	$(call PATCH_MIPS_FLAGS,$(BUILD_DIR)/$(KERNEL_NAME)-linux-mipsle)
	@echo "Build complete: $(BUILD_DIR)/$(KERNEL_NAME)-linux-mipsle"

## build-android-arm64: Build core for Android ARM64
build-android-arm64: generate
	@echo "Building for android/arm64..."
	@mkdir -p $(BUILD_DIR)
	GOOS=android GOARCH=arm64 $(GO) build -tags stdjson -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(KERNEL_NAME)-android-arm64 ./$(CMD_DIR)
	@echo "Build complete: $(BUILD_DIR)/$(KERNEL_NAME)-android-arm64"

## build-shell-android-arm64: Build the web shell for Android ARM64, with the UI compiled in
build-shell-android-arm64:
	@echo "Building the web UI..."
	@cd web/frontend && pnpm run build:backend
	@echo "Building $(SHELL_NAME) for android/arm64..."
	@mkdir -p $(BUILD_DIR)
	@cd web/backend && GOOS=android GOARCH=arm64 $(GO) build -tags stdjson -ldflags "$(LDFLAGS)" -o ../../$(BUILD_DIR)/$(SHELL_NAME)-android-arm64 .
	@echo "Build complete: $(BUILD_DIR)/$(SHELL_NAME)-android-arm64"

## build-android-bundle: Build the kernel and the shell for Android and package them as a universal zip
build-android-bundle: generate
	@echo "Building the kernel for all Android architectures..."
	@mkdir -p $(BUILD_DIR)
	GOOS=android GOARCH=arm64 $(GO) build -tags stdjson -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(KERNEL_NAME)-android-arm64 ./$(CMD_DIR)
	@echo "Building the shell for Android arm64..."
	@$(MAKE) build-shell-android-arm64
	@echo "Staging JNI libs..."
	@rm -rf $(BUILD_DIR)/android-staging
	@mkdir -p $(BUILD_DIR)/android-staging/arm64-v8a
	@cp $(BUILD_DIR)/$(KERNEL_NAME)-android-arm64 $(BUILD_DIR)/android-staging/arm64-v8a/lib$(KERNEL_NAME).so
	@cp $(BUILD_DIR)/$(SHELL_NAME)-android-arm64 $(BUILD_DIR)/android-staging/arm64-v8a/lib$(SHELL_NAME).so
	@cd $(BUILD_DIR)/android-staging && zip -r ../$(SHELL_NAME)-android-universal.zip .
	@rm -rf $(BUILD_DIR)/android-staging
	@echo "All Android builds complete: $(BUILD_DIR)/$(SHELL_NAME)-android-universal.zip"

## build-pi-zero: Build for Raspberry Pi Zero 2 W (32-bit and 64-bit)
build-pi-zero: build-linux-arm build-linux-arm64
	@echo "Pi Zero 2 W builds: $(BUILD_DIR)/$(KERNEL_NAME)-linux-arm (32-bit), $(BUILD_DIR)/$(KERNEL_NAME)-linux-arm64 (64-bit)"

## build-all: Build compa-kernel for all Makefile-managed platforms
build-all: generate
	@echo "Building for multiple platforms..."
	@mkdir -p $(BUILD_DIR)
	GOOS=linux GOARCH=amd64 $(GO) build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(KERNEL_NAME)-linux-amd64 ./$(CMD_DIR)
	GOOS=linux GOARCH=arm GOARM=7 $(GO) build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(KERNEL_NAME)-linux-arm ./$(CMD_DIR)
	GOOS=linux GOARCH=arm64 $(GO) build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(KERNEL_NAME)-linux-arm64 ./$(CMD_DIR)
	@$(PTY_PATCH_LOONG64)
	GOOS=linux GOARCH=loong64 $(GO) build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(KERNEL_NAME)-linux-loong64 ./$(CMD_DIR)
	GOOS=linux GOARCH=riscv64 $(GO) build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(KERNEL_NAME)-linux-riscv64 ./$(CMD_DIR)
	GOOS=linux GOARCH=mipsle GOMIPS=softfloat $(GO) build $(GOFLAGS_NO_GOOLM) -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(KERNEL_NAME)-linux-mipsle ./$(CMD_DIR)
	$(call PATCH_MIPS_FLAGS,$(BUILD_DIR)/$(KERNEL_NAME)-linux-mipsle)
	GOOS=linux GOARCH=arm GOARM=7 $(GO) build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(KERNEL_NAME)-linux-armv7 ./$(CMD_DIR)
	GOOS=darwin GOARCH=arm64 $(GO) build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(KERNEL_NAME)-darwin-arm64 ./$(CMD_DIR)
	GOOS=windows GOARCH=amd64 $(GO) build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(KERNEL_NAME)-windows-amd64.exe ./$(CMD_DIR)
	GOOS=netbsd GOARCH=amd64 $(GO) build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(KERNEL_NAME)-netbsd-amd64 ./$(CMD_DIR)
	GOOS=netbsd GOARCH=arm64 $(GO) build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(KERNEL_NAME)-netbsd-arm64 ./$(CMD_DIR)
	@echo "Core builds complete"

## install: Install compa-kernel to the system
install: build
	@echo "Installing $(KERNEL_NAME)..."
	@mkdir -p $(INSTALL_BIN_DIR)
	# Copy binary with temporary suffix to ensure atomic update
	@cp $(BUILD_DIR)/$(KERNEL_NAME) $(INSTALL_BIN_DIR)/$(KERNEL_NAME)$(INSTALL_TMP_SUFFIX)
	@chmod +x $(INSTALL_BIN_DIR)/$(KERNEL_NAME)$(INSTALL_TMP_SUFFIX)
	@mv -f $(INSTALL_BIN_DIR)/$(KERNEL_NAME)$(INSTALL_TMP_SUFFIX) $(INSTALL_BIN_DIR)/$(KERNEL_NAME)
	@echo "Installed binary to $(INSTALL_BIN_DIR)/$(KERNEL_NAME)"
	@echo "Installation complete!"

## uninstall: Remove compa-kernel from the system
uninstall:
	@echo "Uninstalling $(KERNEL_NAME)..."
	@rm -f $(INSTALL_BIN_DIR)/$(KERNEL_NAME)
	@echo "Removed binary from $(INSTALL_BIN_DIR)/$(KERNEL_NAME)"
	@echo "Note: Only the executable file has been deleted."
	@echo "If you need to delete all configurations (config.json, workspace, etc.), run 'make uninstall-all'"

## uninstall-all: Remove compa-kernel and all Compa data
uninstall-all:
	@echo "Removing workspace and skills..."
	@rm -rf $(COMPA_HOME)
	@echo "Removed workspace: $(COMPA_HOME)"
	@echo "Complete uninstallation done!"

## clean: Remove build artifacts
clean:
	@echo "Cleaning build artifacts..."
ifeq ($(OS),Windows_NT)
	@$(POWERSHELL) "if (Test-Path -LiteralPath '$(BUILD_DIR)') { Remove-Item -LiteralPath '$(BUILD_DIR)' -Recurse -Force }"
else
	@rm -rf $(BUILD_DIR)
endif
	@echo "Clean complete"

## vet: Run go vet for static analysis
vet: generate
	@packages="$$($(GO) list $(GOFLAGS) ./...)" && \
		$(GO) vet $(GOFLAGS) $$(printf '%s\n' "$$packages" | grep -v '^github.com/xibodev/compa/web/')
	@cd web/backend && $(WEB_GO) vet ./...

## test: Test Go code and the web frontend
test: generate
	@$(GO) test $(GOFLAGS) $$($(GO) list $(GOFLAGS) ./... | grep -v github.com/xibodev/compa/web/)
	@cd web/backend && $(WEB_GO) test $(GOFLAGS) ./...
	@cd web/frontend && pnpm test

## integration-test: Run Docker-backed integration test suites
integration-test:
	@bash ./scripts/run-integration-tests.sh

## fmt: Format Go code
fmt:
	@$(GOLANGCI_LINT) fmt

## lint-docs: Check common documentation layout and naming conventions
lint-docs:
	@./scripts/lint-docs.sh

## lint: Run linters
lint:
	@$(GOLANGCI_LINT) run --build-tags $(GO_BUILD_TAGS)
	@./scripts/lint-docs.sh

## fix: Fix linting issues
fix:
	@$(GOLANGCI_LINT) run --fix --build-tags $(GO_BUILD_TAGS)

## deps: Download dependencies
deps:
	@$(GO) mod download
	@$(GO) mod verify

## update-deps: Update dependencies
update-deps:
	@$(GO) get -u ./...
	@$(GO) mod tidy

## check: Run deps, fmt, vet, tests, and docs consistency checks
check: deps fmt vet test lint-docs

## run: Build and run compa-kernel
run: build
	@$(BUILD_DIR)/$(KERNEL_NAME) $(ARGS)

## build-macos-app: Build Compa macOS .app bundle (no terminal window)
build-macos-app: product
	@echo "Building macOS .app bundle..."
	@if [ "$(UNAME_S)" != "Darwin" ]; then \
		echo "Error: This target is only available on macOS"; \
		exit 1; \
	fi
	@./scripts/build-macos-app.sh
	@echo "macOS .app bundle created: $(BUILD_DIR)/Compa.app"

## help: Show this help message
help:
	@echo "Compa Makefile"
	@echo ""
	@echo "Usage:"
	@echo "  make [target]"
	@echo ""
	@echo "Targets:"
	@grep -E '^## ' $(MAKEFILE_LIST) | sort | awk -F': ' '{printf "  %-16s %s\n", substr($$1, 4), $$2}'
	@echo ""
	@echo "Examples:"
	@echo "  make product            # Build compa and compa-kernel into $(BUILD_DIR)/"
	@echo "  make build              # Build compa-kernel for the current platform"
	@echo "  make install            # Install compa-kernel to ~/.local/bin"
	@echo "  make uninstall          # Remove compa-kernel from ~/.local/bin"
	@echo ""
	@echo "Environment Variables:"
	@echo "  INSTALL_PREFIX          # Installation prefix (default: ~/.local)"
	@echo "  WORKSPACE_DIR           # Workspace directory (default: ~/.compa/workspace)"
	@echo "  VERSION                 # Version string (default: git describe, without the leading v)"
	@echo ""
	@echo "Current Configuration:"
	@echo "  Platform: $(PLATFORM)/$(ARCH)"
	@echo "  Binary: $(BINARY_PATH)"
	@echo "  Install Prefix: $(INSTALL_PREFIX)"
	@echo "  Workspace: $(WORKSPACE_DIR)"
