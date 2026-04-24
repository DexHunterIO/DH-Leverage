package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"dh-leverage/common/api"
	"dh-leverage/common/config"
	mongodb "dh-leverage/common/database/mongo"
	redisdb "dh-leverage/common/database/redis"
	"dh-leverage/common/sources"
	"dh-leverage/common/sources/liqwid"
	"dh-leverage/common/sources/surf"
	"dh-leverage/common/wallet"
)

func main() {
	args := os.Args[1:]
	if len(args) < 1 {
		fmt.Println("Usage: go run main.go <command>")
		fmt.Println("Use 'go run main.go help' to see available commands.")
		return
	}
	switch args[0] {
	case "api":
		runAPI()
	case "worker":
		fmt.Println("Starting worker...")
		// TODO: worker entrypoint
	case "help":
		fmt.Println("Available commands:")
		fmt.Println("  api     - Start the API server (aggregates lending depth)")
		fmt.Println("  worker  - Start the worker")
		fmt.Println("  help    - Show this help message")
	default:
		fmt.Printf("Unknown command: %s\n", args[0])
		fmt.Println("Use 'go run main.go help' to see available commands.")
	}
}

func runAPI() {
	if err := config.LoadEnv(); err != nil {
		log.Printf("config: %v", err)
	}
	addr := config.API_PORT
	if addr == "" {
		addr = ":8080"
	}

	bootCtx, cancelBoot := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelBoot()

	cache := buildCache(bootCtx)
	orderCache := buildOrderCache(bootCtx)
	persister := buildPersister(bootCtx)
	balanceCache := buildBalanceCache(bootCtx)
	ttl := parseTTL(config.MARKETS_CACHE_TTL, 60*time.Second)
	orderTTL := 30 * time.Second
	log.Printf("config: markets cache ttl = %s, orders cache ttl = %s", ttl, orderTTL)

	srcs := []sources.Source{
		wrapSource(liqwid.New(), persister, cache, ttl, orderCache, orderTTL),
		wrapSource(surf.New(), persister, cache, ttl, orderCache, orderTTL),
	}

	walletClient := wallet.New(balanceCache, 30*time.Second)

	srv := api.New(addr, walletClient, srcs...)
	log.Fatal(srv.Start())
}

// wrapSource composes a raw protocol source with persistence (inner layer)
// and caching (outer layer). Cache hits skip the persister so we only
// write to Mongo on a real upstream fetch. Orders are cached per
// (address, limit) in a separate namespace.
func wrapSource(src sources.Source, p sources.Persister, c sources.Cache, ttl time.Duration, oc sources.OrderCache, oTTL time.Duration) sources.Source {
	src = sources.NewPersistedSource(src, p)
	return sources.NewCachedSource(src, c, ttl).WithOrderCache(oc, oTTL)
}

func buildCache(ctx context.Context) sources.Cache {
	rc, err := redisdb.NewMarketsCache(ctx, config.REDIS_URL)
	if err != nil {
		log.Printf("redis unavailable (%v) — falling back to in-memory cache", err)
		return sources.NewMemoryCache()
	}
	log.Printf("redis cache enabled at %s", config.REDIS_URL)
	return rc
}

func buildOrderCache(ctx context.Context) sources.OrderCache {
	rc, err := redisdb.NewOrderCache(ctx, config.REDIS_URL)
	if err != nil {
		log.Printf("redis order cache unavailable (%v) — using in-memory fallback", err)
		return sources.NewMemoryOrderCache()
	}
	log.Printf("redis order cache enabled")
	return rc
}

func buildBalanceCache(ctx context.Context) wallet.BalanceCache {
	rc, err := redisdb.NewBalanceCache(ctx, config.REDIS_URL)
	if err != nil {
		log.Printf("redis balance cache unavailable (%v) — using in-memory fallback", err)
		return wallet.NewMemoryCache()
	}
	log.Printf("redis balance cache enabled")
	return rc
}

func buildPersister(ctx context.Context) sources.Persister {
	mp, err := mongodb.NewMarketsPersister(ctx, config.DATABASE_URL)
	if err != nil {
		log.Printf("mongo unavailable (%v) — persistence disabled", err)
		return sources.NoopPersister{}
	}
	log.Printf("mongo persistence enabled at %s", config.DATABASE_URL)
	return mp
}

func parseTTL(s string, fallback time.Duration) time.Duration {
	if s == "" {
		return fallback
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return fallback
	}
	return d
}
