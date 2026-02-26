package checknodehealth

import (
	"testing"
	"time"
)

// step describes a single action or assertion in a circuit breaker test scenario.
type step struct {
	action    string        // "unhealthy", "healthy", "advance", "allow"
	advance   time.Duration // used when action == "advance"
	wantAllow bool          // used when action == "allow"
}

func TestCircuitBreaker(t *testing.T) {
	tests := []struct {
		name      string
		threshold int
		window    time.Duration
		cooldown  time.Duration
		steps     []step
	}{
		{
			name:      "allow when closed",
			threshold: 3, window: 15 * time.Minute, cooldown: 10 * time.Minute,
			steps: []step{
				{action: "allow", wantAllow: true},
			},
		},
		{
			name:      "opens after consecutive threshold",
			threshold: 3, window: 15 * time.Minute, cooldown: 10 * time.Minute,
			steps: []step{
				{action: "unhealthy"},
				{action: "unhealthy"},
				{action: "allow", wantAllow: true},  // below threshold
				{action: "unhealthy"},               // hits threshold
				{action: "allow", wantAllow: false}, // circuit open
				{action: "allow", wantAllow: false}, // still open
			},
		},
		{
			name:      "healthy node resets consecutive counter",
			threshold: 3, window: 15 * time.Minute, cooldown: 10 * time.Minute,
			steps: []step{
				{action: "unhealthy"},
				{action: "unhealthy"},
				{action: "healthy"}, // breaks the streak
				{action: "unhealthy"},
				{action: "unhealthy"},
				{action: "allow", wantAllow: true},  // only 2 consecutive, not 3
				{action: "unhealthy"},               // 3rd consecutive
				{action: "allow", wantAllow: false}, // now tripped
			},
		},
		{
			name:      "resets after cooldown",
			threshold: 3, window: 15 * time.Minute, cooldown: 10 * time.Minute,
			steps: []step{
				{action: "unhealthy"},
				{action: "unhealthy"},
				{action: "unhealthy"},
				{action: "allow", wantAllow: false},
				{action: "advance", advance: 10*time.Minute + 1*time.Second},
				{action: "allow", wantAllow: true}, // cooldown elapsed
				{action: "allow", wantAllow: true}, // still open after reset
			},
		},
		{
			name:      "consecutive events outside window don't count",
			threshold: 3, window: 15 * time.Minute, cooldown: 10 * time.Minute,
			steps: []step{
				{action: "unhealthy"},
				{action: "unhealthy"},
				{action: "advance", advance: 16 * time.Minute}, // old events expire
				{action: "unhealthy"},                          // only 1 in window
				{action: "allow", wantAllow: true},
			},
		},
		{
			name:      "consecutive events within window accumulate",
			threshold: 3, window: 15 * time.Minute, cooldown: 10 * time.Minute,
			steps: []step{
				{action: "unhealthy"},
				{action: "advance", advance: 5 * time.Minute},
				{action: "unhealthy"},
				{action: "advance", advance: 5 * time.Minute},
				{action: "unhealthy"}, // 3rd within 15 min
				{action: "allow", wantAllow: false},
			},
		},
		{
			name:      "allow resets after cooldown and can re-trip",
			threshold: 2, window: 15 * time.Minute, cooldown: 10 * time.Minute,
			steps: []step{
				{action: "unhealthy"},
				{action: "unhealthy"},
				{action: "allow", wantAllow: false},
				{action: "advance", advance: 11 * time.Minute},
				{action: "allow", wantAllow: true}, // cooldown elapsed
				{action: "unhealthy"},
				{action: "unhealthy"},
				{action: "allow", wantAllow: false}, // tripped again
			},
		},
		{
			name:      "allow resets internal state after cooldown",
			threshold: 2, window: 15 * time.Minute, cooldown: 10 * time.Minute,
			steps: []step{
				{action: "unhealthy"},
				{action: "unhealthy"},
				{action: "allow", wantAllow: false},
				{action: "advance", advance: 11 * time.Minute},
				{action: "allow", wantAllow: true}, // resets state
				{action: "unhealthy"},              // only 1 after reset
				{action: "allow", wantAllow: true}, // not enough to trip
			},
		},
		{
			name:      "threshold one trips immediately",
			threshold: 1, window: 15 * time.Minute, cooldown: 10 * time.Minute,
			steps: []step{
				{action: "unhealthy"},
				{action: "allow", wantAllow: false},
			},
		},
		{
			name:      "healthy after open does not close prematurely",
			threshold: 2, window: 15 * time.Minute, cooldown: 10 * time.Minute,
			steps: []step{
				{action: "unhealthy"},
				{action: "unhealthy"},
				{action: "allow", wantAllow: false},
				{action: "healthy"},                 // resets counter, not circuit
				{action: "allow", wantAllow: false}, // still open
				{action: "allow", wantAllow: false}, // still open
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cb := NewNodeConditionCircuitBreaker(tc.threshold, tc.window, tc.cooldown)
			now := time.Now()
			cb.nowFunc = func() time.Time { return now }

			for i, s := range tc.steps {
				switch s.action {
				case "unhealthy":
					cb.RecordUnhealthyNode()
				case "healthy":
					cb.RecordHealthyNode()
				case "advance":
					now = now.Add(s.advance)
				case "allow":
					got := cb.Allow()
					if got != s.wantAllow {
						t.Errorf("step %d: Allow() = %v, want %v", i, got, s.wantAllow)
					}
				default:
					t.Fatalf("step %d: unknown action %q", i, s.action)
				}
			}
		})
	}
}
