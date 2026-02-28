package checknodehealth

import (
	"sync"
	"time"

	"k8s.io/klog/v2"
)

const (
	// DefaultCircuitBreakerThreshold is the number of consecutive unhealthy nodes
	// within the monitoring window that triggers the circuit breaker.
	DefaultCircuitBreakerThreshold = 3

	// DefaultCircuitBreakerWindow is the time window in which consecutive unhealthy
	// node events are counted. If the threshold is reached within this window, the circuit opens.
	DefaultCircuitBreakerWindow = 15 * time.Minute

	// DefaultCircuitBreakerCooldown is the duration the circuit breaker stays open
	// before allowing node condition updates again.
	DefaultCircuitBreakerCooldown = 10 * time.Minute
)

// NodeConditionCircuitBreaker implements a circuit breaker pattern for node health condition updates.
//
// When N consecutive nodes are marked unhealthy within a short time window, this likely indicates
// a systemic issue (e.g., control plane problem) rather than individual node failures.
// The circuit breaker stops setting unhealthy conditions on new nodes to prevent
// cascading effects, and resumes after a cooldown period.
//
// A healthy node result resets the consecutive counter, so the circuit only trips when
// N unhealthy results occur in a row without any healthy result in between.
//
// States:
//   - Closed (normal): Node condition updates are allowed. Consecutive unhealthy events are tracked.
//   - Open (tripped): Node condition updates are blocked. Transitions back to closed after cooldown.
type NodeConditionCircuitBreaker struct {
	mu sync.Mutex

	// Configuration
	threshold int           // Number of consecutive unhealthy events to trip the breaker
	window    time.Duration // Time window for counting consecutive events
	cooldown  time.Duration // Duration to stay open before resetting

	// State
	consecutiveUnhealthy []time.Time // Timestamps of consecutive unhealthy node condition updates
	openedAt             *time.Time  // When the circuit breaker was opened; nil if closed

	// For testing
	nowFunc func() time.Time
}

// NewNodeConditionCircuitBreaker creates a new circuit breaker with the given parameters.
func NewNodeConditionCircuitBreaker(threshold int, window, cooldown time.Duration) *NodeConditionCircuitBreaker {
	return &NodeConditionCircuitBreaker{
		threshold: threshold,
		window:    window,
		cooldown:  cooldown,
		nowFunc:   time.Now,
	}
}

// Allow checks whether a node condition update is allowed.
// Returns true if the circuit is closed (updates allowed), false if open (updates blocked).
func (cb *NodeConditionCircuitBreaker) Allow() bool {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	now := cb.nowFunc()

	// If circuit is open, check if cooldown has elapsed
	if cb.openedAt != nil {
		if now.Sub(*cb.openedAt) >= cb.cooldown {
			klog.InfoS("Circuit breaker cooldown elapsed, resetting to closed state",
				"openedAt", cb.openedAt,
				"cooldown", cb.cooldown,
			)
			cb.reset()
			return true
		}
		klog.InfoS("Circuit breaker is open, blocking node condition update",
			"openedAt", cb.openedAt,
			"cooldownRemaining", cb.cooldown-now.Sub(*cb.openedAt),
		)
		return false
	}

	return true
}

// RecordUnhealthyNode records that an unhealthy condition was set on a node.
// If this causes the consecutive threshold to be reached within the window, the circuit opens.
func (cb *NodeConditionCircuitBreaker) RecordUnhealthyNode() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	now := cb.nowFunc()

	// Add the new event
	cb.consecutiveUnhealthy = append(cb.consecutiveUnhealthy, now)

	// Prune events outside the window
	cb.consecutiveUnhealthy = pruneExpiredEvents(cb.consecutiveUnhealthy, cb.window, now)

	klog.InfoS("Recorded unhealthy node event",
		"consecutiveInWindow", len(cb.consecutiveUnhealthy),
		"threshold", cb.threshold,
	)

	// Check if threshold is reached
	if len(cb.consecutiveUnhealthy) >= cb.threshold {
		klog.InfoS("Circuit breaker threshold reached, opening circuit",
			"consecutiveUnhealthy", len(cb.consecutiveUnhealthy),
			"threshold", cb.threshold,
			"window", cb.window,
		)
		cb.openedAt = &now
	}
}

// RecordHealthyNode records that a healthy result was observed.
// This resets the consecutive unhealthy counter since the streak is broken.
func (cb *NodeConditionCircuitBreaker) RecordHealthyNode() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	if len(cb.consecutiveUnhealthy) > 0 {
		klog.InfoS("Healthy node observed, resetting consecutive unhealthy counter",
			"previousCount", len(cb.consecutiveUnhealthy),
		)
		cb.consecutiveUnhealthy = nil
	}
}

// reset clears the circuit breaker state back to closed.
// Must be called with the mutex held.
func (cb *NodeConditionCircuitBreaker) reset() {
	cb.openedAt = nil
	cb.consecutiveUnhealthy = nil
}

// pruneExpiredEvents returns events that are within the monitoring window,
// removing any that have expired. It does not modify the input slice.
func pruneExpiredEvents(events []time.Time, window time.Duration, now time.Time) []time.Time {
	cutoff := now.Add(-window)
	i := 0
	for i < len(events) && events[i].Before(cutoff) {
		i++
	}
	return events[i:]
}
