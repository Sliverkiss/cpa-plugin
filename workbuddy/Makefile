PLUGIN_ID ?= workbuddy
# Prefer workbuddy/VERSION so local `make build` matches release/CI.
VERSION ?= $(shell tr -d ' \n\r' < VERSION 2>/dev/null)
ifeq ($(strip $(VERSION)),)
VERSION := 0.6.0
endif
GO ?= go
# Ensure go is on PATH for both parse-time $(shell) calls and recipes.
ifeq ($(shell command -v $(GO) 2>/dev/null),)
GO := /usr/local/go/bin/go
export PATH := /usr/local/go/bin:$(PATH)
endif
GOOS ?= $(shell $(GO) env GOOS)
GOARCH ?= $(shell $(GO) env GOARCH)
CC ?= $(shell $(GO) env CC)
LD_FLAGS := -s -w -X main.version=$(VERSION)

ifeq ($(GOOS),darwin)
    LIB_EXT := dylib
else ifeq ($(GOOS),windows)
    LIB_EXT := dll
else
    LIB_EXT := so
endif

DIST_DIR ?= dist
PLUGIN_BIN ?= $(DIST_DIR)/$(PLUGIN_ID).$(LIB_EXT)

build:
	mkdir -p $(dir $(PLUGIN_BIN))
	CGO_ENABLED=1 GOOS=$(GOOS) GOARCH=$(GOARCH) CC="$(CC)" \
		$(GO) build -trimpath -buildmode=c-shared -ldflags "$(LD_FLAGS)" -o $(PLUGIN_BIN) .
	rm -f $(basename $(PLUGIN_BIN)).h

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

check: test vet

clean:
	rm -rf $(DIST_DIR)

.PHONY: build test vet check clean
