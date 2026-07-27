package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const (
	defaultCacheTTL       = 30 * time.Minute
	defaultRequestTimeout = 30 * time.Second
	defaultMaxConcurrency = 8
)

type lifecycleRequest struct {
	ConfigYAML json.RawMessage `json:"config_yaml"`
}

func defaultConfig() pluginConfig {
	return pluginConfig{
		CacheTTL:        defaultCacheTTL,
		RequestTimeout:  defaultRequestTimeout,
		MaxConcurrency:  defaultMaxConcurrency,
		IncludeDisabled: false,
	}
}

func decodeLifecycleConfig(raw []byte) (pluginConfig, error) {
	cfg := defaultConfig()
	if len(raw) == 0 {
		return cfg, nil
	}
	var req lifecycleRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return cfg, fmt.Errorf("decode lifecycle request: %w", err)
	}
	text, err := lifecycleConfigText(req.ConfigYAML)
	if err != nil {
		return cfg, err
	}
	values := yamlScalars(text)
	if value := values["cache-ttl"]; value != "" {
		parsed, err := time.ParseDuration(value)
		if err != nil || parsed < time.Minute {
			return cfg, fmt.Errorf("cache-ttl must be a Go duration >= 1m")
		}
		cfg.CacheTTL = parsed
	}
	if value := values["request-timeout"]; value != "" {
		parsed, err := time.ParseDuration(value)
		if err != nil || parsed < time.Second || parsed > 5*time.Minute {
			return cfg, fmt.Errorf("request-timeout must be between 1s and 5m")
		}
		cfg.RequestTimeout = parsed
	}
	if value := values["max-concurrency"]; value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 || parsed > 64 {
			return cfg, fmt.Errorf("max-concurrency must be between 1 and 64")
		}
		cfg.MaxConcurrency = parsed
	}
	if value := values["include-disabled"]; value != "" {
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			return cfg, fmt.Errorf("include-disabled must be boolean")
		}
		cfg.IncludeDisabled = parsed
	}
	return cfg, nil
}

func lifecycleConfigText(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", nil
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		if decoded, decodeErr := base64.StdEncoding.DecodeString(text); decodeErr == nil && strings.Contains(string(decoded), ":") {
			return string(decoded), nil
		}
		return text, nil
	}
	var bytes []byte
	if err := json.Unmarshal(raw, &bytes); err == nil {
		return string(bytes), nil
	}
	return "", fmt.Errorf("config_yaml must be a string or byte array")
}

func yamlScalars(raw string) map[string]string {
	values := make(map[string]string)
	for _, line := range strings.Split(raw, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") || !strings.Contains(trimmed, ":") {
			continue
		}
		parts := strings.SplitN(trimmed, ":", 2)
		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(strings.SplitN(parts[1], "#", 2)[0])
		value = strings.Trim(value, `"'`)
		if key != "" {
			values[key] = value
		}
	}
	return values
}
