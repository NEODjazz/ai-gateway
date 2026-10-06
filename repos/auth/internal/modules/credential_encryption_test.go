package modules

import (
	"os"
	"strings"
	"testing"
)

func TestCredentialEncryptionKeyEnvironment(t *testing.T) {
	for _, tc := range []struct {
		name         string
		canonical    *string
		legacy, want string
		wantErr      bool
	}{
		{name: "unset"},
		{name: "canonical", canonical: stringPointer("new-fixture-key"), want: "new-fixture-key"},
		{name: "legacy", legacy: "old-fixture-key", want: "old-fixture-key"},
		{name: "same", canonical: stringPointer("same-fixture-key"), legacy: "same-fixture-key", want: "same-fixture-key"},
		{name: "conflict", canonical: stringPointer("new-fixture-key"), legacy: "old-fixture-key", wantErr: true},
		{name: "explicit empty conflicts", canonical: stringPointer(""), legacy: "old-fixture-key", wantErr: true},
		{name: "explicit empty", canonical: stringPointer("")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("PROVIDER_CREDENTIAL_ENCRYPTION_KEY", tc.legacy)
			t.Setenv("CREDENTIAL_ENCRYPTION_KEY", "")
			if tc.canonical == nil {
				if err := os.Unsetenv("CREDENTIAL_ENCRYPTION_KEY"); err != nil {
					t.Fatal(err)
				}
			} else {
				t.Setenv("CREDENTIAL_ENCRYPTION_KEY", *tc.canonical)
			}
			key, err := credentialEncryptionKeyFromEnv()
			if key != tc.want || (err != nil) != tc.wantErr {
				t.Fatal("unexpected key selection or error")
			}
			if err != nil && (strings.Contains(err.Error(), "new-fixture-key") || strings.Contains(err.Error(), "old-fixture-key")) {
				t.Fatal("key exposed in error")
			}
		})
	}
}

func stringPointer(s string) *string { return &s }

func TestSettingsSeparatesEncryptionAndHashKeys(t *testing.T) {
	t.Setenv("CREDENTIAL_ENCRYPTION_KEY", strings.Repeat("e", 32))
	t.Setenv("PROVIDER_CREDENTIAL_ENCRYPTION_KEY", "")
	t.Setenv("AUTH_KEY_HASH_SECRET", "independent-hash-fixture")
	settings := SettingsFromEnv()
	if settings.CredentialEncryptionKey != strings.Repeat("e", 32) || settings.KeyHashSecret != "independent-hash-fixture" || settings.credentialEncryptionErr != nil {
		t.Fatal("configuration keys are not independent")
	}
	t.Setenv("PROVIDER_CREDENTIAL_ENCRYPTION_KEY", strings.Repeat("x", 32))
	if NewAuthModule(false).initErr == nil {
		t.Fatal("conflicting configuration accepted")
	}
}
