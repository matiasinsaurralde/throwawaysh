UNAME_S := $(shell uname -s)

SERVICE_BIN ?= ./throwawaysh
SERVICE_CMD ?= ./cmd/throwawaysh
ENTITLEMENTS ?= ./cmd/throwawaysh/entitlements.plist

BUILD_DIR ?= ./.build
AGENT_BIN ?= throwawaysh-guest-agent
AGENT_CMD ?= ./cmd/guest-agent
AGENT_GOOS ?= linux
AGENT_GOARCH ?= arm64
AGENT_CGO_ENABLED ?= 0
AGENT_BUILD_OUTPUT ?= $(BUILD_DIR)/$(AGENT_BIN)-$(AGENT_GOOS)-$(AGENT_GOARCH)
ROOTFS ?= ./rootfs
AGENT_INSTALL_PATH ?= $(ROOTFS)/usr/local/bin/$(AGENT_BIN)

.PHONY: build build-service build-agent install-agent lint test clean

build: build-service install-agent

build-service:
	@echo "==> building service binary: $(SERVICE_BIN)"
	go build -o $(SERVICE_BIN) $(SERVICE_CMD)
ifeq ($(UNAME_S),Darwin)
	@echo "==> codesigning service binary (Darwin)"
	codesign --entitlements $(ENTITLEMENTS) --force -s - $(SERVICE_BIN)
else
	@echo "==> skipping codesign on $(UNAME_S)"
endif

build-agent:
	@echo "==> building guest agent: $(AGENT_BUILD_OUTPUT)"
	mkdir -p $(BUILD_DIR)
	CGO_ENABLED=$(AGENT_CGO_ENABLED) GOOS=$(AGENT_GOOS) GOARCH=$(AGENT_GOARCH) \
		go build -o $(AGENT_BUILD_OUTPUT) $(AGENT_CMD)

install-agent: build-agent
	@echo "==> installing guest agent to: $(AGENT_INSTALL_PATH)"
	mkdir -p $(dir $(AGENT_INSTALL_PATH))
	cp $(AGENT_BUILD_OUTPUT) $(AGENT_INSTALL_PATH)
	chmod +x $(AGENT_INSTALL_PATH)

lint:
	@echo "==> running golangci-lint"
	go run github.com/golangci/golangci-lint/cmd/golangci-lint@latest run

test:
	@echo "==> running go test"
	go test ./...

clean:
	@echo "==> cleaning build artifacts"
	rm -rf $(BUILD_DIR)
