package engine

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// Job status lifecycle.
const (
	StatusAwaitingFunding = "awaiting_funding" // temp address created, waiting for the user's funding tx
	StatusRunning         = "running"          // the loop is building the position
	StatusOpen            = "open"             // position fully built and live
	StatusReversing       = "reversing"        // unwinding (user-requested or post-failure)
	StatusReversed        = "reversed"         // unwound and swept back to the user
	StatusFailed          = "failed"           // a leg failed; funds recoverable via reverse
)

// Step records a single on-chain action taken while building or unwinding a
// position, so progress is observable and a reversal knows exactly what to undo.
type Step struct {
	Kind     string `json:"kind" bson:"kind"` // trade|supply|borrow|repay|withdraw|sweep|leg|batch|status
	Source   string `json:"source,omitempty" bson:"source,omitempty"`
	MarketID string `json:"marketId,omitempty" bson:"marketId,omitempty"`
	TxHash   string `json:"txHash,omitempty" bson:"txHash,omitempty"`
	// URL is the Cardanoscan link for TxHash, so the step can be followed
	// on-chain straight from the DB record or the UI.
	URL    string    `json:"url,omitempty" bson:"url,omitempty"`
	Amount float64   `json:"amount,omitempty" bson:"amount,omitempty"`
	Note   string    `json:"note,omitempty" bson:"note,omitempty"`
	At     time.Time `json:"at" bson:"at"`
}

// Job is a tracked leverage position (long or short) and the temp wallet that
// holds it. The signing key lives only in Seal (encrypted).
type Job struct {
	ID                 string     `json:"id" bson:"_id"`
	Owner              string     `json:"owner" bson:"owner"` // user's bech32 address (sweep destination)
	Direction          Direction  `json:"direction" bson:"direction"`
	CollateralUnit     string     `json:"collateralUnit" bson:"collateralUnit"` // base asset id ("" = ADA)
	TargetUnit         string     `json:"targetUnit" bson:"targetUnit"`         // token being longed/shorted
	InitialAmount      float64    `json:"initialAmount" bson:"initialAmount"`
	Leverage           float64    `json:"leverage" bson:"leverage"`
	Plan               *Plan      `json:"plan" bson:"plan"`
	TempAddress        string     `json:"tempAddress" bson:"tempAddress"`
	Seal               *SealedKey `json:"-" bson:"seal"` // never serialized to API clients
	FundingLovelace    int64      `json:"fundingLovelace" bson:"fundingLovelace"`
	FeeReserveLovelace int64      `json:"feeReserveLovelace" bson:"feeReserveLovelace"` // ADA held for tx fees
	Status             string     `json:"status" bson:"status"`
	Steps              []Step     `json:"steps" bson:"steps"`
	Exposure           float64    `json:"exposure" bson:"exposure"`
	HealthFactor       float64    `json:"healthFactor" bson:"healthFactor"`
	// Health tracking — refreshed by the background monitor from DexHunter's
	// average price. EntryPrice is ADA per target token at open; CurrentPrice
	// is the latest; PnLPct is the position's mark-to-market P&L on the user's
	// capital; HealthUpdatedAt stamps the last refresh.
	EntryPrice      float64   `json:"entryPrice,omitempty" bson:"entryPrice,omitempty"`
	CurrentPrice    float64   `json:"currentPrice,omitempty" bson:"currentPrice,omitempty"`
	PnLPct          float64   `json:"pnlPct,omitempty" bson:"pnlPct,omitempty"`
	LiqThreshold    float64   `json:"liqThreshold,omitempty" bson:"liqThreshold,omitempty"`
	HealthUpdatedAt time.Time `json:"healthUpdatedAt,omitempty" bson:"healthUpdatedAt,omitempty"`
	Error           string    `json:"error,omitempty" bson:"error,omitempty"`
	CreatedAt       time.Time `json:"createdAt" bson:"createdAt"`
	UpdatedAt       time.Time `json:"updatedAt" bson:"updatedAt"`
}

// JobStore persists leverage jobs. Implementations must be safe for
// concurrent use.
type JobStore interface {
	Save(ctx context.Context, job *Job) error
	Get(ctx context.Context, id string) (*Job, error)
	ListByOwner(ctx context.Context, owner string) ([]*Job, error)
	// ListByStatus returns every job in a given status, across owners — used by
	// the background health monitor to find open positions.
	ListByStatus(ctx context.Context, status string) ([]*Job, error)
}

// ErrJobNotFound is returned by Get when no job matches the id.
var ErrJobNotFound = errors.New("engine: job not found")

// --- in-memory store (fallback when Mongo is unreachable) -------------------

type MemoryJobStore struct {
	mu   sync.RWMutex
	jobs map[string]*Job
}

func NewMemoryJobStore() *MemoryJobStore {
	return &MemoryJobStore{jobs: make(map[string]*Job)}
}

func (m *MemoryJobStore) Save(_ context.Context, job *Job) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := *job
	m.jobs[job.ID] = &cp
	return nil
}

func (m *MemoryJobStore) Get(_ context.Context, id string) (*Job, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	j, ok := m.jobs[id]
	if !ok {
		return nil, ErrJobNotFound
	}
	cp := *j
	return &cp, nil
}

func (m *MemoryJobStore) ListByOwner(_ context.Context, owner string) ([]*Job, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []*Job
	for _, j := range m.jobs {
		if j.Owner == owner {
			cp := *j
			out = append(out, &cp)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

func (m *MemoryJobStore) ListByStatus(_ context.Context, status string) ([]*Job, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []*Job
	for _, j := range m.jobs {
		if j.Status == status {
			cp := *j
			out = append(out, &cp)
		}
	}
	return out, nil
}

// --- Mongo store ------------------------------------------------------------

const leverageJobsColl = "leverage_jobs"

type MongoJobStore struct {
	client *mongo.Client
	coll   *mongo.Collection
}

// NewMongoJobStore dials Mongo, pings, and returns a store backed by the
// leverage_jobs collection.
func NewMongoJobStore(ctx context.Context, uri, dbName string) (*MongoJobStore, error) {
	if uri == "" {
		return nil, errors.New("engine store: empty Mongo URI")
	}
	connectCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	client, err := mongo.Connect(connectCtx, options.Client().ApplyURI(uri))
	if err != nil {
		return nil, fmt.Errorf("engine store: connect: %w", err)
	}
	if err := client.Ping(connectCtx, nil); err != nil {
		_ = client.Disconnect(context.Background())
		return nil, fmt.Errorf("engine store: ping: %w", err)
	}
	coll := client.Database(dbName).Collection(leverageJobsColl)
	_, _ = coll.Indexes().CreateOne(connectCtx, mongo.IndexModel{
		Keys: bson.D{{Key: "owner", Value: 1}, {Key: "createdAt", Value: -1}},
	})
	return &MongoJobStore{client: client, coll: coll}, nil
}

func (s *MongoJobStore) Close() error { return s.client.Disconnect(context.Background()) }

func (s *MongoJobStore) Save(ctx context.Context, job *Job) error {
	job.UpdatedAt = time.Now().UTC()
	_, err := s.coll.UpdateByID(ctx, job.ID, bson.M{"$set": job}, options.Update().SetUpsert(true))
	return err
}

func (s *MongoJobStore) Get(ctx context.Context, id string) (*Job, error) {
	var job Job
	err := s.coll.FindOne(ctx, bson.M{"_id": id}).Decode(&job)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrJobNotFound
	}
	if err != nil {
		return nil, err
	}
	return &job, nil
}

func (s *MongoJobStore) ListByOwner(ctx context.Context, owner string) ([]*Job, error) {
	cur, err := s.coll.Find(ctx, bson.M{"owner": owner}, options.Find().SetSort(bson.D{{Key: "createdAt", Value: -1}}))
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var out []*Job
	if err := cur.All(ctx, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *MongoJobStore) ListByStatus(ctx context.Context, status string) ([]*Job, error) {
	cur, err := s.coll.Find(ctx, bson.M{"status": status})
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var out []*Job
	if err := cur.All(ctx, &out); err != nil {
		return nil, err
	}
	return out, nil
}
