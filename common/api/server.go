// Package api exposes a thin HTTP layer that aggregates lending/borrowing
// depth from every registered source, plus a tiny single-page frontend
// served from the embedded web/ bundle. Built on Fiber v2.
//
// Routes:
//
//	GET /                       — embedded frontend (index.html)
//	GET /api/health             — liveness
//	GET /api/markets            — every source's markets, fetched concurrently
//	GET /api/markets?source={n}&token={id}
//	                            — filter by source name and/or token id
//	GET /api/markets/by-token/:id
//	                            — same as ?token=, path-style
//	GET /api/orders             — every source's orders, fetched concurrently
//	GET /api/orders?source={n}&address={addr}&limit={n}
//
// Per-source errors are returned in the `sources` map of each response so a
// single failing pipe never blanks the whole API.
package api

import (
	"context"
	"encoding/hex"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"dh-leverage/common/sources"
	"dh-leverage/common/wallet"
	"dh-leverage/web"

	"github.com/Salvionied/apollo/serialization/TransactionWitnessSet"
	"github.com/fxamacker/cbor/v2"

	"github.com/Salvionied/apollo/serialization/Transaction"
	"github.com/gofiber/fiber/v2"
)

type Server struct {
	addr   string
	srcs   []sources.Source
	wallet *wallet.Client
	app    *fiber.App
}

func New(addr string, w *wallet.Client, srcs ...sources.Source) *Server {
	s := &Server{
		addr:   addr,
		srcs:   srcs,
		wallet: w,
		app: fiber.New(fiber.Config{
			AppName:               "dh-leverage",
			DisableStartupMessage: true,
			ReadTimeout:           30 * time.Second,
			WriteTimeout:          30 * time.Second,
		}),
	}
	s.routes()
	return s
}

// App exposes the underlying Fiber app for callers that want to attach
// middleware or extra routes (tests, custom mounts, etc).
func (s *Server) App() *fiber.App { return s.app }

func (s *Server) routes() {
	// API
	api := s.app.Group("/api")
	api.Get("/health", s.handleHealth)
	api.Get("/markets", s.handleMarkets)
	api.Get("/markets/by-token/:id", s.handleMarketsByToken)
	api.Get("/orders", s.handleOrders)
	api.Get("/wallet/balance", s.handleBalance)

	// Transaction builders. Each accepts a sources.TxParams JSON body and
	// returns {source, action, cbor, hint}. Frontend signs via CIP-30.
	api.Post("/tx/supply", s.handleTxBuild("supply"))
	api.Post("/tx/withdraw", s.handleTxBuild("withdraw"))
	api.Post("/tx/borrow", s.handleTxBuild("borrow"))
	// Close-position (full repay or cancel pending) — no user-entered
	// amount, the protocol computes it from the on-chain position.
	api.Post("/tx/close", s.handleTxClose)
	// Witness merge: takes an unsigned tx CBOR + a CIP-30 witness set
	// and returns the combined signed tx CBOR ready to submit.
	api.Post("/tx/finalize", s.handleTxFinalize)
	// Remote submit: dispatches to the source's own submit endpoint
	// (Liqwid GraphQL submitTransaction; Surf /api/wallet/assemble +
	// /api/wallet/submit). Avoids client-side witness merging entirely.
	api.Post("/tx/submit", s.handleTxSubmit)

	// Embedded frontend at /
	s.app.Get("/", s.handleIndex)
}

func (s *Server) Start() error {
	addr := normalizeAddr(s.addr)
	log.Printf("api: listening on %s with %d source(s)", addr, len(s.srcs))
	return s.app.Listen(addr)
}

// Shutdown gracefully closes the underlying Fiber app.
func (s *Server) Shutdown(ctx context.Context) error {
	return s.app.ShutdownWithContext(ctx)
}

// normalizeAddr accepts "8080", ":8080", "127.0.0.1:8080", etc and returns
// something Fiber's Listen will accept. Fixes a footgun where API_PORT in
// .env is set without the leading colon.
func normalizeAddr(a string) string {
	if a == "" {
		return ":8080"
	}
	if strings.Contains(a, ":") {
		return a
	}
	return ":" + a
}

// --- frontend ----------------------------------------------------------------

func (s *Server) handleIndex(c *fiber.Ctx) error {
	// Disable browser cache for the frontend — the embedded HTML is
	// rebuilt with every binary and we want UI changes to show up on
	// a simple reload without requiring a hard refresh.
	c.Set("Cache-Control", "no-store, no-cache, must-revalidate")
	c.Set("Pragma", "no-cache")
	c.Set("Expires", "0")
	c.Type("html")
	return c.Send(web.IndexHTML)
}

// --- handlers ----------------------------------------------------------------

func (s *Server) handleHealth(c *fiber.Ctx) error {
	return c.JSON(fiber.Map{"status": "ok"})
}

type marketsResponse struct {
	FetchedAt time.Time         `json:"fetchedAt"`
	Sources   map[string]string `json:"sources"` // source name -> error string ("" if ok)
	Markets   []sources.Market  `json:"markets"`
}

func (s *Server) handleMarkets(c *fiber.Ctx) error {
	return s.respondMarkets(c, c.Query("source"), c.Query("token"))
}

func (s *Server) handleMarketsByToken(c *fiber.Ctx) error {
	return s.respondMarkets(c, c.Query("source"), c.Params("id"))
}

func (s *Server) respondMarkets(c *fiber.Ctx, sourceFilter, token string) error {
	ctx, cancel := context.WithTimeout(c.UserContext(), 30*time.Second)
	defer cancel()

	var (
		mu   sync.Mutex
		all  []sources.Market
		errs = make(map[string]string)
		wg   sync.WaitGroup
	)

	for _, src := range s.srcs {
		if sourceFilter != "" && src.Name() != sourceFilter {
			continue
		}
		wg.Add(1)
		go func(src sources.Source) {
			defer wg.Done()
			ms, err := src.FetchMarkets(ctx)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs[src.Name()] = err.Error()
				log.Printf("api: source %s markets failed: %v", src.Name(), err)
				return
			}
			errs[src.Name()] = ""
			all = append(all, ms...)
		}(src)
	}
	wg.Wait()

	if token != "" {
		all = filterByToken(all, token)
	}

	return c.JSON(marketsResponse{
		FetchedAt: time.Now().UTC(),
		Sources:   errs,
		Markets:   all,
	})
}

// filterByToken returns the markets where either side matches the token
// identifier. Match is case-insensitive and tries (in order):
//
//  1. Symbol equality (e.g. "SNEK", "ADA")
//  2. Bare policy ID
//  3. Full unit (policyId + hexName concatenated)
//
// "ada" / empty unit is treated specially because ADA carries no policy id.
func filterByToken(ms []sources.Market, token string) []sources.Market {
	t := strings.ToLower(strings.TrimSpace(token))
	if t == "" {
		return ms
	}
	out := ms[:0]
	for _, m := range ms {
		if matchesToken(m.Borrow, t) || matchesToken(m.Collateral, t) {
			out = append(out, m)
		}
	}
	return out
}

func matchesToken(a sources.Asset, token string) bool {
	if token == "ada" {
		return a.Symbol == "ADA" || (a.PolicyID == "" && a.AssetName == "" && a.Symbol != "")
	}
	if strings.EqualFold(a.Symbol, token) {
		return true
	}
	if strings.EqualFold(a.PolicyID, token) {
		return true
	}
	if strings.EqualFold(a.PolicyID+a.AssetName, token) {
		return true
	}
	return false
}

type ordersResponse struct {
	FetchedAt time.Time         `json:"fetchedAt"`
	Sources   map[string]string `json:"sources"`
	Orders    []sources.Order   `json:"orders"`
}

// handleTxFinalize merges a CIP-30 witness set into an unsigned tx and
// returns the combined hex ready to broadcast. Replaces the broken
// inline JS combine helper which was wiping the protocol's existing
// witnesses (Plutus scripts, redeemers, datums) and producing
// TxSendError on submit.
func (s *Server) handleTxFinalize(c *fiber.Ctx) error {
	var body struct {
		CBOR       string `json:"cbor"`
		WitnessSet string `json:"witnessSet"`
	}
	if err := c.BodyParser(&body); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid body: " + err.Error()})
	}
	if body.CBOR == "" || body.WitnessSet == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "cbor and witnessSet required"})
	}
	fmt.Println("INITIAL CBOR", body.CBOR)
	fmt.Println("WITNESS SET", body.WitnessSet)
	tx := Transaction.Transaction{}
	twsEmpty := TransactionWitnessSet.TransactionWitnessSet{}
	//tws.PlutusData = PlutusData.PlutusIndefArray{}
	tx.TransactionWitnessSet = twsEmpty

	decoded, _ := hex.DecodeString(body.CBOR)
	err := cbor.Unmarshal(decoded, &tx)
	if err != nil {
		c.SendStatus(500)
		return c.SendString(string("invalid transaction"))
	}
	witnessSet := tx.TransactionWitnessSet
	tws := TransactionWitnessSet.TransactionWitnessSet{}
	decoded_witness, _ := hex.DecodeString(body.WitnessSet)
	err = cbor.Unmarshal(decoded_witness, &tws)
	if err != nil {
		c.SendStatus(500)
		return c.SendString(string("invalid transaction"))
	}
	witnessSet.VkeyWitnesses = append(witnessSet.VkeyWitnesses, tws.VkeyWitnesses...)
	tx.TransactionWitnessSet = witnessSet
	encMode := cbor.CTAP2EncOptions()
	enc, _ := encMode.EncMode()
	cborHex, err := enc.Marshal(tx)
	if err != nil {
		c.SendStatus(500)
		return c.SendString(string("invalid transaction"))
	}
	fmt.Println("FINAL CBORHEX", hex.EncodeToString(cborHex))
	return c.JSON(fiber.Map{"cbor": cborHex})
}

// handleTxClose builds a full-repay or cancel tx for the target
// source. Body is a sources.TxCloseParams with {source, marketId,
// address, txHash, outputIndex, kind}. Returns {source, action, cbor,
// hint} like the other build endpoints.
func (s *Server) handleTxClose(c *fiber.Ctx) error {
	var p sources.TxCloseParams
	if err := c.BodyParser(&p); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid body: " + err.Error()})
	}
	if p.Source == "" || p.MarketID == "" || p.Address == "" || p.TxHash == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "source, marketId, address and txHash required"})
	}
	if p.Kind == "" {
		p.Kind = "repay"
	}
	log.Printf("api: tx close request source=%s kind=%s marketId=%s txHash=%s#%d utxos=%d otherAddrs=%d",
		p.Source, p.Kind, p.MarketID, p.TxHash, p.OutputIndex, len(p.UTXOs), len(p.OtherAddresses))
	var src sources.Source
	for _, x := range s.srcs {
		if x.Name() == p.Source {
			src = x
			break
		}
	}
	if src == nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "unknown source: " + p.Source})
	}
	builder, ok := src.(sources.TxBuilder)
	if !ok {
		return c.Status(fiber.StatusNotImplemented).JSON(fiber.Map{"error": "source does not implement TxBuilder"})
	}
	ctx, cancel := context.WithTimeout(c.UserContext(), 30*time.Second)
	defer cancel()
	built, err := builder.BuildClose(ctx, p)
	if err != nil {
		log.Printf("api: tx close on %s failed: %v", p.Source, err)
		return c.Status(fiber.StatusBadGateway).JSON(fiber.Map{"error": err.Error()})
	}
	return c.JSON(built)
}

// handleTxSubmit dispatches to the target source's remote submit
// endpoint. Liqwid uses GraphQL submitTransaction(transaction, signature)
// which merges the wallet witness set into the unsigned tx server-side.
// Surf uses /api/wallet/assemble + /api/wallet/submit. Either way the
// frontend never has to merge CBOR locally — every TxSendError caused
// by script_data_hash mismatches is gone.
func (s *Server) handleTxSubmit(c *fiber.Ctx) error {
	var p sources.SubmitParams
	if err := c.BodyParser(&p); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid body: " + err.Error()})
	}
	if p.Source == "" || p.CBOR == "" || p.WitnessSet == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "source, cbor and witnessSet required"})
	}
	var src sources.Source
	for _, x := range s.srcs {
		if x.Name() == p.Source {
			src = x
			break
		}
	}
	if src == nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "unknown source: " + p.Source})
	}
	builder, ok := src.(sources.TxBuilder)
	if !ok {
		return c.Status(fiber.StatusNotImplemented).JSON(fiber.Map{"error": "source does not implement TxBuilder"})
	}
	ctx, cancel := context.WithTimeout(c.UserContext(), 45*time.Second)
	defer cancel()
	hash, err := builder.SubmitTx(ctx, p)
	if err != nil {
		log.Printf("api: tx submit on %s failed: %v", p.Source, err)
		return c.Status(fiber.StatusBadGateway).JSON(fiber.Map{"error": err.Error()})
	}
	return c.JSON(fiber.Map{"txHash": hash})
}

// handleTxBuild returns a Fiber handler bound to a specific action
// (supply / withdraw / borrow). The body is a sources.TxParams JSON; the
// `source` field selects which Source's TxBuilder to dispatch to.
func (s *Server) handleTxBuild(action string) fiber.Handler {
	return func(c *fiber.Ctx) error {
		var p sources.TxParams
		if err := c.BodyParser(&p); err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid body: " + err.Error()})
		}
		p.Action = action
		if p.Source == "" {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "source required"})
		}
		if p.Address == "" {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "address required"})
		}
		if p.Amount <= 0 {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "amount must be > 0"})
		}

		var src sources.Source
		for _, x := range s.srcs {
			if x.Name() == p.Source {
				src = x
				break
			}
		}
		if src == nil {
			return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "unknown source: " + p.Source})
		}
		builder, ok := src.(sources.TxBuilder)
		if !ok {
			return c.Status(fiber.StatusNotImplemented).JSON(fiber.Map{"error": "source does not implement TxBuilder"})
		}

		ctx, cancel := context.WithTimeout(c.UserContext(), 30*time.Second)
		defer cancel()

		var (
			tx  *sources.BuiltTx
			err error
		)
		log.Printf("api: tx %s source=%s amount=%.6f full=%v", action, p.Source, p.Amount, p.Full)
		switch action {
		case "supply":
			tx, err = builder.BuildSupply(ctx, p)
		case "withdraw":
			tx, err = builder.BuildWithdraw(ctx, p)
		case "borrow":
			tx, err = builder.BuildBorrow(ctx, p)
		default:
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "unknown action"})
		}
		if err != nil {
			log.Printf("api: tx %s on %s failed: %v", action, p.Source, err)
			return c.Status(fiber.StatusBadGateway).JSON(fiber.Map{"error": err.Error()})
		}
		return c.JSON(tx)
	}
}

func (s *Server) handleBalance(c *fiber.Ctx) error {
	addr := c.Query("address")
	if addr == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "address query param required"})
	}
	if s.wallet == nil {
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{"error": "wallet client not configured"})
	}
	ctx, cancel := context.WithTimeout(c.UserContext(), 20*time.Second)
	defer cancel()
	bal, err := s.wallet.FetchBalance(ctx, addr)
	if err != nil {
		log.Printf("api: wallet balance failed for %s: %v", addr[:min(16, len(addr))], err)
		return c.Status(fiber.StatusBadGateway).JSON(fiber.Map{"error": err.Error()})
	}
	return c.JSON(bal)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func (s *Server) handleOrders(c *fiber.Ctx) error {
	ctx, cancel := context.WithTimeout(c.UserContext(), 30*time.Second)
	defer cancel()

	filter := c.Query("source")
	query := sources.OrderQuery{
		Address: c.Query("address"),
		Limit:   c.QueryInt("limit", 0),
		Refresh: c.Query("refresh") == "1" || c.Query("refresh") == "true",
	}

	var (
		mu   sync.Mutex
		all  []sources.Order
		errs = make(map[string]string)
		wg   sync.WaitGroup
	)

	for _, src := range s.srcs {
		if filter != "" && src.Name() != filter {
			continue
		}
		wg.Add(1)
		go func(src sources.Source) {
			defer wg.Done()
			os, err := src.FetchOrders(ctx, query)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs[src.Name()] = err.Error()
				log.Printf("api: source %s orders failed: %v", src.Name(), err)
				return
			}
			errs[src.Name()] = ""
			all = append(all, os...)
		}(src)
	}
	wg.Wait()

	return c.JSON(ordersResponse{
		FetchedAt: time.Now().UTC(),
		Sources:   errs,
		Orders:    all,
	})
}
