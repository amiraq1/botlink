package server

import "sync"

// eventBroker is a tiny in-memory pub/sub keyed by group. Subscribers get
// coalesced wake-ups (a buffered channel of size one), never payloads, so a
// stream can never carry room or member data.
type eventBroker struct {
	mu          sync.Mutex
	subs        map[int64]map[*subscription]struct{}
	total       int
	maxTotal    int
	maxPerGroup int
}

type subscription struct {
	events  chan struct{}
	closing chan struct{}
	once    sync.Once
}

func (s *subscription) close() {
	s.once.Do(func() { close(s.closing) })
}

func newEventBroker(maxTotal, maxPerGroup int) *eventBroker {
	return &eventBroker{
		subs:        map[int64]map[*subscription]struct{}{},
		maxTotal:    maxTotal,
		maxPerGroup: maxPerGroup,
	}
}

func (b *eventBroker) subscribe(groupID int64) (*subscription, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	set := b.subs[groupID]
	if b.total >= b.maxTotal || len(set) >= b.maxPerGroup {
		return nil, false
	}
	sub := &subscription{
		events:  make(chan struct{}, 1),
		closing: make(chan struct{}),
	}
	if set == nil {
		set = make(map[*subscription]struct{}, 1)
		b.subs[groupID] = set
	}
	set[sub] = struct{}{}
	b.total++
	return sub, true
}

func (b *eventBroker) unsubscribe(groupID int64, sub *subscription) {
	b.mu.Lock()
	if set := b.subs[groupID]; set != nil {
		if _, ok := set[sub]; ok {
			delete(set, sub)
			b.total--
			if len(set) == 0 {
				delete(b.subs, groupID)
			}
		}
	}
	b.mu.Unlock()
	sub.close()
}

func (b *eventBroker) publish(groupID int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for sub := range b.subs[groupID] {
		select {
		case sub.events <- struct{}{}:
		default:
		}
	}
}

func (b *eventBroker) closeAll() {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, set := range b.subs {
		for sub := range set {
			sub.close()
		}
	}
	b.subs = map[int64]map[*subscription]struct{}{}
	b.total = 0
}
