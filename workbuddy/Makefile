PLUGIN_ID ?= workbuddy
VERSION ?= 0.3.10
GO ?= go
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

clean:
	rm -rf $(DIST_DIR)

.PHONY: build clean
