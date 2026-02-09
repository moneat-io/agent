package config

import (
	"os"
	"strconv"
	"strings"
	"time"
)

// Config holds the agent configuration
type Config struct {
	MoneatURL            string
	AgentKey             string
	PollInterval         time.Duration
	IngestPath           string
	LogsPath             string
	LogsEnabled          bool
	LogMode              string
	LogContainers        map[string]struct{}
	LogExclude           map[string]struct{}
	LogBatchSize         int
	LogBatchInterval     time.Duration
	LogDiscoveryInterval time.Duration
	LogProjectID         *int64
}

func LoadFromEnv() *Config {
	pollInterval := 60 * time.Second
	if val := os.Getenv("POLL_INTERVAL"); val != "" {
		if seconds, err := strconv.Atoi(val); err == nil {
			pollInterval = time.Duration(seconds) * time.Second
		}
	}

	logsEnabled := parseBoolEnv("MONEAT_LOGS", false)

	logBatchSize := 100
	if val := os.Getenv("MONEAT_LOG_BATCH_SIZE"); val != "" {
		if parsed, err := strconv.Atoi(val); err == nil && parsed > 0 {
			logBatchSize = parsed
		}
	}

	logBatchInterval := 5 * time.Second
	if val := os.Getenv("MONEAT_LOG_BATCH_INTERVAL"); val != "" {
		if parsed, err := strconv.Atoi(val); err == nil && parsed > 0 {
			logBatchInterval = time.Duration(parsed) * time.Second
		}
	}

	logDiscoveryInterval := 10 * time.Second
	if val := os.Getenv("MONEAT_LOG_DISCOVERY_INTERVAL"); val != "" {
		if parsed, err := strconv.Atoi(val); err == nil && parsed > 0 {
			logDiscoveryInterval = time.Duration(parsed) * time.Second
		}
	}

	logMode := strings.ToLower(getEnv("MONEAT_LOG_MODE", "all"))
	includes := csvSet(os.Getenv("MONEAT_LOG_CONTAINERS"))
	excludes := csvSet(os.Getenv("MONEAT_LOG_EXCLUDE"))

	// Allow include/exclude lists to implicitly set mode if no explicit mode was set.
	if os.Getenv("MONEAT_LOG_MODE") == "" {
		switch {
		case len(includes) > 0:
			logMode = "include"
		case len(excludes) > 0:
			logMode = "exclude"
		default:
			logMode = "all"
		}
	}

	var logProjectID *int64
	if val := os.Getenv("MONEAT_LOG_PROJECT_ID"); val != "" {
		if parsed, err := strconv.ParseInt(val, 10, 64); err == nil && parsed > 0 {
			logProjectID = &parsed
		}
	}

	return &Config{
		MoneatURL:            getEnv("MONEAT_URL", "https://api.moneat.io"),
		AgentKey:             getEnv("MONEAT_KEY", ""),
		PollInterval:         pollInterval,
		IngestPath:           "/v1/monitor/ingest",
		LogsPath:             "/v1/monitor/logs",
		LogsEnabled:          logsEnabled,
		LogMode:              logMode,
		LogContainers:        includes,
		LogExclude:           excludes,
		LogBatchSize:         logBatchSize,
		LogBatchInterval:     logBatchInterval,
		LogDiscoveryInterval: logDiscoveryInterval,
		LogProjectID:         logProjectID,
	}
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func parseBoolEnv(key string, defaultValue bool) bool {
	val := strings.TrimSpace(strings.ToLower(os.Getenv(key)))
	if val == "" {
		return defaultValue
	}
	switch val {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return defaultValue
	}
}

func csvSet(raw string) map[string]struct{} {
	result := make(map[string]struct{})
	for _, token := range strings.Split(raw, ",") {
		value := strings.ToLower(strings.TrimSpace(token))
		if value == "" {
			continue
		}
		result[value] = struct{}{}
	}
	return result
}
