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
	defaultDatabasePath   = "./data/cpa-session-cache.db"
	defaultStreamStateTTL = 10 * time.Minute
	// Retention defaults are sized for weeks of single-user traffic: 30 days
	// of rows, capped at 50k requests.
	defaultRetention = 30 * 24 * time.Hour
	defaultMaxRows   = 50000
)

type pluginConfig struct {
	DatabasePath   string
	Retention      time.Duration
	MaxRows        int
	StreamStateTTL time.Duration
}

type lifecycleRequest struct {
	ConfigYAML json.RawMessage `json:"config_yaml"`
}

func defaultConfig() pluginConfig {
	return pluginConfig{
		DatabasePath:   defaultDatabasePath,
		Retention:      defaultRetention,
		MaxRows:        defaultMaxRows,
		StreamStateTTL: defaultStreamStateTTL,
	}
}

// decodeLifecycleConfig applies the plugin config block at register and
// reconfigure. Invalid values fall back to the documented defaults per field
// instead of failing registration: a passive observer must load even when its
// config block is malformed.
func decodeLifecycleConfig(raw []byte) pluginConfig {
	cfg := defaultConfig()
	if len(raw) == 0 {
		return cfg
	}
	var req lifecycleRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return cfg
	}
	text, err := lifecycleConfigText(req.ConfigYAML)
	if err != nil {
		return cfg
	}
	values := yamlScalars(text)
	if value := values["database-path"]; value != "" {
		cfg.DatabasePath = value
	}
	if value := values["retention"]; value != "" {
		if parsed, parseErr := time.ParseDuration(value); parseErr == nil && parsed > 0 && parsed <= 365*24*time.Hour {
			cfg.Retention = parsed
		}
	}
	if value := values["max-rows"]; value != "" {
		if parsed, parseErr := strconv.Atoi(value); parseErr == nil && parsed > 0 && parsed <= 1000000 {
			cfg.MaxRows = parsed
		}
	}
	if value := values["stream-state-ttl"]; value != "" {
		if parsed, parseErr := time.ParseDuration(value); parseErr == nil && parsed > 0 && parsed <= 24*time.Hour {
			cfg.StreamStateTTL = parsed
		}
	}
	return cfg
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
