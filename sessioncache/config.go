package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
)

const defaultDatabasePath = "./data/cpa-session-cache.db"

type pluginConfig struct {
	DatabasePath string
}

type lifecycleRequest struct {
	ConfigYAML json.RawMessage `json:"config_yaml"`
}

func defaultConfig() pluginConfig {
	return pluginConfig{DatabasePath: defaultDatabasePath}
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
	if value := values["database-path"]; value != "" {
		cfg.DatabasePath = value
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
