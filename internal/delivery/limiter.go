package delivery

import (
	"context"
	"net/url"
	"sync"
	"time"
)

type targetState struct {
	mu         sync.Mutex
	nextPermit time.Time
	active     int
	rate       int
	concurrent int
}

type TargetController struct {
	mu      sync.Mutex
	targets map[string]*targetState
}

func NewTargetController() *TargetController {
	return &TargetController{targets: make(map[string]*targetState)}
}

func (c *TargetController) Acquire(ctx context.Context, rawURL string, rate, concurrency int) (func(), error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	key := parsed.Scheme + "://" + parsed.Host
	c.mu.Lock()
	state, ok := c.targets[key]
	if !ok {
		state = &targetState{rate: rate, concurrent: concurrency}
		c.targets[key] = state
	}
	c.mu.Unlock()

	for {
		state.mu.Lock()
		if rate < state.rate {
			state.rate = rate
		}
		if concurrency < state.concurrent {
			state.concurrent = concurrency
		}
		if state.active < state.concurrent {
			state.active++
			break
		}
		state.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(5 * time.Millisecond):
		}
	}
	release := func() {
		state.mu.Lock()
		state.active--
		state.mu.Unlock()
	}
	interval := time.Second / time.Duration(state.rate)
	now := time.Now()
	permitAt := now
	if state.nextPermit.After(now) {
		permitAt = state.nextPermit
	}
	state.nextPermit = permitAt.Add(interval)
	state.mu.Unlock()

	timer := time.NewTimer(time.Until(permitAt))
	defer timer.Stop()
	select {
	case <-timer.C:
		return release, nil
	case <-ctx.Done():
		release()
		return nil, ctx.Err()
	}
}
