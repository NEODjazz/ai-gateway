package modules

import (
	"os"
	"strconv"
	"time"
)

type Settings struct {
	Pricing                      PricingConfig
	UsageEventsEnabled           bool
	ClickHouseURL                string
	ClickHouseDatabase           string
	ClickHouseUsername           string
	ClickHousePassword           string
	ClickHouseUsageEventsTable   string
	TariffsEnabled               bool
	LimitsEnabled                bool
	QuotasEnabled                bool
	FinancialTransactionsEnabled bool
	PostgresDSN                  string
	DurableOutboxEnabled         bool
	OutboxPollInterval           time.Duration
}

func SettingsFromEnv() Settings {
	return Settings{
		Pricing:                      PricingConfigFromEnv(),
		UsageEventsEnabled:           envBool("BILLING_USAGE_EVENTS_ENABLED", false),
		ClickHouseURL:                env("CLICKHOUSE_URL", "http://localhost:8123"),
		ClickHouseDatabase:           env("CLICKHOUSE_DATABASE", "ai_gateway"),
		ClickHouseUsername:           env("CLICKHOUSE_USERNAME", ""),
		ClickHousePassword:           env("CLICKHOUSE_PASSWORD", ""),
		ClickHouseUsageEventsTable:   env("CLICKHOUSE_USAGE_EVENTS_TABLE", "usage_events"),
		TariffsEnabled:               envBool("BILLING_TARIFFS_ENABLED", false),
		LimitsEnabled:                envBool("BILLING_LIMITS_ENABLED", false),
		QuotasEnabled:                envBool("BILLING_QUOTAS_ENABLED", false),
		FinancialTransactionsEnabled: envBool("BILLING_FINANCIAL_TRANSACTIONS_ENABLED", false),
		PostgresDSN:                  os.Getenv("POSTGRES_DSN"),
		DurableOutboxEnabled:         envBool("BILLING_DURABLE_OUTBOX_ENABLED", false),
		OutboxPollInterval:           time.Duration(envInt("BILLING_OUTBOX_POLL_MS", 500)) * time.Millisecond,
	}
}

func envInt(key string, fallback int) int {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func env(key string, fallback string) string {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	return value
}

func envFloat(key string, fallback float64) float64 {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return fallback
	}
	return parsed
}

func envBool(key string, fallback bool) bool {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	return value == "true" || value == "1" || value == "yes"
}
