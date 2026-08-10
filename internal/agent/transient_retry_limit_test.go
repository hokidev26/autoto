package agent

import (
	"testing"

	"autoto/internal/config"
)

// The retry ceiling is edited in the execution-budget card, which spells "no
// ceiling" as -1 like every budget beside it. runModelTurn used to clamp any
// negative value to zero, turning "keep trying" into "never retry" -- the exact
// opposite of what was asked for.
func TestNormalizeMaxTransientRetriesTreatsNegativeAsUnlimited(t *testing.T) {
	for _, value := range []int{-1, -5, -1000} {
		if got := normalizeMaxTransientRetries(value); got != unlimitedTransientRetries {
			t.Fatalf("normalizeMaxTransientRetries(%d) = %d, want %d", value, got, unlimitedTransientRetries)
		}
	}
	// Zero is a real setting and must not be swept into unlimited: it reports a
	// transient provider failure on the first attempt.
	if got := normalizeMaxTransientRetries(0); got != 0 {
		t.Fatalf("normalizeMaxTransientRetries(0) = %d, want 0", got)
	}
	if got := normalizeMaxTransientRetries(10); got != 10 {
		t.Fatalf("normalizeMaxTransientRetries(10) = %d, want 10", got)
	}
}

// Nothing has been set yet, so the ceiling comes from the config the runner was
// built with. This is the path every existing run takes before the setting is
// touched, so it must keep working unchanged.
func TestMaxTransientRetriesFallsBackToConfig(t *testing.T) {
	runner := &Runner{cfg: config.AgentConfig{MaxTransientRetries: 7}}
	if got := runner.MaxTransientRetries(); got != 7 {
		t.Fatalf("MaxTransientRetries() = %d, want the configured 7", got)
	}

	unlimited := &Runner{cfg: config.AgentConfig{MaxTransientRetries: -1}}
	if got := unlimited.MaxTransientRetries(); got != unlimitedTransientRetries {
		t.Fatalf("a configured -1 must read back as unlimited, got %d", got)
	}
}

// Saving the setting has to affect the next turn, not the next restart.
func TestSetMaxTransientRetriesOverridesConfig(t *testing.T) {
	runner := &Runner{cfg: config.AgentConfig{MaxTransientRetries: 10}}

	if applied := runner.SetMaxTransientRetries(3); applied != 3 {
		t.Fatalf("SetMaxTransientRetries(3) reported %d", applied)
	}
	if got := runner.MaxTransientRetries(); got != 3 {
		t.Fatalf("live ceiling = %d, want 3", got)
	}

	// Zero must survive as zero. It is also the zero value of the field, so a
	// setter that could not tell "set to 0" from "never set" would silently fall
	// back to the config's 10 here.
	if applied := runner.SetMaxTransientRetries(0); applied != 0 {
		t.Fatalf("SetMaxTransientRetries(0) reported %d", applied)
	}
	if got := runner.MaxTransientRetries(); got != 0 {
		t.Fatalf("live ceiling after setting 0 = %d, want 0", got)
	}

	if applied := runner.SetMaxTransientRetries(-1); applied != unlimitedTransientRetries {
		t.Fatalf("SetMaxTransientRetries(-1) reported %d", applied)
	}
	if got := runner.MaxTransientRetries(); got != unlimitedTransientRetries {
		t.Fatalf("live ceiling after setting -1 = %d, want %d", got, unlimitedTransientRetries)
	}
}

func TestMaxTransientRetriesOnNilRunner(t *testing.T) {
	var runner *Runner
	if got := runner.MaxTransientRetries(); got != 0 {
		t.Fatalf("a nil runner must report 0, got %d", got)
	}
	if applied := runner.SetMaxTransientRetries(-1); applied != unlimitedTransientRetries {
		t.Fatalf("a nil runner still normalizes its argument, got %d", applied)
	}
}
