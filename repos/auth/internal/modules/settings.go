package modules

import (
	"os"
	"strconv"
	"strings"
)

type Settings struct {
	PostgresKeysEnabled bool
	PostgresDSN         string
	KeyHashSecret       string
	StaticKeyFallback   bool
	DemoKeysEnabled     bool
}

func SettingsFromEnv() Settings {
	return Settings{
		PostgresKeysEnabled: envBool("AUTH_POSTGRES_KEYS_ENABLED", false),
		PostgresDSN:         strings.TrimSpace(os.Getenv("AUTH_POSTGRES_DSN")),
		KeyHashSecret:       os.Getenv("AUTH_KEY_HASH_SECRET"),
		StaticKeyFallback:   envBool("AUTH_STATIC_KEY_FALLBACK_ENABLED", true),
		DemoKeysEnabled:     envBool("AUTH_DEMO_KEYS_ENABLED", true),
	}
}

func envBool(name string, fallback bool) bool {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return fallback
	}
	return parsed
}
