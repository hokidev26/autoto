package server

import (
	"context"
	"errors"
	"net/http"
)

type temporaryTunnelService struct {
	manager *TemporaryTunnelManager
}

func (s *Server) temporaryTunnels() temporaryTunnelService {
	var manager *TemporaryTunnelManager
	if s != nil {
		manager = s.temporaryTunnel
	}
	return temporaryTunnelService{manager: manager}
}

func (t temporaryTunnelService) snapshot() TemporaryTunnelSnapshot {
	if t.manager == nil {
		return TemporaryTunnelSnapshot{Available: false, Status: temporaryTunnelUnavailable, Error: "temporary tunnel manager is unavailable"}
	}
	return t.manager.Snapshot()
}

func (t temporaryTunnelService) install(ctx context.Context) (TemporaryTunnelSnapshot, error) {
	if t.manager == nil {
		return TemporaryTunnelSnapshot{}, apiErr(http.StatusServiceUnavailable, "temporary tunnel manager is unavailable")
	}
	snapshot, err := t.manager.InstallCloudflared(ctx)
	if err != nil {
		status := http.StatusBadGateway
		switch {
		case errors.Is(err, errCloudflaredInstallUnsupported), errors.Is(err, errCloudflaredInstallInProgress), snapshot.Status == temporaryTunnelRunning, snapshot.Status == temporaryTunnelStarting, snapshot.Status == temporaryTunnelStopping:
			status = http.StatusConflict
		}
		return TemporaryTunnelSnapshot{}, apiErr(status, err.Error())
	}
	return snapshot, nil
}

func (t temporaryTunnelService) start(ctx context.Context) (TemporaryTunnelSnapshot, error) {
	if t.manager == nil {
		return TemporaryTunnelSnapshot{}, apiErr(http.StatusServiceUnavailable, "temporary tunnel manager is unavailable")
	}
	snapshot, err := t.manager.StartTunnel(ctx)
	if err != nil {
		return TemporaryTunnelSnapshot{}, apiErr(http.StatusServiceUnavailable, err.Error())
	}
	return snapshot, nil
}

func (t temporaryTunnelService) stop(ctx context.Context) (TemporaryTunnelSnapshot, error) {
	if t.manager == nil {
		return t.snapshot(), nil
	}
	snapshot, err := t.manager.StopTunnel(ctx)
	if err != nil {
		return TemporaryTunnelSnapshot{}, apiErr(http.StatusServiceUnavailable, err.Error())
	}
	return snapshot, nil
}
