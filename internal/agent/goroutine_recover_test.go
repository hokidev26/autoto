package agent

import (
	"sync"
	"testing"
)

// TestRecoverGoroutineStopsPanicPropagation exercises the shared backstop used
// by the fire-and-forget goroutines (title generation, background compaction,
// stall watchdog). A goroutine panic has no softer observable signal: if the
// helper fails to recover, the panic escapes and crashes the whole test binary
// instead of failing this test.
func TestRecoverGoroutineStopsPanicPropagation(t *testing.T) {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer recoverGoroutine("panicking test goroutine", "agentId", "agent-test")
		defer wg.Done()
		panic("boom")
	}()
	// The non-panic path must stay a no-op so the helper is safe to defer
	// unconditionally.
	go func() {
		defer recoverGoroutine("calm test goroutine")
		defer wg.Done()
	}()
	wg.Wait()
}
