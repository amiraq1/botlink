package server

import (
	"sync"
	"time"
)

// windowLimiter is a fixed-window counter with TTL eviction. It deliberately
// avoids an extra module dependency and keeps memory bounded even when an
// attacker sends a different key on every request.
type windowLimiter struct {
	mu        sync.Mutex
	windows   map[string]*rateWindow
	limit     int
	window    time.Duration
	maxKeys   int
	lastPrune time.Time
}

type rateWindow struct {
	count int
	reset time.Time
}

func newWindowLimiter(limit int, window time.Duration, maxKeys int) *windowLimiter {
	return &windowLimiter{
		windows:   make(map[string]*rateWindow),
		limit:     limit,
		window:    window,
		maxKeys:   maxKeys,
		lastPrune: time.Now(),
	}
}

// allow returns whether the request is permitted and, when denied, the
// number of seconds to send in Retry-After.
func (l *windowLimiter) allow(key string, now time.Time) (bool, int) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if now.Sub(l.lastPrune) >= time.Minute || len(l.windows) > l.maxKeys {
		for k, w := range l.windows {
			if now.After(w.reset) {
				delete(l.windows, k)
			}
		}
		// Hard cap: evict arbitrary oldest-ish entries when every key is hot.
		for len(l.windows) > l.maxKeys {
			for k := range l.windows {
				delete(l.windows, k)
				break
			}
		}
		l.lastPrune = now
	}

	w := l.windows[key]
	if w == nil || !now.Before(w.reset) {
		w = &rateWindow{reset: now.Add(l.window)}
		l.windows[key] = w
	}
	if w.count >= l.limit {
		retry := int(time.Until(w.reset).Seconds()) + 1
		if retry < 1 {
			retry = 1
		}
		return false, retry
	}
	w.count++
	return true, 0
}
