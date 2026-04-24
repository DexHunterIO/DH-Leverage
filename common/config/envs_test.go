package config

import (
	"os"
	"testing"
)

func TestGetEnv_Fallback(t *testing.T) {
	// Ensure the key doesn't exist.
	os.Unsetenv("TEST_DH_LEVERAGE_KEY_XYZ")
	got := getEnv("TEST_DH_LEVERAGE_KEY_XYZ", "default-val")
	if got != "default-val" {
		t.Fatalf("expected fallback, got %q", got)
	}
}

func TestGetEnv_EnvSet(t *testing.T) {
	os.Setenv("TEST_DH_LEVERAGE_KEY_XYZ", "custom-val")
	defer os.Unsetenv("TEST_DH_LEVERAGE_KEY_XYZ")

	got := getEnv("TEST_DH_LEVERAGE_KEY_XYZ", "default-val")
	if got != "custom-val" {
		t.Fatalf("expected custom-val, got %q", got)
	}
}

func TestLoadEnv_SetsDefaults(t *testing.T) {
	// Ensure no .env interferes; clear vars.
	os.Unsetenv("API_PORT")
	os.Unsetenv("DATABASE_URL")
	os.Unsetenv("REDIS_URL")
	os.Unsetenv("CONNECTION_TYPE")
	os.Unsetenv("MARKETS_CACHE_TTL")

	if err := LoadEnv(); err != nil {
		t.Fatalf("LoadEnv: %v", err)
	}

	if API_PORT == "" {
		t.Fatal("API_PORT should have a default")
	}
	if CONNECTION_TYPE == "" {
		t.Fatal("CONNECTION_TYPE should have a default")
	}
	if MARKETS_CACHE_TTL == "" {
		t.Fatal("MARKETS_CACHE_TTL should have a default")
	}
}
