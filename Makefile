.PHONY: build sync_diff_inspector test tidy clean

GO ?= go
BIN_DIR ?= bin
LDFLAGS := -X "github.com/pingcap/tidb-tools/pkg/utils.Version=$(shell git describe --tags --dirty --always)"
LDFLAGS += -X "github.com/pingcap/tidb-tools/pkg/utils.BuildTS=$(shell date -u '+%Y-%m-%d %H:%M:%S')"
LDFLAGS += -X "github.com/pingcap/tidb-tools/pkg/utils.GitHash=$(shell git rev-parse HEAD)"
LDFLAGS += -X "github.com/pingcap/tidb-tools/pkg/utils.GitBranch=$(shell git rev-parse --abbrev-ref HEAD)"

build: sync_diff_inspector

sync_diff_inspector:
	@mkdir -p $(BIN_DIR)
	$(GO) build -ldflags '$(LDFLAGS)' -o $(BIN_DIR)/sync_diff_inspector ./sync_diff_inspector

test:
	$(GO) test ./sync_diff_inspector/... ./pkg/dbutil ./pkg/filter ./pkg/table-filter ./pkg/table-rule-selector ./pkg/column-mapping ./pkg/utils ./pkg/schemacmp

tidy:
	$(GO) mod tidy

clean:
	$(GO) clean
