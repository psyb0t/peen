package client

import (
	"context"

	"github.com/psyb0t/go-workflows/backend"
)

// GetStats returns backend stats.
func (c *Client) GetStats(ctx context.Context) (*backend.Stats, error) {
	return c.backend.GetStats(ctx)
}
