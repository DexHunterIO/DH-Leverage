package config

import (
	"os"

	"github.com/joho/godotenv"
)

var (
	API_PORT        string
	DATABASE_URL    string
	REDIS_URL       string
	UNIX_SOCKET     string
	TCP_SOCKET      string
	CONNECTION_TYPE string
)

func LoadEnv() error {
	err := godotenv.Load()
	if err != nil {
		return err
	}

	API_PORT = getEnv("API_PORT", ":8080")
	DATABASE_URL = getEnv("DATABASE_URL", "postgres://user:pass@localhost:5432/dbname")
	REDIS_URL = getEnv("REDIS_URL", "redis://localhost:6379")
	UNIX_SOCKET = getEnv("UNIX_SOCKET", "/tmp/socket.sock")
	TCP_SOCKET = getEnv("TCP_SOCKET", ":9090")
	CONNECTION_TYPE = getEnv("CONNECTION_TYPE", "tcp")

	return nil
}
func getEnv(key, fallback string) string {
	if value, exists := os.LookupEnv(key); exists {
		return value
	}
	return fallback
}
