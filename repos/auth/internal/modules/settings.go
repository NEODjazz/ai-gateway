package modules

import (
	"errors"
	"os"
	"strconv"
	"strings"
)

type Settings struct {
	PostgresKeysEnabled     bool
	PostgresDSN             string
	KeyHashSecret           string
	CredentialEncryptionKey string
	credentialEncryptionErr error
	StaticKeyFallback       bool
	DemoKeysEnabled         bool
}

func SettingsFromEnv() Settings {
	key, keyErr := credentialEncryptionKeyFromEnv()
	return Settings{
		PostgresKeysEnabled:     envBool("AUTH_POSTGRES_KEYS_ENABLED", false),
		PostgresDSN:             strings.TrimSpace(os.Getenv("AUTH_POSTGRES_DSN")),
		KeyHashSecret:           os.Getenv("AUTH_KEY_HASH_SECRET"),
		CredentialEncryptionKey: key,
		credentialEncryptionErr: keyErr,
		StaticKeyFallback:       envBool("AUTH_STATIC_KEY_FALLBACK_ENABLED", true),
		DemoKeysEnabled:         envBool("AUTH_DEMO_KEYS_ENABLED", true),
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

// The provider-specific name is a deprecated alias, never a second key.
func credentialEncryptionKeyFromEnv() (string, error) {
	key, configured := os.LookupEnv("CREDENTIAL_ENCRYPTION_KEY")
	legacy := os.Getenv("PROVIDER_CREDENTIAL_ENCRYPTION_KEY")
	if !configured {
		return legacy, nil
	}
	if legacy != "" && key != legacy {
		return "", errors.New("CREDENTIAL_ENCRYPTION_KEY conflicts with deprecated PROVIDER_CREDENTIAL_ENCRYPTION_KEY")
	}
	return key, nil
}
