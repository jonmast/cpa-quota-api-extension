package main

/*
#include <stdint.h>
#include <stdlib.h>

typedef struct {
	void* ptr;
	size_t len;
} cliproxy_buffer;

typedef int (*cliproxy_host_call_fn)(void*, const char*, const uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_host_free_fn)(void*, size_t);

typedef struct {
	uint32_t abi_version;
	void* host_ctx;
	cliproxy_host_call_fn call;
	cliproxy_host_free_fn free_buffer;
} cliproxy_host_api;

static const cliproxy_host_api* auth_stored_host;

static int auth_host_api_valid(const cliproxy_host_api* host, uint32_t abi_version) {
	return host != NULL && host->abi_version == abi_version && host->call != NULL && host->free_buffer != NULL;
}

static void auth_store_host_api(const cliproxy_host_api* host) {
	auth_stored_host = host;
}

static int auth_call_host_api(const char* method, const uint8_t* request, size_t request_len, cliproxy_buffer* response) {
	if (auth_stored_host == NULL || auth_stored_host->call == NULL) {
		return 1;
	}
	return auth_stored_host->call(auth_stored_host->host_ctx, method, request, request_len, response);
}

static void auth_free_host_buffer(void* ptr, size_t len) {
	if (auth_stored_host != NULL && auth_stored_host->free_buffer != NULL && ptr != NULL) {
		auth_stored_host->free_buffer(ptr, len);
	}
}
*/
import "C"

import (
	"encoding/json"
	"fmt"
	"unsafe"
)

// hostClient is the subset of host callbacks this plugin uses. It is an
// interface so tests can substitute a fake without crossing the cgo boundary.
type hostClient interface {
	doHTTP(hostHTTPRequest) (hostHTTPResponse, error)
	// invoke issues an arbitrary host callback. The executor needs the stream
	// callbacks (host.http.do_stream, host.stream.emit, ...), which all share
	// this one envelope shape, so they are not each given a method here.
	invoke(method string, payload any) (json.RawMessage, error)
	log(level, message string, fields map[string]any)
}

type cgoHostClient struct{}

var (
	// activeHost is nil until cliproxy_plugin_init installs the host API.
	// Discovery treats a nil host as a fetch failure rather than panicking,
	// which keeps unit tests and any non-ABI load path safe.
	activeHost hostClient
)

func installHost(raw unsafe.Pointer) bool {
	host := (*C.cliproxy_host_api)(raw)
	if C.auth_host_api_valid(host, C.uint32_t(abiVersion)) == 0 {
		return false
	}
	C.auth_store_host_api(host)
	activeHost = cgoHostClient{}
	return true
}

// doHTTP issues an HTTP request through the host rather than net/http. The
// host routes it via NewProxyAwareHTTPClient, so CPA's configured proxy is
// honoured; a direct net/http call from this .so would bypass it.
func (cgoHostClient) doHTTP(request hostHTTPRequest) (hostHTTPResponse, error) {
	result, err := callHost(methodHostHTTPDo, request)
	if err != nil {
		return hostHTTPResponse{}, err
	}
	var response hostHTTPResponse
	if err := json.Unmarshal(result, &response); err != nil {
		return hostHTTPResponse{}, fmt.Errorf("decode host HTTP response: %w", err)
	}
	return response, nil
}

func (cgoHostClient) invoke(method string, payload any) (json.RawMessage, error) {
	return callHost(method, payload)
}

func (cgoHostClient) log(level, message string, fields map[string]any) {
	_, _ = callHost(methodHostLog, hostLogRequest{Level: level, Message: message, Fields: fields})
}

func callHost(method string, payload any) (json.RawMessage, error) {
	rawPayload, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal host callback %s: %w", method, err)
	}
	cMethod := C.CString(method)
	defer C.free(unsafe.Pointer(cMethod))

	var response C.cliproxy_buffer
	var requestPtr *C.uint8_t
	if len(rawPayload) > 0 {
		allocated := C.CBytes(rawPayload)
		if allocated == nil {
			return nil, fmt.Errorf("allocate host callback %s", method)
		}
		defer C.free(allocated)
		requestPtr = (*C.uint8_t)(allocated)
	}
	callCode := C.auth_call_host_api(cMethod, requestPtr, C.size_t(len(rawPayload)), &response)
	if response.ptr != nil {
		defer C.auth_free_host_buffer(response.ptr, response.len)
	}
	if callCode != 0 {
		return nil, fmt.Errorf("host callback %s returned code %d", method, int(callCode))
	}
	if response.ptr == nil || response.len == 0 {
		return nil, fmt.Errorf("host callback %s returned no response", method)
	}
	if uint64(response.len) > uint64(^uint32(0)>>1) {
		return nil, fmt.Errorf("host callback %s response is too large", method)
	}
	rawResponse := C.GoBytes(response.ptr, C.int(response.len))
	var env envelope
	if err := json.Unmarshal(rawResponse, &env); err != nil {
		return nil, fmt.Errorf("decode host callback envelope %s: %w", method, err)
	}
	if !env.OK {
		if env.Error != nil {
			return nil, fmt.Errorf("%s: %s", env.Error.Code, env.Error.Message)
		}
		return nil, fmt.Errorf("host callback %s failed", method)
	}
	return env.Result, nil
}
