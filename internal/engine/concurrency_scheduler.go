package engine

import (
	"context"
	"sync"
)

// concurrencyScheduler enforces concurrency: group exclusivity among
// matrix combinations of the same job. act has no equivalent at all
// (confirmed via source — the field doesn't exist there), so this is
// mirror-gha's own design rather than a port.
//
// cancel-in-progress: false acquires a per-group 1-buffered channel as a
// mutex — a second combination in the same group blocks until the first
// releases (a real queue). cancel-in-progress: true instead cancels the
// group's currently-running combination's context before starting the
// new one, using a per-group generation counter so a delayed release
// from an already-superseded holder can never incorrectly clear a newer
// holder's state — a real correctness hazard a naive "one cancel func per
// group" implementation would hit under genuine concurrency.
type concurrencyScheduler struct {
	mu     sync.Mutex
	slots  map[string]chan struct{}
	gen    map[string]int
	cancel map[string]context.CancelFunc
}

func newConcurrencyScheduler() *concurrencyScheduler {
	return &concurrencyScheduler{
		slots:  map[string]chan struct{}{},
		gen:    map[string]int{},
		cancel: map[string]context.CancelFunc{},
	}
}

// acquire blocks (cancelInProgress false) or preempts the group's
// currently-running combination (cancelInProgress true). group == ""
// means no concurrency: field applies — ctx is returned unchanged and
// release is a no-op. The caller must always call release when done,
// exactly once, regardless of which path was taken.
func (s *concurrencyScheduler) acquire(ctx context.Context, group string, cancelInProgress bool) (runCtx context.Context, release func()) {
	if group == "" {
		return ctx, func() {}
	}

	if !cancelInProgress {
		s.mu.Lock()
		ch, ok := s.slots[group]
		if !ok {
			ch = make(chan struct{}, 1)
			s.slots[group] = ch
		}
		s.mu.Unlock()

		select {
		case ch <- struct{}{}:
		case <-ctx.Done():
			return ctx, func() {}
		}
		return ctx, func() { <-ch }
	}

	s.mu.Lock()
	if prevCancel, ok := s.cancel[group]; ok {
		prevCancel()
	}
	s.gen[group]++
	myGen := s.gen[group]
	newCtx, cancel := context.WithCancel(ctx)
	s.cancel[group] = cancel
	s.mu.Unlock()

	return newCtx, func() {
		s.mu.Lock()
		if s.gen[group] == myGen {
			delete(s.cancel, group)
			delete(s.gen, group)
		}
		s.mu.Unlock()
		cancel()
	}
}
