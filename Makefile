PLUGIN_NAME ?= cpa-quota-extension
DIST_DIR ?= dist
CPA_UPSTREAM ?= upstream/CLIProxyAPI
CPA_COMPAT_TAG ?= v7.2.61

.PHONY: fmt test build verify-upstream clean

fmt:
	gofmt -w *.go

test:
	go test ./...

build:
	mkdir -p $(DIST_DIR)
	CGO_ENABLED=1 go build -buildmode=c-shared -trimpath -ldflags='-s -w' -o $(DIST_DIR)/$(PLUGIN_NAME).so .
	rm -f $(DIST_DIR)/$(PLUGIN_NAME).h

verify-upstream:
	test -f $(CPA_UPSTREAM)/sdk/pluginabi/types.go
	test "$$(git -C $(CPA_UPSTREAM) rev-parse HEAD)" = "$$(git -C $(CPA_UPSTREAM) rev-list -n 1 $(CPA_COMPAT_TAG))"
	grep -q 'MethodHostAuthList' $(CPA_UPSTREAM)/sdk/pluginabi/types.go
	grep -q 'MethodHostAuthGet' $(CPA_UPSTREAM)/sdk/pluginabi/types.go
	grep -q 'MethodHostHTTPDo' $(CPA_UPSTREAM)/sdk/pluginabi/types.go
	grep -q 'MethodManagementRegister' $(CPA_UPSTREAM)/sdk/pluginabi/types.go

clean:
	rm -rf $(DIST_DIR)
