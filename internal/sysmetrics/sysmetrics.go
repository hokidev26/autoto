// Package sysmetrics reports host CPU, memory, and network utilisation for the
// home dashboard's resource cards.
//
// CPU and network are rate quantities: the OS exposes monotonic counters, not
// percentages, so a single reading says nothing on its own. Sampler keeps the
// previous reading and reports the average over the interval between two calls.
// The first call therefore has no CPU or network figure to give, and says so via
// Available rather than reporting a zero that would read as "the machine is
// idle".
//
// Collection is per-platform and deliberately dependency-free (the repository
// already vendors golang.org/x/sys). Platforms without an implementation report
// everything unavailable so the UI can hide the cards instead of showing
// fabricated numbers.
package sysmetrics

import (
	"sync"
	"time"
)

// minInterval guards the rate maths. Two samples taken microseconds apart divide
// a tiny counter delta by a tiny duration, which amplifies scheduling jitter
// into wild percentages; below this the previous result is repeated instead.
const minInterval = 200 * time.Millisecond

// CPU reports processor utilisation averaged over the interval between the two
// most recent samples.
type CPU struct {
	Available bool    `json:"available"`
	Percent   float64 `json:"percent"`
}

// Memory reports physical memory in use. Unlike CPU and network this is an
// instantaneous reading, so it is valid on the very first sample.
type Memory struct {
	Available  bool    `json:"available"`
	Percent    float64 `json:"percent"`
	UsedBytes  uint64  `json:"usedBytes"`
	TotalBytes uint64  `json:"totalBytes"`
}

// Network reports throughput across all non-loopback interfaces, averaged over
// the interval between the two most recent samples.
type Network struct {
	Available     bool    `json:"available"`
	RxBytesPerSec float64 `json:"rxBytesPerSec"`
	TxBytesPerSec float64 `json:"txBytesPerSec"`
}

// Sample is one reading of all three metrics.
type Sample struct {
	CapturedAt time.Time `json:"-"`
	CPU        CPU       `json:"cpu"`
	Memory     Memory    `json:"memory"`
	Network    Network   `json:"network"`
}

// cpuTimes holds the raw counters a platform reports for processor time. Units
// are platform-specific and irrelevant: only the ratio of the deltas is used.
type cpuTimes struct {
	// Busy and Total are cumulative since boot. Total includes Busy.
	Busy  float64
	Total float64
}

// netCounters holds cumulative bytes across all counted interfaces.
type netCounters struct {
	RxBytes uint64
	TxBytes uint64
}

// Sampler turns the platform's cumulative counters into rates. Safe for
// concurrent use: an HTTP handler may be entered from several requests at once,
// and the stored previous reading is shared mutable state.
type Sampler struct {
	mu sync.Mutex

	// Injected rather than called directly so the rate arithmetic can be tested
	// against scripted counters on any platform. Held per-Sampler rather than in
	// package globals so tests do not interfere with each other.
	now         func() time.Time
	readCPU     func() (cpuTimes, bool)
	readMemory  func() Memory
	readNetwork func() (netCounters, bool)

	lastAt time.Time
	// Retained so a call that arrives inside minInterval can answer with the
	// previous rates rather than either blocking or dividing by ~0.
	lastResult Sample
	haveResult bool

	lastCPU cpuTimes
	haveCPU bool
	lastNet netCounters
	haveNet bool
}

// NewSampler returns a Sampler with no history. The first Sample reports CPU and
// network as unavailable.
func NewSampler() *Sampler {
	return &Sampler{
		now:         time.Now,
		readCPU:     collectCPUTimes,
		readMemory:  collectMemory,
		readNetwork: collectNetCounters,
	}
}

// Sample reads the host counters and reports utilisation. Collection failures
// are not errors: an unavailable metric is a normal outcome on platforms or in
// containers that do not expose it, and the caller renders around it.
func (s *Sampler) Sample() Sample {
	if s == nil {
		return Sample{CapturedAt: time.Now()}
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	nowFn := s.now
	if nowFn == nil {
		nowFn = time.Now
	}
	now := nowFn()

	// Memory is instantaneous, so it is always current even when the rate
	// metrics have to reuse the previous interval's answer.
	memory := Memory{}
	if s.readMemory != nil {
		memory = s.readMemory()
	}

	elapsed := now.Sub(s.lastAt)
	if s.haveResult && (elapsed < minInterval || elapsed <= 0) {
		result := s.lastResult
		result.CapturedAt = now
		result.Memory = memory
		return result
	}

	result := Sample{CapturedAt: now, Memory: memory}
	result.CPU = s.sampleCPU()
	result.Network = s.sampleNetwork(elapsed)

	s.lastAt = now
	s.lastResult = result
	s.haveResult = true
	return result
}

func (s *Sampler) sampleCPU() CPU {
	if s.readCPU == nil {
		return CPU{}
	}
	current, ok := s.readCPU()
	if !ok {
		s.haveCPU = false
		return CPU{}
	}
	previous, hadPrevious := s.lastCPU, s.haveCPU
	s.lastCPU = current
	s.haveCPU = true
	if !hadPrevious {
		return CPU{}
	}

	busyDelta := current.Busy - previous.Busy
	totalDelta := current.Total - previous.Total
	// A counter reset (suspend/resume, container restart, or a rolled-over
	// 32-bit tick count) makes a delta negative or nonsensical. Treat it as a
	// missing interval and wait for the next one rather than emitting a spike.
	if totalDelta <= 0 || busyDelta < 0 {
		return CPU{}
	}
	return CPU{Available: true, Percent: clampPercent(busyDelta / totalDelta * 100)}
}

func (s *Sampler) sampleNetwork(elapsed time.Duration) Network {
	if s.readNetwork == nil {
		return Network{}
	}
	current, ok := s.readNetwork()
	if !ok {
		s.haveNet = false
		return Network{}
	}
	previous, hadPrevious := s.lastNet, s.haveNet
	s.lastNet = current
	s.haveNet = true
	if !hadPrevious || elapsed < minInterval {
		return Network{}
	}

	seconds := elapsed.Seconds()
	if seconds <= 0 {
		return Network{}
	}
	return Network{
		Available:     true,
		RxBytesPerSec: counterRate(current.RxBytes, previous.RxBytes, seconds),
		TxBytesPerSec: counterRate(current.TxBytes, previous.TxBytes, seconds),
	}
}

// counterRate converts a delta of cumulative bytes into bytes per second. A
// counter that went backwards (interface removed, adapter reset) yields 0
// rather than a negative or absurdly large rate.
func counterRate(current, previous uint64, seconds float64) float64 {
	if current < previous || seconds <= 0 {
		return 0
	}
	return float64(current-previous) / seconds
}

func clampPercent(value float64) float64 {
	// NaN fails both comparisons, so it is caught by the final guard.
	if value < 0 {
		return 0
	}
	if value > 100 {
		return 100
	}
	if value != value {
		return 0
	}
	return value
}

func memoryPercent(used, total uint64) float64 {
	if total == 0 {
		return 0
	}
	return clampPercent(float64(used) / float64(total) * 100)
}
