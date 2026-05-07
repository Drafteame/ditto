package main

import (
	"sync"
	"testing"
	"time"
)

type fakeClock struct {
	mu        sync.Mutex
	now       time.Time
	timers    []*fakeTimer
	nextID    int
	fireOrder int
}

type fakeTimer struct {
	clock    *fakeClock
	id       int
	ch       chan time.Time
	deadline time.Time
	active   bool
	firedAt  time.Time
	firedSeq int
}

func newFakeClock(start time.Time) *fakeClock {
	return &fakeClock{now: start}
}

func testClockStart() time.Time {
	return time.Date(2026, 5, 7, 12, 0, 0, 0, time.UTC)
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) NewTimer(d time.Duration) Timer {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nextID++
	t := &fakeTimer{
		clock:    c,
		id:       c.nextID,
		ch:       make(chan time.Time, 1),
		deadline: c.now.Add(d),
		active:   true,
	}
	c.timers = append(c.timers, t)
	return t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	target := c.now.Add(d)
	for {
		next := c.nextDueTimerLocked(target)
		if next == nil {
			break
		}
		c.now = next.deadline
		next.fireLocked(c.now)
	}
	c.now = target
	c.mu.Unlock()
}

func (c *fakeClock) nextDueTimerLocked(target time.Time) *fakeTimer {
	var next *fakeTimer
	for _, timer := range c.timers {
		if !timer.active || timer.deadline.After(target) {
			continue
		}
		if next == nil || timer.deadline.Before(next.deadline) || (timer.deadline.Equal(next.deadline) && timer.id < next.id) {
			next = timer
		}
	}
	return next
}

func (t *fakeTimer) C() <-chan time.Time {
	return t.ch
}

func (t *fakeTimer) Stop() bool {
	t.clock.mu.Lock()
	defer t.clock.mu.Unlock()
	wasActive := t.active
	t.active = false
	return wasActive
}

func (t *fakeTimer) Reset(d time.Duration) bool {
	t.clock.mu.Lock()
	defer t.clock.mu.Unlock()
	wasActive := t.active
	t.active = true
	t.deadline = t.clock.now.Add(d)
	t.firedAt = time.Time{}
	t.firedSeq = 0
	select {
	case <-t.ch:
	default:
	}
	return wasActive
}

func (t *fakeTimer) fireLocked(at time.Time) {
	if !t.active {
		return
	}
	t.active = false
	t.clock.fireOrder++
	t.firedAt = at
	t.firedSeq = t.clock.fireOrder
	select {
	case t.ch <- at:
	default:
	}
}

func TestFakeClockAdvanceFiresTimersChronologically(t *testing.T) {
	start := testClockStart()
	clock := newFakeClock(start)
	first := clock.NewTimer(20 * time.Millisecond).(*fakeTimer)
	second := clock.NewTimer(10 * time.Millisecond).(*fakeTimer)
	third := clock.NewTimer(10 * time.Millisecond).(*fakeTimer)

	clock.Advance(20 * time.Millisecond)

	if !second.firedAt.Equal(start.Add(10 * time.Millisecond)) {
		t.Fatalf("second timer fired at %s", second.firedAt)
	}
	if !third.firedAt.Equal(start.Add(10 * time.Millisecond)) {
		t.Fatalf("third timer fired at %s", third.firedAt)
	}
	if !first.firedAt.Equal(start.Add(20 * time.Millisecond)) {
		t.Fatalf("first timer fired at %s", first.firedAt)
	}
	if !(second.firedSeq < third.firedSeq && third.firedSeq < first.firedSeq) {
		t.Fatalf("unexpected fire order: second=%d third=%d first=%d", second.firedSeq, third.firedSeq, first.firedSeq)
	}
}

func TestFakeTimerStopAndReset(t *testing.T) {
	start := testClockStart()
	clock := newFakeClock(start)
	timer := clock.NewTimer(10 * time.Millisecond).(*fakeTimer)
	if !timer.Stop() {
		t.Fatal("expected active timer to stop")
	}
	clock.Advance(10 * time.Millisecond)
	assertNoTimerValue(t, timer)

	if timer.Reset(20 * time.Millisecond) {
		t.Fatal("reset after stop should report inactive timer")
	}
	clock.Advance(19 * time.Millisecond)
	assertNoTimerValue(t, timer)
	clock.Advance(time.Millisecond)
	if got := <-timer.C(); !got.Equal(start.Add(30 * time.Millisecond)) {
		t.Fatalf("timer fired at %s", got)
	}
}

func assertNoTimerValue(t *testing.T, timer Timer) {
	t.Helper()
	select {
	case got := <-timer.C():
		t.Fatalf("unexpected timer value %s", got)
	default:
	}
}
