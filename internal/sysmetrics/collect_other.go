//go:build !windows && !linux

package sysmetrics

// Platforms without a collector report everything unavailable. macOS would need
// host_statistics64/sysctl plumbing (and cgo for the cleanest route) for what is
// a supplementary dashboard panel; the honest answer is "not measured here" so
// the UI hides the cards rather than drawing zeros that look like real readings.

func collectCPUTimes() (cpuTimes, bool) {
	return cpuTimes{}, false
}

func collectMemory() Memory {
	return Memory{}
}

func collectNetCounters() (netCounters, bool) {
	return netCounters{}, false
}
