package api

import (
	"context"
	"encoding/json"
	"log"
	"time"

	"dh-leverage/common/strike"

	"github.com/gofiber/fiber/v2"
)

func (s *Server) strikeReady(c *fiber.Ctx) bool {
	if s.strike == nil || s.strikeStore == nil {
		_ = c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{"error": "strike perpetuals not configured"})
		return false
	}
	return true
}

// strikeCred resolves the connected user's credential from the address, or
// writes a 401 and returns nil.
func (s *Server) strikeCred(c *fiber.Ctx, address string) *strike.Credential {
	cred, err := s.strikeStore.Credential(address)
	if err != nil {
		_ = c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": err.Error()})
		return nil
	}
	return cred
}

// handleStrikeStatus reports whether trading is enabled and whether an address is
// connected.
func (s *Server) handleStrikeStatus(c *fiber.Ctx) error {
	if !s.strikeReady(c) {
		return nil
	}
	return c.JSON(fiber.Map{
		"builderEnabled": s.strike.BuilderEnabled(),
		"feeBps":         s.strike.FeeBps(),
		"connected":      c.Query("address") != "" && s.strikeStore.Connected(c.Query("address")),
	})
}

// handleStrikeConnectChallenge starts builder-connect: generate this user's API
// keypair, ask Strike for a challenge, stash the keypair by nonce, return the
// message for the wallet to sign (CIP-30).
func (s *Server) handleStrikeConnectChallenge(c *fiber.Ctx) error {
	if !s.strikeReady(c) {
		return nil
	}
	if !s.strike.BuilderEnabled() {
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{"error": "no builder code configured (STRIKE_BUILDER_CODE)"})
	}
	var body struct {
		Address string `json:"address"`
	}
	if err := c.BodyParser(&body); err != nil || body.Address == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "address required"})
	}
	seedHex, pubHex, err := strike.GenerateKeyPair()
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}
	ctx, cancel := context.WithTimeout(c.UserContext(), 25*time.Second)
	defer cancel()
	ch, err := s.strike.RequestSignature(ctx, body.Address, strike.ChainCardano, pubHex, s.strike.FeeBps())
	if err != nil {
		log.Printf("api: strike connect challenge failed: %v", err)
		return c.Status(fiber.StatusBadGateway).JSON(fiber.Map{"error": err.Error()})
	}
	s.strikeStore.PutPending(ch.Nonce, body.Address, seedHex, pubHex)
	return c.JSON(fiber.Map{"nonce": ch.Nonce, "messageToSign": ch.MessageToSign})
}

// handleStrikeConnectVerify completes builder-connect: submit the user's wallet
// signature, store the verified credential keyed by address.
func (s *Server) handleStrikeConnectVerify(c *fiber.Ctx) error {
	if !s.strikeReady(c) {
		return nil
	}
	var body struct {
		Address         string `json:"address"`
		Nonce           string `json:"nonce"`
		WalletSignature string `json:"walletSignature"` // CIP-30 "{coseSign1Hex}:{coseKeyHex}"
	}
	if err := c.BodyParser(&body); err != nil || body.Nonce == "" || body.WalletSignature == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "nonce and walletSignature required"})
	}
	seedHex, pubHex, _, ok := s.strikeStore.TakePending(body.Nonce)
	if !ok {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "challenge expired or unknown — restart connect"})
	}
	ctx, cancel := context.WithTimeout(c.UserContext(), 25*time.Second)
	defer cancel()
	res, err := s.strike.VerifySignature(ctx, body.Nonce, body.WalletSignature)
	if err != nil {
		log.Printf("api: strike connect verify failed: %v", err)
		return c.Status(fiber.StatusBadGateway).JSON(fiber.Map{"error": err.Error()})
	}
	cred, err := strike.CredentialFromHex(seedHex, pubHex, res.AccountID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}
	s.strikeStore.PutCredential(body.Address, cred)
	return c.JSON(fiber.Map{"connected": true, "accountId": res.AccountID})
}

// --- public reads ------------------------------------------------------------

// handleStrikeMarkets lists tradeable perp markets (public).
func (s *Server) handleStrikeMarkets(c *fiber.Ctx) error {
	if !s.strikeReady(c) {
		return nil
	}
	ctx, cancel := context.WithTimeout(c.UserContext(), 15*time.Second)
	defer cancel()
	raw, err := s.strike.Markets(ctx)
	if err != nil {
		return c.Status(fiber.StatusBadGateway).JSON(fiber.Map{"error": err.Error()})
	}
	c.Type("json")
	return c.Send(raw)
}

// handleStrikeBalances returns the connected user's balances.
func (s *Server) handleStrikeBalances(c *fiber.Ctx) error {
	if !s.strikeReady(c) {
		return nil
	}
	cred := s.strikeCred(c, c.Query("address"))
	if cred == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(c.UserContext(), 20*time.Second)
	defer cancel()
	raw, err := s.strike.Balances(ctx, cred)
	if err != nil {
		return c.Status(fiber.StatusBadGateway).JSON(fiber.Map{"error": err.Error()})
	}
	c.Type("json")
	return c.Send(raw)
}

// handleStrikeWithdrawQuote quotes a withdrawal of usdValue (USD) to the user's
// wallet; returns {withdraw_id, fee, message_to_sign} to sign.
func (s *Server) handleStrikeWithdrawQuote(c *fiber.Ctx) error {
	if !s.strikeReady(c) {
		return nil
	}
	var body struct {
		Address  string `json:"address"`
		UsdValue string `json:"usdValue"`
		Asset    string `json:"asset"`
	}
	if err := c.BodyParser(&body); err != nil || body.UsdValue == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "address and usdValue required"})
	}
	asset := body.Asset
	if asset == "" {
		asset = "USDM" // default settlement asset on Cardano (ADA is rejected)
	}
	cred := s.strikeCred(c, body.Address)
	if cred == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(c.UserContext(), 25*time.Second)
	defer cancel()
	raw, err := s.strike.WithdrawQuote(ctx, cred, body.UsdValue, asset, body.Address)
	if err != nil {
		return c.Status(fiber.StatusBadGateway).JSON(fiber.Map{"error": err.Error()})
	}
	c.Type("json")
	return c.Send(raw)
}

// strikeHist returns the configured history store, lazily creating an in-memory
// one if none was wired (so the history routes always work, just non-durably).
func (s *Server) strikeHist() strike.History {
	if s.strikeHistory == nil {
		s.strikeHistory = strike.NewMemoryHistory()
	}
	return s.strikeHistory
}

// recordHistory best-effort records a deposit/withdraw entry. Never blocks the
// caller's response on a history write failure.
func (s *Server) recordHistory(e strike.HistoryEntry) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.strikeHist().Record(ctx, e); err != nil {
		log.Printf("api: strike history record failed: %v", err)
	}
}

// handleStrikeHistory lists a user's recorded deposits/withdrawals, newest first.
func (s *Server) handleStrikeHistory(c *fiber.Ctx) error {
	address := c.Query("address")
	if address == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "address required"})
	}
	ctx, cancel := context.WithTimeout(c.UserContext(), 10*time.Second)
	defer cancel()
	entries, err := s.strikeHist().List(ctx, address)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}
	if entries == nil {
		entries = []strike.HistoryEntry{}
	}
	return c.JSON(fiber.Map{"entries": entries})
}

// handleStrikeWithdrawSettle attaches the on-chain settlement tx hash to a
// previously-recorded pending withdrawal (called by the UI after it submits the
// batcher tx).
func (s *Server) handleStrikeWithdrawSettle(c *fiber.Ctx) error {
	var body struct {
		Address    string `json:"address"`
		WithdrawID string `json:"withdrawId"`
		TxHash     string `json:"txHash"`
		Status     string `json:"status"`
	}
	if err := c.BodyParser(&body); err != nil || body.Address == "" || body.WithdrawID == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "address and withdrawId required"})
	}
	status := body.Status
	if status == "" {
		status = "sent"
	}
	ctx, cancel := context.WithTimeout(c.UserContext(), 10*time.Second)
	defer cancel()
	if err := s.strikeHist().SettleWithdraw(ctx, body.Address, body.WithdrawID, body.TxHash, status); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}
	return c.JSON(fiber.Map{"ok": true})
}

// handleStrikeWithdrawBatcher builds the on-chain batcher tx that settles a
// confirmed withdrawal. Returns Strike's response ({txData: unsigned cbor}) for
// the user to sign + submit. Public (the batcher endpoint is unauthenticated),
// but we still require a connected user for consistency.
func (s *Server) handleStrikeWithdrawBatcher(c *fiber.Ctx) error {
	if !s.strikeReady(c) {
		return nil
	}
	var body struct {
		Address       string   `json:"address"`
		LeaderAddress string   `json:"leaderAddress"`
		Utxos         []string `json:"utxos"`
	}
	if err := c.BodyParser(&body); err != nil || body.Address == "" || len(body.Utxos) == 0 {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "address and utxos required"})
	}
	leader := body.LeaderAddress
	if leader == "" {
		leader = s.strike.WithdrawLeader()
	}
	if leader == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "leaderAddress required (set STRIKE_WITHDRAW_LEADER or pass leaderAddress)"})
	}
	ctx, cancel := context.WithTimeout(c.UserContext(), 30*time.Second)
	defer cancel()
	raw, err := s.strike.WithdrawBatcher(ctx, body.Address, leader, body.Utxos)
	if err != nil {
		log.Printf("api: strike withdraw-batcher failed: %v", err)
		return c.Status(fiber.StatusBadGateway).JSON(fiber.Map{"error": err.Error()})
	}
	c.Type("json")
	return c.Send(raw)
}

// handleStrikeWithdrawConfirm submits the signed withdrawal.
func (s *Server) handleStrikeWithdrawConfirm(c *fiber.Ctx) error {
	if !s.strikeReady(c) {
		return nil
	}
	var body struct {
		Address         string `json:"address"`
		WithdrawID      string `json:"withdrawId"`
		WalletSignature string `json:"walletSignature"`
		Asset           string `json:"asset"`    // display-only
		UsdValue        string `json:"usdValue"` // display-only
	}
	if err := c.BodyParser(&body); err != nil || body.WithdrawID == "" || body.WalletSignature == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "address, withdrawId and walletSignature required"})
	}
	cred := s.strikeCred(c, body.Address)
	if cred == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(c.UserContext(), 25*time.Second)
	defer cancel()
	raw, err := s.strike.WithdrawConfirm(ctx, cred, body.WithdrawID, body.WalletSignature)
	if err != nil {
		return c.Status(fiber.StatusBadGateway).JSON(fiber.Map{"error": err.Error()})
	}
	asset := body.Asset
	if asset == "" {
		asset = "USDM"
	}
	s.recordHistory(strike.HistoryEntry{
		Address: body.Address, Type: "withdraw", Asset: asset,
		UsdValue: body.UsdValue, WithdrawID: body.WithdrawID, Status: "pending",
	})
	c.Type("json")
	return c.Send(raw)
}

func (s *Server) handleStrikePositions(c *fiber.Ctx) error {
	if !s.strikeReady(c) {
		return nil
	}
	addr := c.Query("address")
	if addr == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "address query param required"})
	}
	ctx, cancel := context.WithTimeout(c.UserContext(), 20*time.Second)
	defer cancel()
	raw, err := s.strike.Positions(ctx, addr)
	if err != nil {
		return c.Status(fiber.StatusBadGateway).JSON(fiber.Map{"error": err.Error()})
	}
	c.Type("json")
	return c.Send(raw)
}

func (s *Server) handleStrikeAccount(c *fiber.Ctx) error {
	if !s.strikeReady(c) {
		return nil
	}
	addr := c.Query("address")
	if addr == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "address query param required"})
	}
	ctx, cancel := context.WithTimeout(c.UserContext(), 20*time.Second)
	defer cancel()
	raw, err := s.strike.Account(ctx, addr)
	if err != nil {
		return c.Status(fiber.StatusBadGateway).JSON(fiber.Map{"error": err.Error()})
	}
	c.Type("json")
	return c.Send(raw)
}

// --- authenticated (per connected user) --------------------------------------

func (s *Server) handleStrikeLeverage(c *fiber.Ctx) error {
	if !s.strikeReady(c) {
		return nil
	}
	var body struct {
		Address  string `json:"address"`
		Symbol   string `json:"symbol"`
		Leverage int    `json:"leverage"`
	}
	if err := c.BodyParser(&body); err != nil || body.Symbol == "" || body.Leverage <= 0 {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "address, symbol and leverage (>0) required"})
	}
	cred := s.strikeCred(c, body.Address)
	if cred == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(c.UserContext(), 25*time.Second)
	defer cancel()
	raw, err := s.strike.SetLeverage(ctx, cred, body.Symbol, body.Leverage)
	if err != nil {
		log.Printf("api: strike leverage failed: %v", err)
		return c.Status(fiber.StatusBadGateway).JSON(fiber.Map{"error": err.Error()})
	}
	c.Type("json")
	return c.Send(raw)
}

// handleStrikeOrder places an order as the connected user. position "Long"/"Short"
// maps to buy/sell; reduceOnly closes.
func (s *Server) handleStrikeOrder(c *fiber.Ctx) error {
	if !s.strikeReady(c) {
		return nil
	}
	var body struct {
		Address  string `json:"address"`
		Symbol   string `json:"symbol"`
		Position string `json:"position"` // "Long" | "Short"
		Side     string `json:"side"`     // optional explicit "buy"/"sell"
		Type     string `json:"type"`     // default "market"
		Size     string `json:"size"`
		Price    string `json:"price"`
		Reduce   bool   `json:"reduceOnly"`
	}
	if err := c.BodyParser(&body); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid body: " + err.Error()})
	}
	side := body.Side
	if side == "" {
		switch body.Position {
		case "Long":
			side = "buy"
		case "Short":
			side = "sell"
		default:
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "position must be Long or Short (or pass side)"})
		}
	}
	typ := body.Type
	if typ == "" {
		typ = "market"
	}
	if body.Symbol == "" || body.Size == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "symbol and size required"})
	}
	cred := s.strikeCred(c, body.Address)
	if cred == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(c.UserContext(), 30*time.Second)
	defer cancel()
	raw, err := s.strike.CreateOrder(ctx, cred, strike.CreateOrderRequest{
		Symbol: body.Symbol, Side: side, Type: typ, Size: body.Size,
		Price: body.Price, ReduceOnly: body.Reduce,
	})
	if err != nil {
		log.Printf("api: strike order failed: %v", err)
		return c.Status(fiber.StatusBadGateway).JSON(fiber.Map{"error": err.Error()})
	}
	c.Type("json")
	return c.Send(raw)
}

func (s *Server) handleStrikeOrderCancel(c *fiber.Ctx) error {
	if !s.strikeReady(c) {
		return nil
	}
	var body struct {
		Address string `json:"address"`
		OrderID int64  `json:"orderId"`
		Symbol  string `json:"symbol"`
	}
	if err := c.BodyParser(&body); err != nil || body.OrderID == 0 || body.Symbol == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "address, orderId and symbol required"})
	}
	cred := s.strikeCred(c, body.Address)
	if cred == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(c.UserContext(), 25*time.Second)
	defer cancel()
	raw, err := s.strike.CancelOrder(ctx, cred, body.OrderID, body.Symbol)
	if err != nil {
		return c.Status(fiber.StatusBadGateway).JSON(fiber.Map{"error": err.Error()})
	}
	c.Type("json")
	return c.Send(raw)
}

// --- deposit (quote → build → confirm) ---------------------------------------

func (s *Server) handleStrikeDepositQuote(c *fiber.Ctx) error {
	if !s.strikeReady(c) {
		return nil
	}
	var body struct {
		Address  string `json:"address"`
		Lovelace string `json:"lovelace"`
	}
	if err := c.BodyParser(&body); err != nil || body.Lovelace == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "address and lovelace required"})
	}
	cred := s.strikeCred(c, body.Address)
	if cred == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(c.UserContext(), 25*time.Second)
	defer cancel()
	raw, err := s.strike.DepositQuote(ctx, cred, body.Lovelace)
	if err != nil {
		return c.Status(fiber.StatusBadGateway).JSON(fiber.Map{"error": err.Error()})
	}
	c.Type("json")
	return c.Send(raw)
}

func (s *Server) handleStrikeDepositBuild(c *fiber.Ctx) error {
	if !s.strikeReady(c) {
		return nil
	}
	var body struct {
		Address     string   `json:"address"`
		RequestID   string   `json:"requestId"`
		UserAddress string   `json:"userAddress"`
		Utxos       []string `json:"utxos"`
	}
	if err := c.BodyParser(&body); err != nil || body.RequestID == "" || body.UserAddress == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "address, requestId and userAddress required"})
	}
	cred := s.strikeCred(c, body.Address)
	if cred == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(c.UserContext(), 30*time.Second)
	defer cancel()
	raw, err := s.strike.DepositBuildTx(ctx, cred, body.RequestID, body.UserAddress, body.Utxos)
	if err != nil {
		return c.Status(fiber.StatusBadGateway).JSON(fiber.Map{"error": err.Error()})
	}
	c.Type("json")
	return c.Send(raw)
}

func (s *Server) handleStrikeDepositConfirm(c *fiber.Ctx) error {
	if !s.strikeReady(c) {
		return nil
	}
	var body struct {
		Address   string `json:"address"`
		RequestID string `json:"requestId"`
		TxHash    string `json:"txHash"`
		AdaAmount string `json:"adaAmount"` // display-only
		UsdValue  string `json:"usdValue"`  // display-only
	}
	if err := c.BodyParser(&body); err != nil || body.RequestID == "" || body.TxHash == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "address, requestId and txHash required"})
	}
	cred := s.strikeCred(c, body.Address)
	if cred == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(c.UserContext(), 25*time.Second)
	defer cancel()
	raw, err := s.strike.DepositConfirm(ctx, cred, body.RequestID, body.TxHash)
	if err != nil {
		return c.Status(fiber.StatusBadGateway).JSON(fiber.Map{"error": err.Error()})
	}
	s.recordHistory(strike.HistoryEntry{
		Address: body.Address, Type: "deposit", Asset: "ADA",
		Amount: body.AdaAmount, UsdValue: body.UsdValue,
		TxHash: body.TxHash, RequestID: body.RequestID, Status: "confirmed",
	})
	c.Type("json")
	return c.Send(raw)
}

var _ = json.Marshal
