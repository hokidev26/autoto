package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"autoto/internal/db"
	"autoto/internal/mcp"
	"autoto/internal/secrets"
	"autoto/internal/tools"
)

const poolTestResult = `[{"type":"text","text":"ok"}]`

// newPoolFixture installs and enables a plugin whose discovery consumes the
// starter's first client, then returns the executable tool adapter.
func newPoolFixture(t *testing.T, slug string, starter *recordingStarter, resolver secrets.Resolver, refs map[string]string, options ...Option) (*Service, *db.Store, tools.Tool, db.Plugin) {
	t.Helper()
	ctx := context.Background()
	store, err := db.Open(ctx, filepath.Join(t.TempDir(), "plugins.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	root := writePluginFixture(t, slug, nil, refs)
	options = append([]Option{WithMCPStarter(starter.start)}, options...)
	service := NewService(store, resolver, options...)
	t.Cleanup(service.Close)
	installed, err := service.Install(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	enabled, err := service.Enable(ctx, installed.ID)
	if err != nil {
		t.Fatal(err)
	}
	listed, err := service.ListTools(ctx, tools.ResolutionContext{})
	if err != nil || len(listed) != 1 {
		t.Fatalf("list adapters: %+v %v", listed, err)
	}
	return service, store, listed[0], enabled
}

func discoveryClient() *fakeMCPClient {
	return &fakeMCPClient{tools: []mcp.Tool{{Name: "ping", InputSchema: json.RawMessage(`{"type":"object"}`)}}}
}

func executeClient() *fakeMCPClient {
	return &fakeMCPClient{callResult: mcp.ToolCallResult{Content: json.RawMessage(poolTestResult)}}
}

func mustExecute(t *testing.T, adapter tools.Tool) tools.Result {
	t.Helper()
	result, err := adapter.Execute(context.Background(), tools.Call{Name: adapter.Name(), Input: json.RawMessage(`{}`)}, tools.Env{})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestPluginToolReusesPooledProcess(t *testing.T) {
	exec := executeClient()
	starter := &recordingStarter{clients: []*fakeMCPClient{discoveryClient(), exec}}
	_, _, adapter, _ := newPoolFixture(t, "reuse", starter, nil, nil)

	mustExecute(t, adapter)
	mustExecute(t, adapter)
	mustExecute(t, adapter)

	if got := starter.launchCount(); got != 2 {
		t.Fatalf("expected 2 launches (enable + one pooled execute process), got %d", got)
	}
	exec.mu.Lock()
	inits, calls, closes := exec.inits, exec.calls, exec.closes
	exec.mu.Unlock()
	if inits != 1 || calls != 3 || closes != 0 {
		t.Fatalf("pooled client lifecycle inits=%d calls=%d closes=%d", inits, calls, closes)
	}
}

func TestPluginToolPoolDropsClientAfterCallError(t *testing.T) {
	exec1, exec2 := executeClient(), executeClient()
	starter := &recordingStarter{clients: []*fakeMCPClient{discoveryClient(), exec1, exec2}}
	_, _, adapter, _ := newPoolFixture(t, "drop-on-error", starter, nil, nil)

	mustExecute(t, adapter)
	exec1.callErr = errors.New("boom")
	if _, err := adapter.Execute(context.Background(), tools.Call{Name: adapter.Name(), Input: json.RawMessage(`{}`)}, tools.Env{}); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("expected propagated call error, got %v", err)
	}
	if exec1.closeCount() != 1 {
		t.Fatalf("errored pooled client was not closed: %d", exec1.closeCount())
	}
	mustExecute(t, adapter)
	if got := starter.launchCount(); got != 3 {
		t.Fatalf("expected respawn after error (3 launches), got %d", got)
	}
	if exec2.closeCount() != 0 {
		t.Fatalf("replacement client should stay pooled, closes=%d", exec2.closeCount())
	}
}

func TestPluginPoolInvalidatedOnDisable(t *testing.T) {
	exec := executeClient()
	starter := &recordingStarter{clients: []*fakeMCPClient{discoveryClient(), exec}}
	service, _, adapter, plugin := newPoolFixture(t, "disable-invalidate", starter, nil, nil)

	mustExecute(t, adapter)
	if _, err := service.Disable(context.Background(), plugin.ID); err != nil {
		t.Fatal(err)
	}
	if exec.closeCount() != 1 {
		t.Fatalf("disable did not close the warm plugin process: closes=%d", exec.closeCount())
	}
}

func TestPluginPoolRecyclesOnRevisionChange(t *testing.T) {
	ctx := context.Background()
	exec1, exec2 := executeClient(), executeClient()
	starter := &recordingStarter{clients: []*fakeMCPClient{discoveryClient(), exec1, exec2}}
	service, store, adapter, plugin := newPoolFixture(t, "revision-recycle", starter, nil, nil)

	mustExecute(t, adapter)
	current, err := store.GetPlugin(ctx, plugin.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdatePluginStatus(ctx, current.ID, current.Status, true, db.Now(), ""); err != nil {
		t.Fatal(err)
	}
	// The retained adapter fails closed on the revision bump; a re-resolved
	// adapter must recycle the stale pooled process instead of reusing it.
	listed, err := service.ListTools(ctx, tools.ResolutionContext{})
	if err != nil || len(listed) != 1 {
		t.Fatalf("relist adapters: %+v %v", listed, err)
	}
	mustExecute(t, listed[0])
	if exec1.closeCount() != 1 {
		t.Fatalf("stale-revision pooled client was not closed: %d", exec1.closeCount())
	}
	if got := starter.launchCount(); got != 3 {
		t.Fatalf("expected respawn after revision change (3 launches), got %d", got)
	}
	if exec2.closeCount() != 0 {
		t.Fatalf("fresh client should stay pooled, closes=%d", exec2.closeCount())
	}
}

func TestPluginPoolRecyclesOnEnvironmentChange(t *testing.T) {
	secret := "first-secret"
	resolver := resolverFunc(func(_ context.Context, ref secrets.Ref) (string, error) {
		if ref.Name != "PLUGIN_TOKEN" {
			return "", errors.New("unexpected ref")
		}
		return secret, nil
	})
	exec1, exec2 := executeClient(), executeClient()
	starter := &recordingStarter{clients: []*fakeMCPClient{discoveryClient(), exec1, exec2}}
	_, _, adapter, _ := newPoolFixture(t, "env-recycle", starter, resolver, map[string]string{"TOKEN": "env:PLUGIN_TOKEN"})

	mustExecute(t, adapter)
	secret = "second-secret"
	mustExecute(t, adapter)

	if exec1.closeCount() != 1 {
		t.Fatalf("pooled client with stale secrets was not closed: %d", exec1.closeCount())
	}
	if got := starter.launchCount(); got != 3 {
		t.Fatalf("expected respawn after environment change (3 launches), got %d", got)
	}
	if starter.configs[2].Env["TOKEN"] != "second-secret" {
		t.Fatalf("respawned process did not receive the new secret: %+v", starter.configs[2].Env)
	}
}

func TestPluginPoolOverflowSpawnsEphemeralProcess(t *testing.T) {
	entered := make(chan struct{})
	unblock := make(chan struct{})
	exec1 := executeClient()
	exec2 := executeClient()
	starter := &recordingStarter{clients: []*fakeMCPClient{discoveryClient(), exec1, exec2}}
	_, _, adapter, _ := newPoolFixture(t, "overflow", starter, nil, nil)

	// Warm the pool first so the blocked call below runs on the pooled client.
	mustExecute(t, adapter)
	exec1.onCall = func() {
		close(entered)
		<-unblock
	}
	done := make(chan error, 1)
	go func() {
		_, err := adapter.Execute(context.Background(), tools.Call{Name: adapter.Name(), Input: json.RawMessage(`{}`)}, tools.Env{})
		done <- err
	}()
	<-entered
	exec1.onCall = nil
	// While the pooled process is busy, a concurrent call must not queue
	// behind it: it runs on its own process, which is closed afterwards
	// because the pool keeps at most one process per plugin.
	mustExecute(t, adapter)
	close(unblock)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if got := starter.launchCount(); got != 3 {
		t.Fatalf("expected overflow spawn (3 launches), got %d", got)
	}
	if exec2.closeCount() != 1 {
		t.Fatalf("overflow client was not closed after its call: %d", exec2.closeCount())
	}
	mustExecute(t, adapter)
	if got := starter.launchCount(); got != 3 {
		t.Fatalf("pooled client was not reused after overflow, launches=%d", got)
	}
	if exec1.closeCount() != 0 {
		t.Fatalf("pooled client should stay warm, closes=%d", exec1.closeCount())
	}
}

func TestPluginPoolIdleTTLReapsProcess(t *testing.T) {
	exec := executeClient()
	starter := &recordingStarter{clients: []*fakeMCPClient{discoveryClient(), exec}}
	_, _, adapter, _ := newPoolFixture(t, "idle-reap", starter, nil, nil, WithPoolIdleTTL(25*time.Millisecond))

	mustExecute(t, adapter)
	deadline := time.Now().Add(2 * time.Second)
	for exec.closeCount() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("idle pooled process was not reaped")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestPluginPoolCallCapRecyclesProcess(t *testing.T) {
	exec1, exec2 := executeClient(), executeClient()
	starter := &recordingStarter{clients: []*fakeMCPClient{discoveryClient(), exec1, exec2}}
	_, _, adapter, _ := newPoolFixture(t, "call-cap", starter, nil, nil)

	for index := 0; index < maxPooledCalls; index++ {
		mustExecute(t, adapter)
	}
	if exec1.closeCount() != 1 {
		t.Fatalf("pooled client was not recycled at the call cap: closes=%d", exec1.closeCount())
	}
	exec1.mu.Lock()
	calls := exec1.calls
	exec1.mu.Unlock()
	if calls != maxPooledCalls {
		t.Fatalf("expected exactly %d calls on the capped client, got %d", maxPooledCalls, calls)
	}
	mustExecute(t, adapter)
	if got := starter.launchCount(); got != 3 {
		t.Fatalf("expected respawn after call cap (3 launches), got %d", got)
	}
}

func TestServiceCloseShutsDownPooledProcesses(t *testing.T) {
	exec1, exec2 := executeClient(), executeClient()
	starter := &recordingStarter{clients: []*fakeMCPClient{discoveryClient(), exec1, exec2}}
	service, _, adapter, _ := newPoolFixture(t, "close-pool", starter, nil, nil)

	mustExecute(t, adapter)
	service.Close()
	if exec1.closeCount() != 1 {
		t.Fatalf("service close did not terminate the warm process: %d", exec1.closeCount())
	}
	// A closed pool degrades to per-call processes instead of failing calls.
	result := mustExecute(t, adapter)
	if !strings.Contains(result.Output, "ok") {
		t.Fatalf("execute after pool close returned %q", result.Output)
	}
	if exec2.closeCount() != 1 {
		t.Fatalf("per-call client after close must be closed immediately: %d", exec2.closeCount())
	}
}
