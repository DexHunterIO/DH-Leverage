package mongo

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"

	"dh-leverage/common/strike"
)

const historyCollName = "strike_history"

// StrikeHistory implements strike.History against a MongoDB collection so a
// user's deposit/withdrawal history survives restarts.
type StrikeHistory struct {
	client *mongo.Client
	coll   *mongo.Collection
}

// NewStrikeHistory dials Mongo, pings, ensures an index on (address, createdAt),
// and returns a ready history store. Caller must Close() on shutdown.
func NewStrikeHistory(ctx context.Context, uri string) (*StrikeHistory, error) {
	if uri == "" {
		return nil, errors.New("mongo: empty URI")
	}
	connectCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	client, err := mongo.Connect(connectCtx, options.Client().ApplyURI(uri))
	if err != nil {
		return nil, fmt.Errorf("mongo: connect: %w", err)
	}
	if err := client.Ping(connectCtx, nil); err != nil {
		_ = client.Disconnect(context.Background())
		return nil, fmt.Errorf("mongo: ping: %w", err)
	}

	coll := client.Database(databaseFromURI(uri)).Collection(historyCollName)
	_, _ = coll.Indexes().CreateOne(connectCtx, mongo.IndexModel{
		Keys:    bson.D{{Key: "address", Value: 1}, {Key: "createdAt", Value: -1}},
		Options: options.Index().SetName("address_createdAt"),
	})
	return &StrikeHistory{client: client, coll: coll}, nil
}

func (h *StrikeHistory) Close() error { return h.client.Disconnect(context.Background()) }

func (h *StrikeHistory) Record(ctx context.Context, e strike.HistoryEntry) error {
	if e.CreatedAt.IsZero() {
		e.CreatedAt = time.Now().UTC()
	}
	_, err := h.coll.InsertOne(ctx, e) // bson tags on HistoryEntry map the fields
	return err
}

func (h *StrikeHistory) List(ctx context.Context, address string) ([]strike.HistoryEntry, error) {
	cur, err := h.coll.Find(ctx, bson.M{"address": address},
		options.Find().SetSort(bson.D{{Key: "createdAt", Value: -1}}).SetLimit(200))
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var out []strike.HistoryEntry
	if err := cur.All(ctx, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (h *StrikeHistory) SettleWithdraw(ctx context.Context, address, withdrawID, txHash, status string) error {
	set := bson.M{"txHash": txHash}
	if status != "" {
		set["status"] = status
	}
	_, err := h.coll.UpdateMany(ctx,
		bson.M{"address": address, "type": "withdraw", "withdrawId": withdrawID},
		bson.M{"$set": set})
	return err
}
