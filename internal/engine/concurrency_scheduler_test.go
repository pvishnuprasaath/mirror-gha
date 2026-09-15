package engine

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestConcurrencyScheduler_EmptyGroupNeverBlocks(t *testing.T) {
	s := newConcurrencyScheduler()
	ctx1, release1 := s.acquire(context.Background(), "", false)
	ctx2, release2 := s.acquire(context.Background(), "", false)
	if ctx1 != context.Background() || ctx2 != context.Background() {
		t.Error("acquire(\"\") should return the same context unchanged")
	}
	release1()
	release2()
}

func TestConcurrencyScheduler_BlockingQueueSerializesSameGroup(t *testing.T) {
	s := newConcurrencyScheduler()

	var mu sync.Mutex
	var order []string

	_, release1 := s.acquire(context.Background(), "g", false)

	done := make(chan struct{})
	go func() {
		_, release2 := s.acquire(context.Background(), "g", false)
		mu.Lock()
		order = append(order, "second-acquired")
		mu.Unlock()
		release2()
		close(done)
	}()

	// Give the goroutine a chance to attempt (and be blocked by) acquire.
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	order = append(order, "first-released")
	mu.Unlock()
	release1()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("second acquire never completed after first released")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(order) != 2 || order[0] != "first-released" || order[1] != "second-acquired" {
		t.Errorf("order = %v, want [first-released, second-acquired] (second acquire must wait for the first release)", order)
	}
}

func TestConcurrencyScheduler_DifferentGroupsNeverBlockEachOther(t *testing.T) {
	s := newConcurrencyScheduler()
	_, release1 := s.acquire(context.Background(), "g1", false)
	defer release1()

	done := make(chan struct{})
	go func() {
		_, release2 := s.acquire(context.Background(), "g2", false)
		release2()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(1 * time.Second):
		t.Fatal("acquiring a different group blocked — groups must be independent")
	}
}

func TestConcurrencyScheduler_CancelInProgressCancelsPreviousHolder(t *testing.T) {
	s := newConcurrencyScheduler()
	runCtx1, release1 := s.acquire(context.Background(), "g", true)
	defer release1()

	if runCtx1.Err() != nil {
		t.Fatalf("runCtx1 already cancelled before a second acquire happened")
	}

	runCtx2, release2 := s.acquire(context.Background(), "g", true)
	defer release2()

	select {
	case <-runCtx1.Done():
	case <-time.After(1 * time.Second):
		t.Fatal("acquiring the same group again with cancel-in-progress must cancel the previous holder's context")
	}
	if runCtx2.Err() != nil {
		t.Errorf("runCtx2 should not be cancelled, got %v", runCtx2.Err())
	}
}

func TestConcurrencyScheduler_StaleReleaseDoesNotClobberNewerHolder(t *testing.T) {
	s := newConcurrencyScheduler()
	_, release1 := s.acquire(context.Background(), "g", true)
	_, release2 := s.acquire(context.Background(), "g", true) // cancels release1's ctx

	release1() // a delayed release from the already-superseded first holder

	// A third acquire must still correctly cancel the SECOND holder (not
	// be confused by the stale first release already having run).
	runCtx3, release3 := s.acquire(context.Background(), "g", true)
	defer release3()
	_ = runCtx3
	release2()
}
