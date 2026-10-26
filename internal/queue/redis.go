package queue

import (
	"context"
	"github.com/redis/go-redis/v9"
)

type Client struct{ client *redis.Client }

func Open(addr, password string) *Client {
	return &Client{client: redis.NewClient(&redis.Options{Addr: addr, Password: password, ContextTimeoutEnabled: true, MaxRetries: -1})}
}
func (c *Client) Ping(ctx context.Context) error { return c.client.Ping(ctx).Err() }
func (c *Client) Close() error                   { return c.client.Close() }
