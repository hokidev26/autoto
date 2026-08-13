package db

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// The read pool is query_only: a write routed through it must fail loudly with
// SQLITE_READONLY rather than silently succeed. This is the safety net that
// makes the read/write split survivable — a future mis-routed write becomes an
// error, not corruption.
func TestReadPoolRejectsWrites(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, _, agent, err := store.CreateProject(ctx, "Demo", "", t.TempDir(), "openai:test", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := store.ReadDB().ExecContext(ctx, `UPDATE agents SET title = 'hijacked' WHERE id = ?`, agent.ID); err == nil {
		t.Fatal("expected UPDATE through the read pool to fail with SQLITE_READONLY")
	} else if !strings.Contains(strings.ToLower(err.Error()), "readonly") {
		t.Fatalf("expected a readonly error from the query_only pool, got %v", err)
	}
	if _, err := store.ReadDB().ExecContext(ctx, `CREATE TABLE read_pool_probe (id INTEGER)`); err == nil {
		t.Fatal("expected DDL through the read pool to fail with SQLITE_READONLY")
	}

	// The pragma must hold on every pooled connection, not just the first one.
	// Forcing idle connections out makes the next query open a fresh one.
	store.ReadDB().SetMaxIdleConns(0)
	var queryOnly int
	if err := store.ReadDB().QueryRowContext(ctx, `PRAGMA query_only`).Scan(&queryOnly); err != nil {
		t.Fatal(err)
	}
	if queryOnly != 1 {
		t.Fatalf("expected query_only=1 on a fresh read pool connection, got %d", queryOnly)
	}

	// The writer pool must be untouched by the split.
	if _, err := store.DB().ExecContext(ctx, `UPDATE agents SET title = 'still writable' WHERE id = ?`, agent.ID); err != nil {
		t.Fatalf("writer pool must keep accepting writes: %v", err)
	}
}

// A continuously committing writer must not starve read-pool queries, and the
// readers must never observe errors. This checks the functional half of the
// split (WAL readers run alongside the writer); it deliberately avoids strict
// timing assertions.
func TestReadPoolServesReadsAlongsideWrites(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, _, agent, err := store.CreateProject(ctx, "Demo", "", t.TempDir(), "openai:test", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		if _, err := store.AddMessage(ctx, Message{AgentID: agent.ID, Role: "assistant", ContentText: fmt.Sprintf("seed-%d", i)}); err != nil {
			t.Fatal(err)
		}
	}

	const writerIterations = 60
	writerDone := make(chan struct{})
	var writerErr error
	go func() {
		defer close(writerDone)
		for i := 0; i < writerIterations; i++ {
			if _, err := store.AddMessage(ctx, Message{AgentID: agent.ID, Role: "assistant", ContentText: fmt.Sprintf("stream-%d", i)}); err != nil {
				writerErr = err
				return
			}
		}
	}()

	const readers = 4
	readerErrs := make(chan error, readers)
	var wg sync.WaitGroup
	for r := 0; r < readers; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				if _, err := store.ReadAgentLiveSnapshot(ctx, agent.ID); err != nil {
					readerErrs <- fmt.Errorf("live snapshot: %w", err)
					return
				}
				if _, err := store.ListMessagesPage(ctx, agent.ID, "", 25); err != nil {
					readerErrs <- fmt.Errorf("messages page: %w", err)
					return
				}
				if _, err := store.GetAgent(ctx, agent.ID); err != nil {
					readerErrs <- fmt.Errorf("get agent: %w", err)
					return
				}
				select {
				case <-writerDone:
					return
				default:
				}
			}
		}()
	}

	<-writerDone
	wg.Wait()
	close(readerErrs)
	if writerErr != nil {
		t.Fatalf("writer failed while readers were active: %v", writerErr)
	}
	for err := range readerErrs {
		t.Errorf("reader failed while writer was streaming: %v", err)
	}

	messages, err := store.ListMessages(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 10+writerIterations {
		t.Fatalf("expected %d messages after concurrent run, got %d", 10+writerIterations, len(messages))
	}
}
