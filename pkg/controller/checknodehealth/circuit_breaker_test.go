package checknodehealth

import (
	"testing"
	"time"
)

func TestCircuitBreaker_AllowWhenClosed(t *testing.T) {
	cb := NewNodeConditionCircuitBreaker(3, 15*time.Minute, 10*time.Minute)

	if !cb.Allow() {
		t.Error("Expected Allow() to return true when circuit is closed")
	}
}

func TestCircuitBreaker_OpensAfterConsecutiveThreshold(t *testing.T) {
	cb := NewNodeConditionCircuitBreaker(3, 15*time.Minute, 10*time.Minute)
	now := time.Now()
	cb.nowFunc = func() time.Time { return now }

	// Record events below threshold
	cb.RecordUnhealthyNode()
	cb.RecordUnhealthyNode()

	if !cb.Allow() {
		t.Error("Expected Allow() to return true before threshold is reached")
	}

	// Third consecutive event should trip the breaker
	cb.RecordUnhealthyNode()

	if cb.Allow() {
		t.Error("Expected Allow() to return false after threshold is reached")
	}
	if !cb.IsOpen() {
		t.Error("Expected IsOpen() to return true after threshold is reached")
	}
}

func TestCircuitBreaker_HealthyNodeResetsConsecutiveCounter(t *testing.T) {
	cb := NewNodeConditionCircuitBreaker(3, 15*time.Minute, 10*time.Minute)
	now := time.Now()
	cb.nowFunc = func() time.Time { return now }

	// Record 2 unhealthy events
	cb.RecordUnhealthyNode()
	cb.RecordUnhealthyNode()

	// A healthy node breaks the consecutive streak
	cb.RecordHealthyNode()

	// Record 2 more unhealthy events (but only 2 consecutive now)
	cb.RecordUnhealthyNode()
	cb.RecordUnhealthyNode()

	// Should NOT trip because the streak was broken by the healthy node
	if !cb.Allow() {
		t.Error("Expected Allow() to return true - healthy node should have reset consecutive counter")
	}
	if cb.IsOpen() {
		t.Error("Expected circuit to remain closed - streak was broken by healthy node")
	}

	// One more makes it 3 consecutive — NOW it should trip
	cb.RecordUnhealthyNode()

	if cb.Allow() {
		t.Error("Expected Allow() to return false after 3 consecutive unhealthy")
	}
}

func TestCircuitBreaker_ResetsAfterCooldown(t *testing.T) {
	cb := NewNodeConditionCircuitBreaker(3, 15*time.Minute, 10*time.Minute)
	now := time.Now()
	cb.nowFunc = func() time.Time { return now }

	// Trip the breaker
	cb.RecordUnhealthyNode()
	cb.RecordUnhealthyNode()
	cb.RecordUnhealthyNode()

	if cb.Allow() {
		t.Error("Expected circuit to be open")
	}

	// Advance time past cooldown
	now = now.Add(10*time.Minute + 1*time.Second)

	if !cb.Allow() {
		t.Error("Expected Allow() to return true after cooldown elapsed")
	}
	if cb.IsOpen() {
		t.Error("Expected IsOpen() to return false after cooldown elapsed")
	}
}

func TestCircuitBreaker_ConsecutiveEventsOutsideWindowDontCount(t *testing.T) {
	cb := NewNodeConditionCircuitBreaker(3, 15*time.Minute, 10*time.Minute)
	now := time.Now()
	cb.nowFunc = func() time.Time { return now }

	// Record 2 events
	cb.RecordUnhealthyNode()
	cb.RecordUnhealthyNode()

	// Advance time past the window so old events expire
	now = now.Add(16 * time.Minute)

	// Record 1 more event - should NOT trip because old events expired
	cb.RecordUnhealthyNode()

	if !cb.Allow() {
		t.Error("Expected Allow() to return true when old events expired out of window")
	}
	if cb.IsOpen() {
		t.Error("Expected circuit to remain closed when old events expired")
	}
}

func TestCircuitBreaker_ConsecutiveEventsWithinWindowAccumulate(t *testing.T) {
	cb := NewNodeConditionCircuitBreaker(3, 15*time.Minute, 10*time.Minute)
	now := time.Now()
	cb.nowFunc = func() time.Time { return now }

	cb.RecordUnhealthyNode()
	now = now.Add(5 * time.Minute)
	cb.RecordUnhealthyNode()
	now = now.Add(5 * time.Minute)
	cb.RecordUnhealthyNode() // 3rd consecutive event within 15 min window

	if cb.Allow() {
		t.Error("Expected circuit to be open after 3 consecutive events within window")
	}
}

func TestCircuitBreaker_IsOpenResetsAfterCooldown(t *testing.T) {
	cb := NewNodeConditionCircuitBreaker(2, 15*time.Minute, 10*time.Minute)
	now := time.Now()
	cb.nowFunc = func() time.Time { return now }

	cb.RecordUnhealthyNode()
	cb.RecordUnhealthyNode()

	if !cb.IsOpen() {
		t.Error("Expected IsOpen() to return true")
	}

	// Advance past cooldown
	now = now.Add(11 * time.Minute)

	if cb.IsOpen() {
		t.Error("Expected IsOpen() to return false after cooldown")
	}

	// Should be able to record new events and trip again
	cb.RecordUnhealthyNode()
	cb.RecordUnhealthyNode()

	if !cb.IsOpen() {
		t.Error("Expected circuit to trip again after re-recording events")
	}
}

func TestCircuitBreaker_AllowResetsStateAfterCooldown(t *testing.T) {
	cb := NewNodeConditionCircuitBreaker(2, 15*time.Minute, 10*time.Minute)
	now := time.Now()
	cb.nowFunc = func() time.Time { return now }

	// Trip the breaker
	cb.RecordUnhealthyNode()
	cb.RecordUnhealthyNode()

	if cb.Allow() {
		t.Error("Expected circuit to be open")
	}

	// Advance past cooldown
	now = now.Add(11 * time.Minute)

	// Allow should return true and reset internal state
	if !cb.Allow() {
		t.Error("Expected Allow() to return true after cooldown")
	}

	// After reset, a single event should not trip the breaker
	cb.RecordUnhealthyNode()
	if !cb.Allow() {
		t.Error("Expected Allow() to return true after one event post-reset")
	}
}

func TestCircuitBreaker_ThresholdOne(t *testing.T) {
	cb := NewNodeConditionCircuitBreaker(1, 15*time.Minute, 10*time.Minute)
	now := time.Now()
	cb.nowFunc = func() time.Time { return now }

	cb.RecordUnhealthyNode()

	if cb.Allow() {
		t.Error("Expected circuit to open after single event with threshold=1")
	}
}

func TestCircuitBreaker_HealthyAfterOpenDoesNotClosePremature(t *testing.T) {
	cb := NewNodeConditionCircuitBreaker(2, 15*time.Minute, 10*time.Minute)
	now := time.Now()
	cb.nowFunc = func() time.Time { return now }

	// Trip the breaker
	cb.RecordUnhealthyNode()
	cb.RecordUnhealthyNode()

	if !cb.IsOpen() {
		t.Error("Expected circuit to be open")
	}

	// Recording a healthy node resets the consecutive counter
	// but does NOT close the circuit breaker - cooldown must elapse
	cb.RecordHealthyNode()

	if !cb.IsOpen() {
		t.Error("Expected circuit to remain open - cooldown has not elapsed")
	}
	if cb.Allow() {
		t.Error("Expected Allow() to return false - circuit is still open")
	}
}
