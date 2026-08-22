package helps

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestBoundedAdmissionLimitsActiveAndQueuedWork(t *testing.T) {
	gate := NewBoundedAdmission(1, 1)
	releaseFirst, err := gate.Acquire(context.Background())
	if err != nil {
		t.Fatalf("acquire first: %v", err)
	}

	secondResult := make(chan error, 1)
	secondRelease := make(chan func(), 1)
	go func() {
		release, errAcquire := gate.Acquire(context.Background())
		if errAcquire == nil {
			secondRelease <- release
		}
		secondResult <- errAcquire
	}()

	deadline := time.Now().Add(time.Second)
	for len(gate.queued) != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := len(gate.queued); got != 1 {
		t.Fatalf("queued callers = %d, want 1", got)
	}

	if _, errAcquire := gate.Acquire(context.Background()); !errors.Is(errAcquire, ErrAdmissionQueueFull) {
		t.Fatalf("third acquire error = %v, want ErrAdmissionQueueFull", errAcquire)
	}

	releaseFirst()
	if errSecond := <-secondResult; errSecond != nil {
		t.Fatalf("queued acquire: %v", errSecond)
	}
	releaseQueued := <-secondRelease
	releaseQueued()
	releaseQueued()
}

func TestBoundedAdmissionCanceledWaiterLeavesQueue(t *testing.T) {
	gate := NewBoundedAdmission(1, 1)
	release, err := gate.Acquire(context.Background())
	if err != nil {
		t.Fatalf("acquire active: %v", err)
	}
	defer release()

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, errAcquire := gate.Acquire(ctx)
		result <- errAcquire
	}()

	deadline := time.Now().Add(time.Second)
	for len(gate.queued) != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	cancel()
	if errAcquire := <-result; !errors.Is(errAcquire, context.Canceled) {
		t.Fatalf("canceled acquire error = %v, want context.Canceled", errAcquire)
	}
	if got := len(gate.queued); got != 0 {
		t.Fatalf("queued callers after cancellation = %d, want 0", got)
	}
}
