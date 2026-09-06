PLUGIN_NAME ?= cpa-quota-api-extension
AUTH_PLUGIN_NAME ?= cpa-opencode-go-auth
AUTH_PLUGIN_DIR ?= ./authplugin
SESSION_PLUGIN_NAME ?= cpa-session-cache
SESSION_PLUGIN_DIR ?= ./sessioncache
DIST_DIR ?= dist
CPA_UPSTREAM ?= upstream/CLIProxyAPI
CPA_COMPAT_TAG ?= v7.2.151

.PHONY: fmt test build build-quota build-auth build-session verify-upstream clean

fmt:
	gofmt -w *.go $(AUTH_PLUGIN_DIR)/*.go $(SESSION_PLUGIN_DIR)/*.go

test:
	go test ./...

build: build-quota build-auth build-session

build-quota:
	mkdir -p $(DIST_DIR)
	CGO_ENABLED=1 go build -buildmode=c-shared -trimpath -ldflags='-s -w' -o $(DIST_DIR)/$(PLUGIN_NAME).so .
	rm -f $(DIST_DIR)/$(PLUGIN_NAME).h

build-auth:
	mkdir -p $(DIST_DIR)
	CGO_ENABLED=1 go build -buildmode=c-shared -trimpath -ldflags='-s -w' -o $(DIST_DIR)/$(AUTH_PLUGIN_NAME).so $(AUTH_PLUGIN_DIR)
	rm -f $(DIST_DIR)/$(AUTH_PLUGIN_NAME).h

build-session:
	mkdir -p $(DIST_DIR)
	CGO_ENABLED=1 go build -buildmode=c-shared -trimpath -ldflags='-s -w' -o $(DIST_DIR)/$(SESSION_PLUGIN_NAME).so $(SESSION_PLUGIN_DIR)
	rm -f $(DIST_DIR)/$(SESSION_PLUGIN_NAME).h

verify-upstream:
	test -f $(CPA_UPSTREAM)/sdk/pluginabi/types.go
	test "$$(git -C $(CPA_UPSTREAM) rev-parse HEAD)" = "$$(git -C $(CPA_UPSTREAM) rev-list -n 1 $(CPA_COMPAT_TAG))"
	grep -q 'MethodHostAuthList' $(CPA_UPSTREAM)/sdk/pluginabi/types.go
	grep -q 'MethodHostAuthGet' $(CPA_UPSTREAM)/sdk/pluginabi/types.go
	grep -q 'MethodHostAuthGetRuntime' $(CPA_UPSTREAM)/sdk/pluginabi/types.go
	grep -q 'MethodUsageHandle' $(CPA_UPSTREAM)/sdk/pluginabi/types.go
	grep -q 'MethodHostHTTPDo' $(CPA_UPSTREAM)/sdk/pluginabi/types.go
	grep -q 'MethodManagementRegister' $(CPA_UPSTREAM)/sdk/pluginabi/types.go
	grep -q 'Resources \[\]ResourceRoute' $(CPA_UPSTREAM)/sdk/pluginapi/types.go
	grep -q 'ResourceBasePath string' $(CPA_UPSTREAM)/sdk/pluginapi/types.go
	grep -q 'MethodAuthIdentifier' $(CPA_UPSTREAM)/sdk/pluginabi/types.go
	grep -q 'MethodAuthParse' $(CPA_UPSTREAM)/sdk/pluginabi/types.go
	grep -q 'MethodModelRegister' $(CPA_UPSTREAM)/sdk/pluginabi/types.go
	grep -q 'MethodModelStatic' $(CPA_UPSTREAM)/sdk/pluginabi/types.go
	grep -q 'MethodModelForAuth' $(CPA_UPSTREAM)/sdk/pluginabi/types.go
	grep -q 'type AuthModelRequest struct' $(CPA_UPSTREAM)/sdk/pluginapi/types.go
	grep -q 'type ModelResponse struct' $(CPA_UPSTREAM)/sdk/pluginapi/types.go
	grep -q 'host_callback_id' $(CPA_UPSTREAM)/internal/pluginhost/rpc_schema.go
	grep -q 'MethodHostHTTPDo' $(CPA_UPSTREAM)/internal/pluginhost/host_callbacks.go
	grep -q 'type AuthParseResponse struct' $(CPA_UPSTREAM)/sdk/pluginapi/types.go
	grep -q 'type ModelRegistrationResponse struct' $(CPA_UPSTREAM)/sdk/pluginapi/types.go
	grep -q 'MethodExecutorIdentifier' $(CPA_UPSTREAM)/sdk/pluginabi/types.go
	grep -q 'MethodExecutorExecuteStream' $(CPA_UPSTREAM)/sdk/pluginabi/types.go
	grep -q 'MethodHostHTTPDoStream' $(CPA_UPSTREAM)/sdk/pluginabi/types.go
	grep -q 'MethodHostHTTPStreamRead' $(CPA_UPSTREAM)/sdk/pluginabi/types.go
	grep -q 'MethodHostStreamEmit' $(CPA_UPSTREAM)/sdk/pluginabi/types.go
	grep -q 'Executor  *bool  *`json:"executor"`' $(CPA_UPSTREAM)/internal/pluginhost/rpc_schema.go
	grep -q 'ExecutorInputFormats' $(CPA_UPSTREAM)/internal/pluginhost/rpc_schema.go
	grep -q 'stream_id' $(CPA_UPSTREAM)/internal/pluginhost/rpc_schema.go
	grep -q 'func executorKeyFromAuth' $(CPA_UPSTREAM)/sdk/cliproxy/auth/conductor_execution.go

clean:
	rm -rf $(DIST_DIR)
