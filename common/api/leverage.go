package api

import (
	"context"
	"log"
	"time"

	"dh-leverage/common/engine"

	"github.com/gofiber/fiber/v2"
)

// engineReady guards every leverage handler: the engine is only constructed
// when the BlockFrost / encryption config is present, so without it we return
// a clear 503 instead of nil-panicking.
func (s *Server) engineReady(c *fiber.Ctx) bool {
	if s.engine == nil {
		msg := s.engineDisabledReason
		if msg == "" {
			// BLOCKFROST_PROJECT_ID and ENGINE_ENC_KEY are the only required
			// settings; the DexHunter partner key is optional.
			msg = "leverage engine disabled (set BLOCKFROST_PROJECT_ID and ENGINE_ENC_KEY, and ensure a durable Mongo store on mainnet)"
		}
		_ = c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{"error": msg})
		return false
	}
	return true
}

// handleLeverageQuote returns a non-mutating sizing + market preview.
func (s *Server) handleLeverageQuote(c *fiber.Ctx) error {
	if !s.engineReady(c) {
		return nil
	}
	var req engine.OpenRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid body: " + err.Error()})
	}
	ctx, cancel := context.WithTimeout(c.UserContext(), 25*time.Second)
	defer cancel()
	q, err := s.engine.Quote(ctx, req)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
	}
	return c.JSON(q)
}

// handleLeverageOpen creates the temp wallet + job and returns the unsigned
// funding tx for the user to sign.
func (s *Server) handleLeverageOpen(c *fiber.Ctx) error {
	if !s.engineReady(c) {
		return nil
	}
	var req engine.OpenRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid body: " + err.Error()})
	}
	ctx, cancel := context.WithTimeout(c.UserContext(), 30*time.Second)
	defer cancel()
	res, err := s.engine.Open(ctx, req)
	if err != nil {
		return c.Status(fiber.StatusBadGateway).JSON(fiber.Map{"error": err.Error()})
	}
	log.Printf("api: leverage open job=%s temp=%s", res.JobID, res.TempAddress)
	return c.JSON(res)
}

// handleLeverageStart begins the loop once the funding tx has landed.
func (s *Server) handleLeverageStart(c *fiber.Ctx) error {
	if !s.engineReady(c) {
		return nil
	}
	var body struct {
		JobID string `json:"jobId"`
	}
	if err := c.BodyParser(&body); err != nil || body.JobID == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "jobId required"})
	}
	ctx, cancel := context.WithTimeout(c.UserContext(), 30*time.Second)
	defer cancel()
	if err := s.engine.Start(ctx, body.JobID); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
	}
	return c.JSON(fiber.Map{"status": "running"})
}

// handleLeverageReverse interrupts a running loop / unwinds an open position
// and sweeps funds back to the user.
func (s *Server) handleLeverageReverse(c *fiber.Ctx) error {
	if !s.engineReady(c) {
		return nil
	}
	var body struct {
		JobID string `json:"jobId"`
	}
	if err := c.BodyParser(&body); err != nil || body.JobID == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "jobId required"})
	}
	ctx, cancel := context.WithTimeout(c.UserContext(), 15*time.Second)
	defer cancel()
	if err := s.engine.Reverse(ctx, body.JobID); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
	}
	return c.JSON(fiber.Map{"status": "reversing"})
}

// handleLeverageSweep sweeps loose funds at the temp address back to the user
// WITHOUT unwinding (no sell/repay/cancel) — for a closed position that left
// only trailing change.
func (s *Server) handleLeverageSweep(c *fiber.Ctx) error {
	if !s.engineReady(c) {
		return nil
	}
	var body struct {
		JobID string `json:"jobId"`
	}
	if err := c.BodyParser(&body); err != nil || body.JobID == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "jobId required"})
	}
	ctx, cancel := context.WithTimeout(c.UserContext(), 15*time.Second)
	defer cancel()
	if err := s.engine.Sweep(ctx, body.JobID); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
	}
	return c.JSON(fiber.Map{"status": "sweeping"})
}

// handleLeverageRetry resumes a failed position from its current on-chain state.
func (s *Server) handleLeverageRetry(c *fiber.Ctx) error {
	if !s.engineReady(c) {
		return nil
	}
	var body struct {
		JobID string `json:"jobId"`
	}
	if err := c.BodyParser(&body); err != nil || body.JobID == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "jobId required"})
	}
	ctx, cancel := context.WithTimeout(c.UserContext(), 15*time.Second)
	defer cancel()
	if err := s.engine.Retry(ctx, body.JobID); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
	}
	return c.JSON(fiber.Map{"status": "running"})
}

// handleLeverageStatus returns a single job snapshot.
func (s *Server) handleLeverageStatus(c *fiber.Ctx) error {
	if !s.engineReady(c) {
		return nil
	}
	ctx, cancel := context.WithTimeout(c.UserContext(), 15*time.Second)
	defer cancel()
	job, err := s.engine.Status(ctx, c.Params("id"))
	if err != nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": err.Error()})
	}
	return c.JSON(job)
}

// handleLeverageJobs lists an owner's tracked long/short positions.
func (s *Server) handleLeverageJobs(c *fiber.Ctx) error {
	if !s.engineReady(c) {
		return nil
	}
	owner := c.Query("address")
	if owner == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "address query param required"})
	}
	ctx, cancel := context.WithTimeout(c.UserContext(), 15*time.Second)
	defer cancel()
	jobs, err := s.engine.ListJobs(ctx, owner)
	if err != nil {
		return c.Status(fiber.StatusBadGateway).JSON(fiber.Map{"error": err.Error()})
	}
	return c.JSON(fiber.Map{"jobs": jobs})
}
