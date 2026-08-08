package server

import (
	"net/http"
	"time"

	"autoto/internal/sysmetrics"
)

// systemMetricsResponse reports host utilisation for the home dashboard's
// resource cards.
//
// Each metric carries its own availability flag rather than being omitted:
// "not measured on this platform" and "measured as zero" are different facts,
// and the dashboard hides a card in the first case instead of drawing an idle
// bar. CPU and network are unavailable on the first request of a process
// because a rate needs two readings.
type systemMetricsResponse struct {
	CapturedAt string             `json:"capturedAt"`
	CPU        sysmetrics.CPU     `json:"cpu"`
	Memory     sysmetrics.Memory  `json:"memory"`
	Network    sysmetrics.Network `json:"network"`
}

// systemMetrics serves GET /api/system/metrics.
//
// Registered in the same authenticated route group as /api/overview and
// /api/runtime/summary. It exposes no more about the host than
// /api/runtime/summary already does (which reports the PID, executable path, and
// CPU count), and adds no unauthenticated surface.
func (s *Server) systemMetrics(w http.ResponseWriter, r *http.Request) {
	sampler := s.sysMetrics
	if sampler == nil {
		// A Server built by a test helper that bypassed New still answers with a
		// well-formed, all-unavailable payload rather than panicking.
		sampler = sysmetrics.NewSampler()
	}
	sample := sampler.Sample()
	capturedAt := sample.CapturedAt
	if capturedAt.IsZero() {
		capturedAt = time.Now()
	}
	writeJSON(w, http.StatusOK, systemMetricsResponse{
		// Nanosecond precision to match /api/overview, and because a client
		// polling every few seconds can otherwise see two readings share a
		// second-precision stamp.
		CapturedAt: capturedAt.UTC().Format(time.RFC3339Nano),
		CPU:        sample.CPU,
		Memory:     sample.Memory,
		Network:    sample.Network,
	})
}
