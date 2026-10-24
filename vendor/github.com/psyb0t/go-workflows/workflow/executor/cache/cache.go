package cache

import (
	"context"
	"time"

	"github.com/jellydator/ttlcache/v3"

	"github.com/psyb0t/go-workflows/backend/metrics"
	"github.com/psyb0t/go-workflows/core"
	"github.com/psyb0t/go-workflows/internal/metrickeys"
	"github.com/psyb0t/go-workflows/workflow/executor"
)

type lruCache struct {
	mc metrics.Client
	c  *ttlcache.Cache[string, executor.WorkflowExecutor]
}

func NewWorkflowExecutorLRUCache(mc metrics.Client, size int, expiration time.Duration) *lruCache {
	if size < 0 {
		size = 0
	}
	c := ttlcache.New(
		ttlcache.WithCapacity[string, executor.WorkflowExecutor](uint64(size)), // #nosec G115 -- Negative inputs are clamped above; every nonnegative int fits uint64.
		ttlcache.WithTTL[string, executor.WorkflowExecutor](expiration),
	)

	c.OnEviction(func(ctx context.Context, er ttlcache.EvictionReason, i *ttlcache.Item[string, executor.WorkflowExecutor]) {
		// Close the executor to allow it to clean up resources.
		i.Value().Close()

		reason := ""
		switch er {
		case ttlcache.EvictionReasonExpired:
			reason = "expired"
		case ttlcache.EvictionReasonCapacityReached:
			reason = "capacity"
		}

		mc.Counter(metrickeys.WorkflowInstanceCacheEviction, metrics.Tags{metrickeys.EvictionReason: reason}, 1)
	})

	return &lruCache{
		mc: mc,
		c:  c,
	}
}

func (lc *lruCache) Get(ctx context.Context, instance *core.WorkflowInstance) (executor.WorkflowExecutor, bool, error) {
	e := lc.c.Get(getKey(instance))
	if e != nil {
		return e.Value(), true, nil
	}

	return nil, false, nil
}

func (lc *lruCache) Store(ctx context.Context, instance *core.WorkflowInstance, executor executor.WorkflowExecutor) error {
	lc.c.Set(getKey(instance), executor, ttlcache.DefaultTTL)

	lc.mc.Gauge(metrickeys.WorkflowInstanceCacheSize, metrics.Tags{}, int64(lc.c.Len()))

	return nil
}

func (lc *lruCache) Evict(ctx context.Context, instance *core.WorkflowInstance) error {
	lc.c.Delete(getKey(instance))

	lc.mc.Gauge(metrickeys.WorkflowInstanceCacheSize, metrics.Tags{}, int64(lc.c.Len()))

	return nil
}

func (lc *lruCache) StartEviction(ctx context.Context) {
	go lc.c.Start()

	<-ctx.Done()

	lc.c.Stop()
}
