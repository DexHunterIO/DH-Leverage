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
	dexhunter "dh-leverage/common/dexhunter-sdk"
	"dh-leverage/common/engine"
	"dh-leverage/common/sources"
	"dh-leverage/common/sources/liqwid"
	"dh-leverage/common/sources/surf"
	"dh-leverage/common/strike"
	"dh-leverage/common/wallet"

	"github.com/Salvionied/apollo/constants"
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

	eng, engDisabledReason := buildEngine(bootCtx, srcs)

	srv := api.New(addr, walletClient, eng, srcs...)
	strikeClient := strike.New(config.STRIKE_BUILDER_CODE, config.STRIKE_FEE_BPS).WithBase(config.STRIKE_BASE_URL).WithWithdrawLeader(config.STRIKE_WITHDRAW_LEADER)
	srv.SetStrike(strikeClient, strike.NewStore())
	srv.SetStrikeHistory(buildStrikeHistory(bootCtx))
	log.Printf("strike v2 enabled (base=%s, builder=%v, feeBps=%d)", config.STRIKE_BASE_URL, strikeClient.BuilderEnabled(), config.STRIKE_FEE_BPS)
	if strikeClient.BuilderEnabled() && config.STRIKE_FEE_BPS == 0 {
		log.Printf("strike: WARNING — builder code set but STRIKE_FEE_BPS=0, so orders collect NO builder fee")
	}
	if eng == nil {
		srv.SetEngineDisabledReason(engDisabledReason)
	} else {
		// Long-lived context so the health monitor runs for the process
		// lifetime (bootCtx is only for startup).
		eng.StartHealthMonitor(context.Background())
		// Resume any positions that were mid-flight before this restart.
		eng.RecoverInflight(context.Background())
	}
	log.Fatal(srv.Start())
}

// buildEngine wires the leverage engine. It needs BlockFrost (to build the
// funding/sweep txs and read temp-address state), a DexHunter partner key
// (swap legs), and an encryption key (to seal temp signing keys). If the
// required config is missing the engine is left nil and the /api/leverage/*
// routes return 503 — the rest of the API still runs.
func buildEngine(ctx context.Context, srcs []sources.Source) (*engine.Engine, string) {
	if config.BLOCKFROST_PROJECT_ID == "" || config.ENGINE_ENC_KEY == "" {
		reason := "leverage engine disabled: set BLOCKFROST_PROJECT_ID and ENGINE_ENC_KEY (the DexHunter partner key is optional)"
		log.Printf("%s", reason)
		return nil, reason
	}
	network := parseNetwork(config.CARDANO_NETWORK)
	chain, err := engine.NewChain(config.BLOCKFROST_PROJECT_ID, config.BLOCKFROST_BASE_URL, network)
	if err != nil {
		reason := fmt.Sprintf("leverage engine disabled: blockfrost chain context: %v", err)
		log.Printf("%s", reason)
		return nil, reason
	}
	store, durable := buildJobStore(ctx)
	// SAFETY: the engine holds the only copy of each position's (encrypted)
	// temp signing key in the job store. With the in-memory fallback those
	// keys vanish on restart, stranding any funds at the temp address with no
	// way to recover them. Never run the engine non-durably against real
	// mainnet funds — disable it instead so no position can be created.
	if !durable && network == constants.MAINNET {
		reason := "leverage engine disabled: mainnet requires a durable job store (Mongo) so temp keys survive a restart — the in-memory fallback would strand funds. Start Mongo at DATABASE_URL and retry."
		log.Printf("%s", reason)
		return nil, reason
	}
	if !durable {
		log.Printf("WARNING: leverage engine using in-memory job store — temp keys are lost on restart. Acceptable only on a testnet.")
	}
	dex := dexhunter.New(config.DEXHUNTER_PARTNER_ID)
	log.Printf("leverage engine enabled (network=%s, dexhunter=%v, durable=%v)", config.CARDANO_NETWORK, config.DEXHUNTER_PARTNER_ID != "", durable)
	return engine.New(srcs, dex, chain, store, config.ENGINE_ENC_KEY, network), ""
}

// buildJobStore returns the leverage job store and whether it is durable
// (Mongo-backed). A non-durable (in-memory) store loses temp signing keys on
// restart, so callers gate real-fund operation on durability.
func buildJobStore(ctx context.Context) (engine.JobStore, bool) {
	store, err := engine.NewMongoJobStore(ctx, config.DATABASE_URL, "dh-leverage")
	if err != nil {
		log.Printf("leverage job store: mongo unavailable (%v) — in-memory fallback (non-durable)", err)
		return engine.NewMemoryJobStore(), false
	}
	log.Printf("leverage job store: mongo enabled (durable)")
	return store, true
}

// buildStrikeHistory returns a Mongo-backed deposit/withdrawal history store,
// falling back to in-memory when Mongo is unavailable (mirroring the cache/
// persister degradation pattern). In-memory history is lost on restart.
func buildStrikeHistory(ctx context.Context) strike.History {
	h, err := mongodb.NewStrikeHistory(ctx, config.DATABASE_URL)
	if err != nil {
		log.Printf("strike history: mongo unavailable (%v) — in-memory fallback (lost on restart)", err)
		return strike.NewMemoryHistory()
	}
	log.Printf("strike history: mongo enabled (durable)")
	return h
}

func parseNetwork(name string) constants.Network {
	switch name {
	case "preview":
		return constants.PREVIEW
	case "preprod":
		return constants.PREPROD
	case "testnet":
		return constants.TESTNET
	default:
		return constants.MAINNET
	}
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
