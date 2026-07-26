package agent

import (
	"testing"
	"time"
)

// TestJitteredBackoffGrowsAndStaysBounded pins the retry schedule. The previous
// 2s ceiling was far below what provider rate limits need, so retries were
// exhausted before the limit could clear.
func TestJitteredBackoffGrowsAndStaysBounded(t *testing.T) {
	// A deterministic "random" that always picks the top of the window lets the
	// growth curve be asserted exactly.
	maxPick := func(n int64) int64 { return n - 1 }
	previous := time.Duration(0)
	for attempt := 0; attempt < 12; attempt++ {
		delay := jitteredBackoff(attempt, maxPick)
		if delay < modelRetryBaseDelay {
			t.Fatalf("attempt %d: delay %s is below the base delay", attempt, delay)
		}
		if delay > modelRetryMaxDelay {
			t.Fatalf("attempt %d: delay %s exceeds the cap %s", attempt, delay, modelRetryMaxDelay)
		}
		if delay < previous {
			t.Fatalf("attempt %d: delay %s went backwards from %s", attempt, delay, previous)
		}
		previous = delay
	}
	if previous != modelRetryMaxDelay {
		t.Fatalf("expected the schedule to reach the %s cap, got %s", modelRetryMaxDelay, previous)
	}
}

// TestJitteredBackoffSpreadsRetries is the reason jitter exists: concurrent
// runners that hit one rate limit must not retry in lockstep.
func TestJitteredBackoffSpreadsRetries(t *testing.T) {
	minPick := func(int64) int64 { return 0 }
	maxPick := func(n int64) int64 { return n - 1 }

	// Attempt 0 has no window yet, so both ends agree.
	if got := jitteredBackoff(0, minPick); got != modelRetryBaseDelay {
		t.Fatalf("attempt 0 should be the base delay, got %s", got)
	}
	// Later attempts must span a real range rather than a single fixed value.
	low := jitteredBackoff(5, minPick)
	high := jitteredBackoff(5, maxPick)
	if low != modelRetryBaseDelay {
		t.Fatalf("jitter floor should be the base delay, got %s", low)
	}
	if high <= low {
		t.Fatalf("expected a jitter window, got low=%s high=%s", low, high)
	}
}

// TestModelRetryBackoffUsesRealRandomness guards the wiring: the exported path
// must stay inside the documented bounds.
func TestModelRetryBackoffUsesRealRandomness(t *testing.T) {
	for attempt := 0; attempt < 10; attempt++ {
		delay := modelRetryBackoff(attempt)
		if delay < modelRetryBaseDelay || delay > modelRetryMaxDelay {
			t.Fatalf("attempt %d produced out-of-range delay %s", attempt, delay)
		}
	}
	if got := modelRetryBackoff(-1); got != modelRetryBaseDelay {
		t.Fatalf("negative attempt must clamp to the base delay, got %s", got)
	}
}
