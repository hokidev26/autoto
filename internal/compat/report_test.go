package compat

import (
	"sync"
	"sync/atomic"
	"testing"
)

func TestReportAddDeduplicatesAndPreservesOrder(t *testing.T) {
	var report Report
	if !report.Empty() {
		t.Fatal("zero-value report should be empty")
	}

	first := Usage{Key: "legacy-token", Legacy: "WebSocket ?token= query parameter", Replacement: "X-Autoto-Token header", Kind: "query-parameter"}
	second := Usage{Key: "legacy-path", Legacy: "old-path", Replacement: "new-path", Kind: "path"}
	report.Add(first)
	report.Add(Usage{Key: first.Key, Legacy: "duplicate", Replacement: "ignored"})
	report.Add(Usage{})
	report.Add(second)

	if report.Empty() {
		t.Fatal("report with usages should not be empty")
	}
	if len(report.Usages) != 2 {
		t.Fatalf("expected two unique usages, got %d", len(report.Usages))
	}
	if report.Usages[0] != first || report.Usages[1] != second {
		t.Fatalf("unexpected usage order: %#v", report.Usages)
	}
	if got := report.LegacyNames(); len(got) != 2 || got[0] != first.Legacy || got[1] != second.Legacy {
		t.Fatalf("unexpected legacy names: %#v", got)
	}
	if got := report.Replacements(); len(got) != 2 || got[0] != first.Replacement || got[1] != second.Replacement {
		t.Fatalf("unexpected replacements: %#v", got)
	}
}

func TestRegistryWarnDeduplicatesConcurrentCalls(t *testing.T) {
	const callers = 64
	usage := Usage{Key: "legacy-token", Legacy: "WebSocket ?token= query parameter", Replacement: "X-Autoto-Token header", Kind: "query-parameter"}
	warnings := make(chan Usage, callers)
	registry := NewRegistry(func(got Usage) {
		warnings <- got
	})

	start := make(chan struct{})
	var accepted atomic.Int64
	var wg sync.WaitGroup
	wg.Add(callers)
	for range callers {
		go func() {
			defer wg.Done()
			<-start
			if registry.Warn(usage) {
				accepted.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()
	close(warnings)

	if got := accepted.Load(); got != 1 {
		t.Fatalf("expected one accepted warning, got %d", got)
	}
	var emitted []Usage
	for got := range warnings {
		emitted = append(emitted, got)
	}
	if len(emitted) != 1 || emitted[0] != usage {
		t.Fatalf("expected one emitted warning, got %#v", emitted)
	}
}

func TestRegistryWarnHandlesEmptyAndNilReceivers(t *testing.T) {
	var nilRegistry *Registry
	if nilRegistry.Warn(Usage{Key: "legacy"}) {
		t.Fatal("nil registry should reject warnings")
	}

	registry := NewRegistry(nil)
	if registry.Warn(Usage{}) {
		t.Fatal("empty usage key should be rejected")
	}
	if !registry.Warn(Usage{Key: "legacy"}) {
		t.Fatal("first non-empty usage should be accepted")
	}
	if registry.Warn(Usage{Key: "legacy"}) {
		t.Fatal("duplicate usage key should be rejected")
	}
}
