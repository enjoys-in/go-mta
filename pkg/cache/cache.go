package cache

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/enjoys-in/go-mta/pkg/logger"
)

// Cache abstracts a Dragonfly/Redis-compatible cache.
type Cache interface {
	Get(ctx context.Context, key string) (string, error)
	Set(ctx context.Context, key string, value string, ttl time.Duration) error
	Incr(ctx context.Context, key string) (int64, error)
	Expire(ctx context.Context, key string, ttl time.Duration) error
	Del(ctx context.Context, keys ...string) error
	Close() error
}

// DragonflyCache implements Cache using go-redis (Dragonfly-compatible).
type DragonflyCache struct {
	client *redis.Client
	log    *logger.Logger
}

// Options configures the Dragonfly/Redis connection.
type Options struct {
	Addr     string
	Username string // optional, leave empty if not required
	Password string
	DB       int
}

// New creates a DragonflyCache connected to the given address.
func New(opts Options) *DragonflyCache {
	c := redis.NewClient(&redis.Options{
		Addr:     opts.Addr,
		Username: opts.Username,
		Password: opts.Password,
		DB:       opts.DB,
	})
	return &DragonflyCache{
		client: c,
		log:    logger.New("cache"),
	}
}

func (d *DragonflyCache) Get(ctx context.Context, key string) (string, error) {
	return d.client.Get(ctx, key).Result()
}

func (d *DragonflyCache) Set(ctx context.Context, key string, value string, ttl time.Duration) error {
	return d.client.Set(ctx, key, value, ttl).Err()
}

func (d *DragonflyCache) Incr(ctx context.Context, key string) (int64, error) {
	return d.client.Incr(ctx, key).Result()
}

func (d *DragonflyCache) Expire(ctx context.Context, key string, ttl time.Duration) error {
	return d.client.Expire(ctx, key, ttl).Err()
}

func (d *DragonflyCache) Del(ctx context.Context, keys ...string) error {
	return d.client.Del(ctx, keys...).Err()
}

func (d *DragonflyCache) Close() error {
	return d.client.Close()
}

// Ping verifies the connection to Dragonfly/Redis.
func (d *DragonflyCache) Ping(ctx context.Context) error {
	return d.client.Ping(ctx).Err()
}
