package helps

import (
	"context"
	"errors"
	"sync"
)

// ErrAdmissionQueueFull reports that both the active capacity and waiting
// queue are full. Callers should reject the request without penalizing the
// selected credential.
var ErrAdmissionQueueFull = errors.New("admission queue full")

// BoundedAdmission limits active work and bounds the number of callers waiting
// for capacity. It intentionally does not add a timeout; the caller's context
// owns cancellation while queued.
type BoundedAdmission struct {
	active chan struct{}
	queued chan struct{}
}

// NewBoundedAdmission creates an admission gate with fixed active and queued
// capacities. Both values must be positive.
func NewBoundedAdmission(maxActive, maxQueued int) *BoundedAdmission {
	if maxActive < 1 {
		maxActive = 1
	}
	if maxQueued < 1 {
		maxQueued = 1
	}
	return &BoundedAdmission{
		active: make(chan struct{}, maxActive),
		queued: make(chan struct{}, maxQueued),
	}
}

// Acquire reserves active capacity or waits in the bounded queue. The returned
// release function is idempotent.
func (a *BoundedAdmission) Acquire(ctx context.Context) (func(), error) {
	if a == nil {
		return func() {}, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}

	select {
	case a.active <- struct{}{}:
		return admissionRelease(a.active), nil
	default:
	}

	select {
	case a.queued <- struct{}{}:
	default:
		return nil, ErrAdmissionQueueFull
	}
	defer func() { <-a.queued }()

	select {
	case a.active <- struct{}{}:
		return admissionRelease(a.active), nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func admissionRelease(active chan struct{}) func() {
	var once sync.Once
	return func() {
		once.Do(func() { <-active })
	}
}
