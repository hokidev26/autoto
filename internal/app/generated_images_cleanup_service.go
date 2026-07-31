package app

import (
	"context"
	"errors"
	"log/slog"
	"sync"

	"autoto/internal/db"
	"autoto/internal/imageassets"
)

// generatedImagesCleanupService runs the startup image sweep once under the
// runtime supervisor. Its worker is tracked so Close can cancel and wait for it
// before Runtime releases the store and asset directories when the shutdown
// context permits.
type generatedImagesCleanupService struct {
	cleanup func(context.Context)

	mu      sync.Mutex
	started bool
	cancel  context.CancelFunc
	done    chan struct{}
}

func newGeneratedImagesCleanupService(logger *slog.Logger, store *db.Store, assets *imageassets.Store) *generatedImagesCleanupService {
	return &generatedImagesCleanupService{
		cleanup: func(ctx context.Context) {
			cleanupGeneratedImages(ctx, logger, store, assets)
		},
	}
}

// Start schedules cleanup and returns immediately. The start context is
// detached because Runtime's start context may be a short-lived readiness
// timeout; Close owns the worker's cancellation instead.
func (s *generatedImagesCleanupService) Start(ctx context.Context) error {
	if s == nil || s.cleanup == nil {
		return errors.New("app: nil generated image cleanup service")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return errors.New("app: generated image cleanup service already started")
	}
	cleanupCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	done := make(chan struct{})
	s.started = true
	s.cancel = cancel
	s.done = done
	cleanup := s.cleanup
	s.mu.Unlock()

	go func() {
		defer close(done)
		cleanup(cleanupCtx)
	}()
	return nil
}

// Close cancels the cleanup worker and waits for it, unless the caller's
// shutdown context expires first.
func (s *generatedImagesCleanupService) Close(ctx context.Context) error {
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
	default:
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
