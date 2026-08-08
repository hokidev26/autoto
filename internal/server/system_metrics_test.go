package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"autoto/internal/config"
	"autoto/internal/db"
)

func newSystemMetricsTestServer(t *testing.T) *Server {
	t.Helper()
	store, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	cfg := config.Config{
		Server: config.ServerConfig{Host: "127.0.0.1", Port: 9090},
		Paths: config.PathsConfig{
			HomeDir:           filepath.Join(t.TempDir(), "home"),
			DatabasePath:      filepath.Join(t.TempDir(), "autoto.db"),
			DefaultProjectDir: filepath.Join(t.TempDir(), "projects"),
		},
	}
	return New(cfg, store, nil, nil)
}

func getSystemMetrics(t *testing.T, app *Server) systemMetricsResponse {
	t.Helper()
	recorder := httptest.NewRecorder()
	app.Routes().ServeHTTP(recorder, newTestRequest(http.MethodGet, "/api/system/metrics", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body.String())
	}
	var body systemMetricsResponse
	if err := json.NewDecoder(recorder.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	return body
}

func TestSystemMetricsRouteReportsWellFormedUtilisation(t *testing.T) {
	app := newSystemMetricsTestServer(t)

	body := getSystemMetrics(t, app)

	if body.CapturedAt == "" {
		t.Fatalf("CapturedAt empty: %+v", body)
	}
	if _, err := time.Parse(time.RFC3339Nano, body.CapturedAt); err != nil {
		t.Fatalf("CapturedAt %q is not RFC3339: %v", body.CapturedAt, err)
	}
	// Availability is platform-dependent, so the assertion is on the invariant
	// that holds everywhere: a metric reported as available carries an in-range
	// value, and one reported as unavailable is not silently drawn as zero load.
	if body.CPU.Available && (body.CPU.Percent < 0 || body.CPU.Percent > 100) {
		t.Errorf("CPU.Percent = %v, out of range", body.CPU.Percent)
	}
	if body.Memory.Available {
		if body.Memory.TotalBytes == 0 || body.Memory.UsedBytes > body.Memory.TotalBytes {
			t.Errorf("implausible memory reading: %+v", body.Memory)
		}
		if body.Memory.Percent < 0 || body.Memory.Percent > 100 {
			t.Errorf("Memory.Percent = %v, out of range", body.Memory.Percent)
		}
	}
	if body.Network.Available && (body.Network.RxBytesPerSec < 0 || body.Network.TxBytesPerSec < 0) {
		t.Errorf("negative network rate: %+v", body.Network)
	}
}

// The availability flags must survive JSON encoding as explicit false rather
// than being dropped, because the client distinguishes "not measured" from
// "measured as zero" and hides the card in the first case.
func TestSystemMetricsSerialisesUnavailableMetricsExplicitly(t *testing.T) {
	app := newSystemMetricsTestServer(t)
	app.sysMetrics = nil // Forces the all-unavailable path.

	recorder := httptest.NewRecorder()
	app.Routes().ServeHTTP(recorder, newTestRequest(http.MethodGet, "/api/system/metrics", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body.String())
	}

	raw := recorder.Body.String()
	for _, want := range []string{`"cpu"`, `"memory"`, `"network"`, `"available":false`, `"percent":0`, `"rxBytesPerSec":0`} {
		if !strings.Contains(raw, want) {
			t.Errorf("response missing %s: %s", want, raw)
		}
	}

	var body systemMetricsResponse
	if err := json.NewDecoder(strings.NewReader(raw)).Decode(&body); err != nil {
		t.Fatal(err)
	}
	// A fresh sampler has no previous reading, so CPU and network cannot be
	// rates yet. Memory is instantaneous and may legitimately be available.
	if body.CPU.Available || body.Network.Available {
		t.Errorf("first sample reported a rate: %+v", body)
	}
}

// A second request must reuse the process-lifetime sampler so the delta between
// consecutive readings is available; a per-request sampler would report CPU and
// network as permanently unavailable.
func TestSystemMetricsSamplerPersistsAcrossRequests(t *testing.T) {
	app := newSystemMetricsTestServer(t)
	if app.sysMetrics == nil {
		t.Fatal("New did not install a sampler")
	}

	first := getSystemMetrics(t, app)
	if first.CPU.Available {
		t.Errorf("first request reported a CPU rate with no prior reading: %+v", first.CPU)
	}
	// Past the sampler's minimum interval so the second reading is a real one
	// rather than a repeat of the first.
	time.Sleep(300 * time.Millisecond)
	second := getSystemMetrics(t, app)

	firstAt, err := time.Parse(time.RFC3339Nano, first.CapturedAt)
	if err != nil {
		t.Fatalf("first CapturedAt %q unparsable: %v", first.CapturedAt, err)
	}
	secondAt, err := time.Parse(time.RFC3339Nano, second.CapturedAt)
	if err != nil {
		t.Fatalf("second CapturedAt %q unparsable: %v", second.CapturedAt, err)
	}
	if !secondAt.After(firstAt) {
		t.Errorf("CapturedAt did not advance: %s then %s", first.CapturedAt, second.CapturedAt)
	}
	// Only assert the rate appeared on platforms that implement collection; the
	// unimplemented branch legitimately stays unavailable forever. Memory being
	// available is the signal that this platform has a working collector.
	if second.Memory.Available && !second.CPU.Available {
		t.Errorf("CPU still unavailable on a platform with working collection: %+v", second)
	}
}

func TestSystemMetricsRejectsNonGET(t *testing.T) {
	app := newSystemMetricsTestServer(t)

	for _, method := range []string{http.MethodPost, http.MethodDelete, http.MethodPut} {
		recorder := httptest.NewRecorder()
		app.Routes().ServeHTTP(recorder, newTestRequest(method, "/api/system/metrics", nil))
		if recorder.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s /api/system/metrics = %d, want 405", method, recorder.Code)
		}
	}
}
