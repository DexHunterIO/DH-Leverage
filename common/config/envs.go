package config

import (
	"os"

	"github.com/joho/godotenv"
)

var (
	API_PORT          string
	DATABASE_URL      string
	REDIS_URL         string
	UNIX_SOCKET       string
	TCP_SOCKET        string
	CONNECTION_TYPE   string
	MARKETS_CACHE_TTL string
)

func LoadEnv() error {
	// .env is optional — fall back to real environment variables when missing.
	_ = godotenv.Load()

	API_PORT = getEnv("API_PORT", ":8080")
	// Default Mongo port is 30000 (not 27017) to avoid collisions with the
	// other Cardano projects this dev box runs in Docker.
	DATABASE_URL = getEnv("DATABASE_URL", "mongodb://admin:mongodb_password@localhost:30000/dh-leverage?authSource=admin")
	REDIS_URL = getEnv("REDIS_URL", "redis://:redis_password@localhost:6379")
	UNIX_SOCKET = getEnv("UNIX_SOCKET", "/tmp/socket.sock")
	TCP_SOCKET = getEnv("TCP_SOCKET", ":9090")
	CONNECTION_TYPE = getEnv("CONNECTION_TYPE", "tcp")
	MARKETS_CACHE_TTL = getEnv("MARKETS_CACHE_TTL", "60s")

	return nil
}
func getEnv(key, fallback string) string {
	if value, exists := os.LookupEnv(key); exists {
		return value
	}
	return fallback
}
