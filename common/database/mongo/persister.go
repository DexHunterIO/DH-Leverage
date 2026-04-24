// Package mongo provides a MongoDB-backed implementation of
// sources.Persister. Each successful upstream FetchMarkets call upserts
// every returned market by (source, poolId) so the database always reflects
// the latest snapshot, with an updatedAt timestamp tracking freshness.
package mongo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"

	"dh-leverage/common/sources"
)

const (
	defaultDatabase = "dh-leverage"
	marketsCollName = "markets"
)

// Persister implements sources.Persister against a MongoDB collection.
type Persister struct {
	client *mongo.Client
	coll   *mongo.Collection
}

// NewMarketsPersister dials Mongo using the connection URI, pings the
// server, ensures the (source, poolId) unique index exists, and returns a
// ready persister. Caller must Close() on shutdown.
func NewMarketsPersister(ctx context.Context, uri string) (*Persister, error) {
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

	dbName := databaseFromURI(uri)
	coll := client.Database(dbName).Collection(marketsCollName)

	// Ensure unique index on (source, poolId).
	if _, err := coll.Indexes().CreateOne(connectCtx, mongo.IndexModel{
		Keys:    bson.D{{Key: "source", Value: 1}, {Key: "poolId", Value: 1}},
		Options: options.Index().SetUnique(true).SetName("source_poolId_unique"),
	}); err != nil {
		// Don't hard-fail if the index already exists with different options.
		// Persister is best-effort by design.
	}

	return &Persister{client: client, coll: coll}, nil
}

func (p *Persister) Close() error {
	return p.client.Disconnect(context.Background())
}

func (p *Persister) PersistMarkets(ctx context.Context, source string, markets []sources.Market) error {
	if len(markets) == 0 {
		return nil
	}
	models := make([]mongo.WriteModel, 0, len(markets))
	now := time.Now().UTC()
	for _, m := range markets {
		doc, err := toBSON(m)
		if err != nil {
			return err
		}
		doc["updatedAt"] = now
		models = append(models,
			mongo.NewUpdateOneModel().
				SetFilter(bson.M{"source": m.Source, "poolId": m.PoolID}).
				SetUpdate(bson.M{"$set": doc}).
				SetUpsert(true),
		)
	}
	_, err := p.coll.BulkWrite(ctx, models, options.BulkWrite().SetOrdered(false))
	return err
}

// toBSON converts a Market to bson.M via a JSON round-trip so the
// camelCase JSON field names are preserved in Mongo without having to
// duplicate every tag with `bson:"…"`.
func toBSON(v any) (bson.M, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	return bson.M(m), nil
}

// databaseFromURI extracts the database name from a Mongo URI's path
// component, falling back to defaultDatabase when none is present.
func databaseFromURI(uri string) string {
	u, err := url.Parse(uri)
	if err != nil {
		return defaultDatabase
	}
	name := strings.TrimPrefix(u.Path, "/")
	if name == "" {
		return defaultDatabase
	}
	return name
}
