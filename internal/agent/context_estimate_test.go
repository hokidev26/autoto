package agent

import (
	"context"
	"strings"
	"testing"

	"autoto/internal/config"
	"autoto/internal/db"
	"autoto/internal/media"
	"autoto/internal/providers"
)

func TestRecordContextCalibrationAndRatio(t *testing.T) {
	r := &Runner{}

	r.recordContextCalibration("agent-1", "prov:model", contextCalibrationMinSampleTokens-1, providers.Usage{InputTokens: 400})
	if _, _, ok := r.contextCalibrationRatio("agent-1", "prov:model"); ok {
		t.Fatal("tiny estimates must not produce a calibration sample")
	}

	// CachedInputTokens is a subset of InputTokens on every adapter, so the
	// cached share must not inflate the measured request size.
	r.recordContextCalibration("agent-1", "prov:model", 10000, providers.Usage{InputTokens: 6000, CachedInputTokens: 4000})
	ratio, sample, ok := r.contextCalibrationRatio("agent-1", "prov:model")
	if !ok {
		t.Fatal("expected a calibration sample after a real usage report")
	}
	if sample.ActualTokens != 6000 || sample.EstimatedTokens != 10000 {
		t.Fatalf("unexpected sample: %+v", sample)
	}
	if ratio != 0.6 {
		t.Fatalf("ratio = %v, want 0.6", ratio)
	}

	if _, _, ok := r.contextCalibrationRatio("agent-1", "other:model"); ok {
		t.Fatal("a model switch must invalidate the calibration sample")
	}

	r.recordContextCalibration("agent-2", "prov:model", 1000, providers.Usage{InputTokens: 50000})
	ratio, _, ok = r.contextCalibrationRatio("agent-2", "prov:model")
	if !ok || ratio != contextCalibrationMaxRatio {
		t.Fatalf("ratio = %v ok=%v, want clamped to %v", ratio, ok, contextCalibrationMaxRatio)
	}

	r.recordContextCalibration("agent-3", "prov:model", 100000, providers.Usage{InputTokens: 100})
	ratio, _, ok = r.contextCalibrationRatio("agent-3", "prov:model")
	if !ok || ratio != contextCalibrationMinRatio {
		t.Fatalf("ratio = %v ok=%v, want clamped to %v", ratio, ok, contextCalibrationMinRatio)
	}

	r.recordContextCalibration("agent-4", "prov:model", 10000, providers.Usage{})
	if _, _, ok := r.contextCalibrationRatio("agent-4", "prov:model"); ok {
		t.Fatal("turns without a usage report must not calibrate")
	}
}

func TestEffectiveContextTokenLimit(t *testing.T) {
	cases := []struct {
		name       string
		limit      int
		ratio      float64
		calibrated bool
		want       int
	}{
		{name: "uncalibrated keeps limit", limit: 100000, ratio: 2, calibrated: false, want: 100000},
		{name: "undercounting shrinks window", limit: 100000, ratio: 2, calibrated: true, want: 50000},
		{name: "overcounting expands window", limit: 100000, ratio: 0.5, calibrated: true, want: 200000},
		{name: "floor", limit: 5000, ratio: 2, calibrated: true, want: 4096},
	}
	for _, tc := range cases {
		if got := effectiveContextTokenLimit(tc.limit, tc.ratio, tc.calibrated); got != tc.want {
			t.Fatalf("%s: effectiveContextTokenLimit = %d, want %d", tc.name, got, tc.want)
		}
	}
}

func TestContextStatusAppliesCalibration(t *testing.T) {
	r := &Runner{cfg: config.AgentConfig{ContextTokenLimit: 10000}}
	agent := db.Agent{ID: "agent-1", Model: "prov:model"}
	messages := []db.Message{{ID: "m1", AgentID: agent.ID, Role: "user", ContentText: strings.Repeat("hello ", 500)}}

	base := r.contextStatusForAgent(agent, messages, nil)
	if base.EstimateBasis != contextEstimateBasisHeuristic || base.LastActualInputTokens != 0 {
		t.Fatalf("uncalibrated status = basis %q lastActual %d", base.EstimateBasis, base.LastActualInputTokens)
	}
	if base.EstimatedTokens < contextCalibrationMinSampleTokens {
		t.Fatalf("test conversation too small to calibrate: %d", base.EstimatedTokens)
	}

	r.recordContextCalibration(agent.ID, agent.Model, base.EstimatedTokens, providers.Usage{InputTokens: int64(base.EstimatedTokens) * 2})
	calibrated := r.contextStatusForAgent(agent, messages, nil)
	if calibrated.EstimateBasis != contextEstimateBasisCalibrated {
		t.Fatalf("basis = %q, want calibrated", calibrated.EstimateBasis)
	}
	if calibrated.LastActualInputTokens != int64(base.EstimatedTokens)*2 {
		t.Fatalf("lastActual = %d, want %d", calibrated.LastActualInputTokens, base.EstimatedTokens*2)
	}
	want := calibrateContextTokens(base.EstimatedTokens, 2)
	if calibrated.EstimatedTokens != want {
		t.Fatalf("estimated = %d, want %d", calibrated.EstimatedTokens, want)
	}
	if calibrated.UsagePercent <= base.UsagePercent {
		t.Fatalf("usage percent must grow with an undercounting sample: %d -> %d", base.UsagePercent, calibrated.UsagePercent)
	}
}

func TestEstimateContextImageMetadataTokens(t *testing.T) {
	messages := []db.Message{
		{ID: "m1", Role: "user", Attachments: []db.Attachment{{Kind: "image", Width: 1024, Height: 768, SizeBytes: 3072, ProcessingStatus: media.ProcessingReady}}},
		{ID: "m2", Role: "assistant", GeneratedImages: []db.GeneratedImage{{Status: "ready", Width: 512, Height: 512, ByteSize: 6144}}},
		{ID: "m3", Role: "user", SupersededAt: "2026-01-01T00:00:00Z", Attachments: []db.Attachment{{Kind: "image", Width: 512, Height: 512}}},
		{ID: "m4", Role: "user", Attachments: []db.Attachment{{Kind: "image", ModelData: []byte{1}, Width: 512, Height: 512}}},
		{ID: "m5", Role: "assistant", GeneratedImages: []db.GeneratedImage{{Status: "pending", Width: 512, Height: 512}}},
		{ID: "m6", Role: "user", Attachments: []db.Attachment{{Kind: "image", Width: 512, Height: 512, ProcessingStatus: media.ProcessingRejected}}},
	}

	want := estimateImageTokens(1024, 768) + 1 + estimateImageTokens(512, 512) + 2
	if got := estimateContextImageMetadataTokens(messages, ""); got != want {
		t.Fatalf("supplement = %d, want %d (superseded, data-bearing, pending, and rejected entries must not count)", got, want)
	}

	afterBoundary := estimateImageTokens(512, 512) + 2
	if got := estimateContextImageMetadataTokens(messages, "m1"); got != afterBoundary {
		t.Fatalf("supplement after boundary = %d, want %d", got, afterBoundary)
	}

	if got := estimateContextImageMetadataTokens(nil, ""); got != 0 {
		t.Fatalf("empty conversation supplement = %d, want 0", got)
	}
}

func TestManagedContextGateUsesCalibratedWindow(t *testing.T) {
	// A measured undercount (actual double the estimate) must make the
	// compaction gate fire even though the raw estimate still fits the window.
	ctx := context.Background()
	store, agent := newAgentTestStore(t, t.TempDir(), "acceptEdits")
	defer store.Close()
	var messages []db.Message
	for i := 0; i < 12; i++ {
		// Large enough that the halved effective window stays above the 4096
		// floor, which exists for real provider windows, not test-sized ones.
		msg, err := store.AddMessage(ctx, db.Message{AgentID: agent.ID, Role: "user", ContentText: "message " + string(rune('a'+i)) + " " + strings.Repeat("body ", 500)})
		if err != nil {
			t.Fatal(err)
		}
		messages = append(messages, msg)
	}
	rawEstimate := estimateRequestTokens(agent.SystemPrompt, providerMessagesForContext(agent, messages), nil)
	limit := rawEstimate + rawEstimate/2 // raw sits well below the 90% compact threshold

	uncalibrated := newAgentTestRunner(store, &scriptedProvider{}, config.AgentConfig{ContextTokenLimit: limit, SummaryModel: "missing:test"})
	if _, _, _, err := uncalibrated.managedContextForTurn(ctx, agent, messages, nil, turnSystemControls{}); err != nil {
		t.Fatal(err)
	}
	unchanged, err := store.GetAgent(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.PruneBoundaryMessageID != "" {
		t.Fatal("uncalibrated runner must not compact below the raw threshold")
	}

	calibrated := newAgentTestRunner(store, &scriptedProvider{}, config.AgentConfig{ContextTokenLimit: limit, SummaryModel: "missing:test"})
	calibrated.recordContextCalibration(agent.ID, agent.Model, rawEstimate, providers.Usage{InputTokens: int64(rawEstimate) * 2})
	if _, _, _, err := calibrated.managedContextForTurn(ctx, agent, messages, nil, turnSystemControls{}); err != nil {
		t.Fatal(err)
	}
	compacted, err := store.GetAgent(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if compacted.PruneBoundaryMessageID == "" || compacted.ContextSummary == "" {
		t.Fatalf("calibrated window must trigger compaction: %+v", compacted)
	}
}
