package server

import (
	"context"
	"log"
	"time"

	"github.com/amiraq1/botlink/internal/store"
)

const (
	schedulerMinWait = 50 * time.Millisecond
	schedulerMaxWait = 30 * time.Second
	schedulerBackoff = time.Second
)

// turnScheduler fires mock replies and silent-turn timeouts at their exact due
// time instead of waiting for the 15-second safety sweep. It sleeps until the
// earliest deadline and is woken early by every group change.
type turnScheduler struct {
	st        *store.Store
	timeoutMs func() int64
	log       *log.Logger
	wakeCh    chan struct{}
	clock     func() int64
}

type dueGroup struct {
	id int64
	at int64
}

func newTurnScheduler(st *store.Store, timeoutMs func() int64, logger *log.Logger, clock func() int64) *turnScheduler {
	if clock == nil {
		clock = func() int64 { return time.Now().UnixMilli() }
	}
	return &turnScheduler{
		st:        st,
		timeoutMs: timeoutMs,
		log:       logger,
		wakeCh:    make(chan struct{}, 1),
		clock:     clock,
	}
}

func (t *turnScheduler) wake() {
	select {
	case t.wakeCh <- struct{}{}:
	default:
	}
}

func (t *turnScheduler) plan(ctx context.Context, now int64) ([]dueGroup, bool) {
	ids, err := t.st.RunningGroupIDs(ctx)
	if err != nil {
		t.log.Printf("scheduler: %v", err)
		return nil, false
	}
	out := make([]dueGroup, 0, len(ids))
	ok := true
	for _, id := range ids {
		due, err := t.st.NextDue(ctx, id, now, t.timeoutMs())
		if err != nil {
			t.log.Printf("scheduler: group %d: %v", id, err)
			ok = false
			continue
		}
		if due.Kind == "" {
			continue
		}
		out = append(out, dueGroup{id: id, at: due.DueAt})
	}
	return out, ok
}

func (t *turnScheduler) run(ctx context.Context) {
	timer := time.NewTimer(schedulerMaxWait)
	defer timer.Stop()
	for {
		now := t.clock()
		groups, planOK := t.plan(ctx, now)
		earliest := int64(0)
		if !planOK {
			earliest = now + int64(schedulerBackoff/time.Millisecond)
		}
		processed := false
		for _, g := range groups {
			if g.at <= now {
				_, err := t.st.ProcessDueGroup(ctx, g.id, now, t.timeoutMs())
				if err != nil {
					t.log.Printf("scheduler: group %d: %v", g.id, err)
				}
				processed = true
				continue
			}
			if earliest == 0 || g.at < earliest {
				earliest = g.at
			}
		}
		if processed {
			// Give the write transaction a moment before re-planning; this
			// also prevents a spin if a due action becomes a no-op.
			next := now + int64(schedulerMinWait/time.Millisecond)
			if earliest == 0 || next < earliest {
				earliest = next
			}
		}
		wait := schedulerMaxWait
		if earliest != 0 {
			d := time.Duration(earliest-now) * time.Millisecond
			if d < schedulerMinWait {
				d = schedulerMinWait
			}
			if d < wait {
				wait = d
			}
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(wait)
		select {
		case <-ctx.Done():
			return
		case <-t.wakeCh:
		case <-timer.C:
		}
	}
}
