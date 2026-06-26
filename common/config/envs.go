package config

import (
	"os"
	"strconv"

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

	// Leverage engine. The engine signs and submits transactions on behalf
	// of a backend-held temporary address, so it needs its own chain access
	// (BlockFrost), a DexHunter partner key for the swap legs, and an
	// encryption key to protect the temp signing keys it persists.
	BLOCKFROST_PROJECT_ID string
	BLOCKFROST_BASE_URL   string
	DEXHUNTER_PARTNER_ID  string
	ENGINE_ENC_KEY        string
	CARDANO_NETWORK       string

	// Strike Finance v2 perpetuals (api.strikefinance.org), BUILDER CODES model.
	// STRIKE_BUILDER_CODE comes from registering at app.strikefinance.org/builder-codes;
	// STRIKE_FEE_BPS is the builder fee charged on order fills (≤ your registered
	// share, ≤100). Without a builder code the integration is read-only.
	STRIKE_BASE_URL        string
	STRIKE_BUILDER_CODE    string
	STRIKE_FEE_BPS         int
	STRIKE_WITHDRAW_LEADER string // batcher leaderAddress for the withdraw-batcher tx
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

	BLOCKFROST_PROJECT_ID = getEnv("BLOCKFROST_PROJECT_ID", "")
	BLOCKFROST_BASE_URL = getEnv("BLOCKFROST_BASE_URL", "https://cardano-mainnet.blockfrost.io/api")
	DEXHUNTER_PARTNER_ID = getEnv("DEXHUNTER_PARTNER_ID", "")
	ENGINE_ENC_KEY = getEnv("ENGINE_ENC_KEY", "")
	CARDANO_NETWORK = getEnv("CARDANO_NETWORK", "mainnet")

	STRIKE_BASE_URL = getEnv("STRIKE_BASE_URL", "https://api.strikefinance.org")
	STRIKE_BUILDER_CODE = getEnv("STRIKE_BUILDER_CODE", "")
	STRIKE_FEE_BPS = getEnvInt("STRIKE_FEE_BPS", 0)
	STRIKE_WITHDRAW_LEADER = getEnv("STRIKE_WITHDRAW_LEADER", "")

	return nil
}
func getEnv(key, fallback string) string {
	if value, exists := os.LookupEnv(key); exists {
		return value
	}
	return fallback
}

func getEnvInt(key string, fallback int) int {
	if value, exists := os.LookupEnv(key); exists {
		if n, err := strconv.Atoi(value); err == nil {
			return n
		}
	}
	return fallback
}
