package teams

import (
	"context"
	"sync"
	"time"
)

// DedupCache handles message deduplication using a sync.Map with TTL eviction.
type DedupCache struct {
	m   sync.Map
	ttl time.Duration
}

type dedupEntry struct {
	expiresAt time.Time
}

// NewDedupCache creates a new DedupCache with the given TTL.
func NewDedupCache(ttl time.Duration) *DedupCache {
	return &DedupCache{
		ttl: ttl,
	}
}

// Seen checks if an activity ID has been seen within the TTL window.
func (d *DedupCache) Seen(activityID string) bool {
	v, ok := d.m.Load(activityID)
	if !ok {
		return false
	}
	entry := v.(dedupEntry)
	if time.Now().After(entry.expiresAt) {
		d.m.Delete(activityID)
		return false
	}
	return true
}

// Mark records that an activity ID has been processed.
func (d *DedupCache) Mark(activityID string) {
	d.m.Store(activityID, dedupEntry{expiresAt: time.Now().Add(d.ttl)})
}

// StartGC runs a background worker that periodically purges expired cache keys.
func (d *DedupCache) StartGC(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Minute)
	go func() {
		for {
			select {
			case <-ticker.C:
				now := time.Now()
				d.m.Range(func(k, v any) bool {
					if now.After(v.(dedupEntry).expiresAt) {
						d.m.Delete(k)
					}
					return true
				})
			case <-ctx.Done():
				ticker.Stop()
				return
			}
		}
	}()
}
