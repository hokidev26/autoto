package app

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"autoto/internal/agent"
	"autoto/internal/background"
)

// stuckWorkReaperInterval is how often the sweep runs. Both reapers are cheap --
// one indexed query each, and nothing to do in the common case -- so the interval
// is set by how long an operator should have to look at an agent that is wedged,
// not by cost.
const stuckWorkReaperInterval = 2 * time.Minute

// stuckWorkReaperService periodically ends runs and background tasks that no
// longer have an executor. Recovery used to happen only at startup, so the only
// way out of a wedged agent inside a long-lived session was to restart Autoto.
type stuckWorkReaperService struct {
	logger            *slog.Logger
	runner            *agent.Runner
	backgroundManager *background.Manager
	interval          time.Duration

	mu      sync.Mutex
	started bool
	cancel  context.CancelFunc
	done    chan struct{}
}

func newStuckWorkReaperService(logger *slog.Logger, runner *agent.Runner, backgroundManager *background.Manager) *stuckWorkReaperService {
	if logger == nil {
		logger = slog.Default()
	}
	return &stuckWorkReaperService{logger: logger, runner: runner, backgroundManager: backgroundManager, interval: stuckWorkReaperInterval}
}

// Start launches the sweep loop and returns immediately. The start context is
// detached for the same reason as the image cleanup service: Runtime's start
// context may be a short readiness timeout, and Close owns the worker instead.
func (s *stuckWorkReaperService) Start(ctx context.Context) error {
	if s == nil {
		return errors.New("app: nil stuck work reaper service")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return errors.New("app: stuck work reaper service already started")
	}
	loopCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	done := make(chan struct{})
	s.started = true
	s.cancel = cancel
	s.done = done
	s.mu.Unlock()

	go func() {
		defer close(done)
		s.loop(loopCtx)
	}()
	return nil
}

func (s *stuckWorkReaperService) loop(ctx context.Context) {
	interval := s.interval
	if interval <= 0 {
		interval = stuckWorkReaperInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.sweep(ctx)
		}
	}
}

func (s *stuckWorkReaperService) sweep(ctx context.Context) {
	if s.backgroundManager != nil {
		if reaped, err := s.backgroundManager.ReapAbandonedTasks(ctx, background.DefaultAbandonedTaskIdleTimeout); err != nil {
			if ctx.Err() == nil {
				s.logger.Warn("background task reaper sweep failed", "error", err)
			}
		} else if reaped > 0 {
			s.logger.Warn("ended abandoned background tasks", "count", reaped)
		}
	}
	if s.runner == nil {
		return
	}
	// Runs are swept after tasks so a task reaped in this pass has already woken
	// whatever was waiting on it before the run behind it is considered.
	if reaped, err := s.runner.ReapAbandonedRuns(ctx, agent.DefaultAbandonedRunIdleTimeout); err != nil {
		if ctx.Err() == nil {
			s.logger.Warn("abandoned run reaper sweep failed", "error", err)
		}
	} else if reaped > 0 {
		s.logger.Warn("ended abandoned runs", "count", reaped)
	}
}

// Close stops the sweep loop and waits for it, unless the caller's shutdown
// context expires first.
func (s *stuckWorkReaperService) Close(ctx context.Context) error {
	if s == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	s.mu.Lock()
	if !s.started {
		s.mu.Unlock()
		return nil
	}
	cancel := s.cancel
	done := s.done
	s.mu.Unlock()

	cancel()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
