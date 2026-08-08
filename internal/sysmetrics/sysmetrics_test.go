package sysmetrics

import (
	"math"
	"runtime"
	"sync"
	"testing"
	"time"
)

// scripted drives a Sampler from fixed readings so the rate arithmetic can be
// asserted exactly, on any platform, without depending on real machine load.
type scripted struct {
	clock time.Time
	cpu   []cpuTimes
	cpuOK []bool
	net   []netCounters
	netOK []bool
	calls int
}

func newScriptedSampler(s *scripted, memory Memory) *Sampler {
	sampler := NewSampler()
	sampler.now = func() time.Time { return s.clock }
	sampler.readMemory = func() Memory { return memory }
	sampler.readCPU = func() (cpuTimes, bool) {
		index := min(s.calls, len(s.cpu)-1)
		if index < 0 {
			return cpuTimes{}, false
		}
		return s.cpu[index], s.cpuOK[index]
	}
	sampler.readNetwork = func() (netCounters, bool) {
		index := min(s.calls, len(s.net)-1)
		if index < 0 {
			return netCounters{}, false
		}
		return s.net[index], s.netOK[index]
	}
	return sampler
}

func TestFirstSampleReportsRatesUnavailableRatherThanZero(t *testing.T) {
	script := &scripted{
		clock: time.Unix(1000, 0),
		cpu:   []cpuTimes{{Busy: 100, Total: 400}},
		cpuOK: []bool{true},
		net:   []netCounters{{RxBytes: 5_000, TxBytes: 1_000}},
		netOK: []bool{true},
	}
	sampler := newScriptedSampler(script, Memory{Available: true, Percent: 42, UsedBytes: 42, TotalBytes: 100})

	got := sampler.Sample()

	// A single reading of a cumulative counter carries no rate. Reporting 0%
	// would be indistinguishable from a genuinely idle machine.
	if got.CPU.Available {
		t.Errorf("CPU.Available = true on first sample, want false")
	}
	if got.Network.Available {
		t.Errorf("Network.Available = true on first sample, want false")
	}
	// Memory is instantaneous and therefore valid immediately.
	if !got.Memory.Available || got.Memory.Percent != 42 {
		t.Errorf("Memory = %+v, want available with percent 42", got.Memory)
	}
}

func TestSecondSampleReportsCPUAndNetworkRates(t *testing.T) {
	script := &scripted{
		clock: time.Unix(1000, 0),
		cpu: []cpuTimes{
			{Busy: 100, Total: 400},
			// 75 busy ticks out of 300 elapsed ticks = 25%.
			{Busy: 175, Total: 700},
		},
		cpuOK: []bool{true, true},
		net: []netCounters{
			{RxBytes: 1_000, TxBytes: 500},
			// 4000 bytes received and 1000 sent over 2 seconds.
			{RxBytes: 9_000, TxBytes: 2_500},
		},
		netOK: []bool{true, true},
	}
	sampler := newScriptedSampler(script, Memory{Available: true})

	sampler.Sample()
	script.calls = 1
	script.clock = script.clock.Add(2 * time.Second)
	got := sampler.Sample()

	if !got.CPU.Available {
		t.Fatalf("CPU.Available = false, want true")
	}
	if math.Abs(got.CPU.Percent-25) > 1e-9 {
		t.Errorf("CPU.Percent = %v, want 25", got.CPU.Percent)
	}
	if !got.Network.Available {
		t.Fatalf("Network.Available = false, want true")
	}
	if math.Abs(got.Network.RxBytesPerSec-4_000) > 1e-9 {
		t.Errorf("RxBytesPerSec = %v, want 4000", got.Network.RxBytesPerSec)
	}
	if math.Abs(got.Network.TxBytesPerSec-1_000) > 1e-9 {
		t.Errorf("TxBytesPerSec = %v, want 1000", got.Network.TxBytesPerSec)
	}
}

func TestSampleWithinMinIntervalRepeatsRatesButRefreshesMemory(t *testing.T) {
	script := &scripted{
		clock: time.Unix(1000, 0),
		cpu: []cpuTimes{
			{Busy: 100, Total: 400},
			{Busy: 175, Total: 700},
			// Would compute a different figure if it were consulted.
			{Busy: 675, Total: 1_200},
		},
		cpuOK: []bool{true, true, true},
		net: []netCounters{
			{RxBytes: 1_000, TxBytes: 500},
			{RxBytes: 9_000, TxBytes: 2_500},
			{RxBytes: 900_000, TxBytes: 250_000},
		},
		netOK: []bool{true, true, true},
	}
	memory := Memory{Available: true, Percent: 10, UsedBytes: 10, TotalBytes: 100}
	sampler := newScriptedSampler(script, memory)

	sampler.Sample()
	script.calls = 1
	script.clock = script.clock.Add(2 * time.Second)
	established := sampler.Sample()

	// Third call arrives well inside minInterval: dividing a counter delta by
	// ~1ms turns scheduling jitter into a meaningless spike.
	script.calls = 2
	script.clock = script.clock.Add(1 * time.Millisecond)
	memory = Memory{Available: true, Percent: 88, UsedBytes: 88, TotalBytes: 100}
	sampler.readMemory = func() Memory { return memory }
	got := sampler.Sample()

	if got.CPU != established.CPU {
		t.Errorf("CPU = %+v, want the previous interval's %+v", got.CPU, established.CPU)
	}
	if got.Network != established.Network {
		t.Errorf("Network = %+v, want the previous interval's %+v", got.Network, established.Network)
	}
	// Memory needs no interval, so it must not be stale.
	if got.Memory.Percent != 88 {
		t.Errorf("Memory.Percent = %v, want the fresh 88", got.Memory.Percent)
	}
}

func TestCounterResetsAndZeroDeltasDoNotProduceSpikes(t *testing.T) {
	cases := []struct {
		name    string
		samples []cpuTimes
	}{
		// Suspend/resume or a container restart can rewind the counters.
		{name: "cpu counter rewound", samples: []cpuTimes{{Busy: 500, Total: 1_000}, {Busy: 10, Total: 20}}},
		// An idle interval with no ticks at all divides by zero.
		{name: "no elapsed ticks", samples: []cpuTimes{{Busy: 500, Total: 1_000}, {Busy: 500, Total: 1_000}}},
		// Busy going backwards while total advances is not representable.
		{name: "busy rewound alone", samples: []cpuTimes{{Busy: 500, Total: 1_000}, {Busy: 400, Total: 1_200}}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			script := &scripted{
				clock: time.Unix(1000, 0),
				cpu:   testCase.samples,
				cpuOK: []bool{true, true},
				net:   []netCounters{{}, {}},
				netOK: []bool{true, true},
			}
			sampler := newScriptedSampler(script, Memory{Available: true})

			sampler.Sample()
			script.calls = 1
			script.clock = script.clock.Add(2 * time.Second)
			got := sampler.Sample()

			if got.CPU.Available {
				t.Errorf("CPU = %+v, want unavailable rather than a fabricated figure", got.CPU)
			}
		})
	}
}

func TestNetworkCounterRewindReportsZeroNotNegative(t *testing.T) {
	script := &scripted{
		clock: time.Unix(1000, 0),
		cpu:   []cpuTimes{{Busy: 1, Total: 2}, {Busy: 2, Total: 4}},
		cpuOK: []bool{true, true},
		net: []netCounters{
			{RxBytes: 900_000, TxBytes: 800_000},
			// Adapter reset: counters restart from a lower value.
			{RxBytes: 1_000, TxBytes: 500},
		},
		netOK: []bool{true, true},
	}
	sampler := newScriptedSampler(script, Memory{Available: true})

	sampler.Sample()
	script.calls = 1
	script.clock = script.clock.Add(2 * time.Second)
	got := sampler.Sample()

	if got.Network.RxBytesPerSec != 0 || got.Network.TxBytesPerSec != 0 {
		t.Errorf("Network = %+v, want zero rates after a counter rewind", got.Network)
	}
}

func TestUnavailableCollectorsClearHistorySoNoStaleDeltaIsUsed(t *testing.T) {
	script := &scripted{
		clock: time.Unix(1000, 0),
		cpu: []cpuTimes{
			{Busy: 100, Total: 400},
			{},
			// After a gap the pre-gap reading must not be treated as the
			// previous interval; that delta would span an unknown duration.
			{Busy: 900, Total: 1_600},
			{Busy: 1_000, Total: 2_000},
		},
		cpuOK: []bool{true, false, true, true},
		net:   []netCounters{{}, {}, {}, {}},
		netOK: []bool{true, false, true, true},
	}
	sampler := newScriptedSampler(script, Memory{Available: true})

	sampler.Sample()
	script.calls = 1
	script.clock = script.clock.Add(2 * time.Second)
	if got := sampler.Sample(); got.CPU.Available {
		t.Errorf("CPU.Available = true while collection failed, want false")
	}
	script.calls = 2
	script.clock = script.clock.Add(2 * time.Second)
	if got := sampler.Sample(); got.CPU.Available {
		t.Errorf("CPU.Available = true on the first reading after a gap, want false")
	}
	// Only once two genuinely adjacent readings exist does a rate reappear.
	script.calls = 3
	script.clock = script.clock.Add(2 * time.Second)
	got := sampler.Sample()
	if !got.CPU.Available {
		t.Fatalf("CPU.Available = false once two adjacent readings exist, want true")
	}
	// 100 busy of 400 elapsed ticks.
	if math.Abs(got.CPU.Percent-25) > 1e-9 {
		t.Errorf("CPU.Percent = %v, want 25", got.CPU.Percent)
	}
}

func TestPercentClampAndMemoryPercentGuards(t *testing.T) {
	if got := clampPercent(math.NaN()); got != 0 {
		t.Errorf("clampPercent(NaN) = %v, want 0", got)
	}
	if got := clampPercent(-5); got != 0 {
		t.Errorf("clampPercent(-5) = %v, want 0", got)
	}
	// Rounding in the OS counters can push a ratio just past 1.
	if got := clampPercent(100.4); got != 100 {
		t.Errorf("clampPercent(100.4) = %v, want 100", got)
	}
	if got := memoryPercent(10, 0); got != 0 {
		t.Errorf("memoryPercent with zero total = %v, want 0", got)
	}
	if got := memoryPercent(25, 200); math.Abs(got-12.5) > 1e-9 {
		t.Errorf("memoryPercent(25, 200) = %v, want 12.5", got)
	}
}

func TestNilSamplerIsUsable(t *testing.T) {
	var sampler *Sampler
	got := sampler.Sample()
	if got.CPU.Available || got.Memory.Available || got.Network.Available {
		t.Errorf("nil sampler reported availability: %+v", got)
	}
}

func TestConcurrentSamplesAreSerialised(t *testing.T) {
	sampler := NewSampler()
	var group sync.WaitGroup
	for range 16 {
		group.Add(1)
		go func() {
			defer group.Done()
			sampler.Sample()
		}()
	}
	group.Wait()
}

// The platform collectors are exercised rather than asserted on: real values
// depend on the machine. The point is that the syscalls and parsing do not panic
// and that an implemented platform reports a plausible memory total.
func TestPlatformCollectorsRunOnThisHost(t *testing.T) {
	sampler := NewSampler()
	first := sampler.Sample()
	time.Sleep(minInterval + 50*time.Millisecond)
	second := sampler.Sample()

	implemented := runtime.GOOS == "windows" || runtime.GOOS == "linux"
	if !implemented {
		if second.CPU.Available || second.Memory.Available || second.Network.Available {
			t.Errorf("%s has no collector but reported availability: %+v", runtime.GOOS, second)
		}
		return
	}

	if !second.Memory.Available {
		t.Fatalf("memory unavailable on %s: %+v", runtime.GOOS, second.Memory)
	}
	if second.Memory.TotalBytes == 0 || second.Memory.UsedBytes > second.Memory.TotalBytes {
		t.Errorf("implausible memory reading: %+v", second.Memory)
	}
	if second.Memory.Percent < 0 || second.Memory.Percent > 100 {
		t.Errorf("Memory.Percent = %v, out of range", second.Memory.Percent)
	}
	if second.CPU.Available && (second.CPU.Percent < 0 || second.CPU.Percent > 100) {
		t.Errorf("CPU.Percent = %v, out of range", second.CPU.Percent)
	}
	if second.Network.Available && (second.Network.RxBytesPerSec < 0 || second.Network.TxBytesPerSec < 0) {
		t.Errorf("negative network rate: %+v", second.Network)
	}
	if first.CapturedAt.IsZero() || second.CapturedAt.Before(first.CapturedAt) {
		t.Errorf("CapturedAt did not advance: %v then %v", first.CapturedAt, second.CapturedAt)
	}
}
