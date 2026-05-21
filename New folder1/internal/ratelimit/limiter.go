// Package ratelimit implements a sliding-window rate limiter.
//
// Design: each user_id is sharded into a separate bucket protected by its own mutex lock.
// On every call to Allow(), expired entries (older than 1 minute) are pruned
// in-place. A background worker periodically cleans up inactive buckets to prevent memory leaks.
package ratelimit

import (
	"sync"
	"time"
)

const (
	WindowDuration = time.Minute
	MaxRequests    = 5
	ShardCount     = 32
)

// userBucket stores the sliding-window state for one user.
type userBucket struct {
	// accepted holds the wall-clock time of each request that was allowed
	// within the current window. Entries are appended on accept and pruned
	// on every call so the slice never grows beyond MaxRequests+1 elements.
	accepted []time.Time

	// rejected is a lifetime (cumulative) counter of rate-limited requests.
	rejected int64

	// lastActive is the timestamp of the last activity from this user.
	lastActive time.Time
}

// prune removes timestamps that have fallen outside the rolling window.
// It reuses the underlying array to avoid allocations.
func (b *userBucket) prune(now time.Time) {
	cutoff := now.Add(-WindowDuration)
	keep := b.accepted[:0]
	for _, t := range b.accepted {
		if t.After(cutoff) {
			keep = append(keep, t)
		}
	}
	b.accepted = keep
}

type limiterShard struct {
	mu      sync.Mutex
	buckets map[string]*userBucket
}

// SlidingWindowLimiter is safe for concurrent use by multiple goroutines.
type SlidingWindowLimiter struct {
	shards [ShardCount]*limiterShard
	stop   chan struct{}
}

// NewSlidingWindowLimiter returns an initialised limiter.
func NewSlidingWindowLimiter() *SlidingWindowLimiter {
	l := &SlidingWindowLimiter{
		stop: make(chan struct{}),
	}
	for i := 0; i < ShardCount; i++ {
		l.shards[i] = &limiterShard{
			buckets: make(map[string]*userBucket),
		}
	}
	go l.startCleanupWorker(5 * time.Minute)
	return l
}

// Close stops the background cleanup worker.
func (l *SlidingWindowLimiter) Close() {
	close(l.stop)
}

func (l *SlidingWindowLimiter) getShard(userID string) *limiterShard {
	// Simple FNV-1a hash to shard the key space
	hash := uint32(2166136261)
	const prime = uint32(16777619)
	for i := 0; i < len(userID); i++ {
		hash ^= uint32(userID[i])
		hash *= prime
	}
	return l.shards[hash%ShardCount]
}

// AllowResult carries the outcome of an Allow call.
type AllowResult struct {
	Allowed            bool
	AcceptedInWindow   int
	RejectedCumulative int64
}

// Allow decides whether a request by userID is within the rate limit.
// It records the decision atomically under a mutex so parallel calls for
// the same user_id are handled correctly.
func (l *SlidingWindowLimiter) Allow(userID string) AllowResult {
	shard := l.getShard(userID)
	shard.mu.Lock()
	defer shard.mu.Unlock()

	now := time.Now()

	b, ok := shard.buckets[userID]
	if !ok {
		b = &userBucket{}
		shard.buckets[userID] = b
	}

	b.prune(now)
	b.lastActive = now

	if len(b.accepted) >= MaxRequests {
		b.rejected++
		return AllowResult{
			Allowed:            false,
			AcceptedInWindow:   len(b.accepted),
			RejectedCumulative: b.rejected,
		}
	}

	b.accepted = append(b.accepted, now)
	return AllowResult{
		Allowed:            true,
		AcceptedInWindow:   len(b.accepted),
		RejectedCumulative: b.rejected,
	}
}

// UserStats is the per-user snapshot returned by Stats.
type UserStats struct {
	UserID             string `json:"user_id"`
	AcceptedInWindow   int    `json:"accepted_in_window"`
	RejectedCumulative int64  `json:"rejected_cumulative"`
}

// Stats returns a snapshot of current statistics for every known user.
// Expired entries are pruned before counting so the window counts are accurate.
func (l *SlidingWindowLimiter) Stats() []UserStats {
	now := time.Now()
	var out []UserStats

	for _, shard := range l.shards {
		shard.mu.Lock()
		for uid, b := range shard.buckets {
			b.prune(now)
			out = append(out, UserStats{
				UserID:             uid,
				AcceptedInWindow:   len(b.accepted),
				RejectedCumulative: b.rejected,
			})
		}
		shard.mu.Unlock()
	}
	return out
}

func (l *SlidingWindowLimiter) startCleanupWorker(interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			l.cleanup()
		case <-l.stop:
			return
		}
	}
}

func (l *SlidingWindowLimiter) cleanup() {
	now := time.Now()
	cutoff := now.Add(-WindowDuration)

	for _, shard := range l.shards {
		shard.mu.Lock()
		for uid, b := range shard.buckets {
			if b.lastActive.Before(cutoff) {
				delete(shard.buckets, uid)
			}
		}
		shard.mu.Unlock()
	}
}
