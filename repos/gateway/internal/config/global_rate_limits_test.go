package config

import "testing"

func TestGlobalRateLimitEnvironment(t *testing.T) {
	t.Setenv("GLOBAL_RATE_LIMIT_RPM", "")
	t.Setenv("GLOBAL_RATE_LIMIT_TPM", "")
	cfg := Load()
	if cfg.GlobalRateLimitRPM != 60 || cfg.GlobalRateLimitTPM != 100000 {
		t.Fatal("incorrect default limits")
	}
	for _, key := range []string{"GLOBAL_RATE_LIMIT_RPM", "GLOBAL_RATE_LIMIT_TPM"} {
		for _, value := range []string{"-1", "no", "1.5", "1000000001", "9223372036854775808"} {
			t.Setenv(key, value)
			if Load().InitErr == nil {
				t.Fatalf("accepted %s=%s", key, value)
			}
		}
		t.Setenv(key, "0")
	}
	cfg = Load()
	if cfg.InitErr != nil || cfg.GlobalRateLimitRPM != 0 || cfg.GlobalRateLimitTPM != 0 {
		t.Fatal("explicit zero not preserved", cfg.InitErr)
	}
}
