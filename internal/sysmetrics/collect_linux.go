//go:build linux

package sysmetrics

import (
	"bufio"
	"os"
	"strconv"
	"strings"
)

const (
	procStatPath    = "/proc/stat"
	procMeminfoPath = "/proc/meminfo"
	procNetDevPath  = "/proc/net/dev"
)

// collectCPUTimes reads the aggregate "cpu" line of /proc/stat. Fields are
// jiffies per state; busy is everything except idle and iowait, because a
// process waiting on disk is not consuming processor time.
func collectCPUTimes() (cpuTimes, bool) {
	file, err := os.Open(procStatPath)
	if err != nil {
		return cpuTimes{}, false
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 5 || fields[0] != "cpu" {
			continue
		}
		var total, idle float64
		for index, field := range fields[1:] {
			value, err := strconv.ParseFloat(field, 64)
			if err != nil {
				return cpuTimes{}, false
			}
			total += value
			// Fields after user/nice/system are idle (3) and iowait (4).
			if index == 3 || index == 4 {
				idle += value
			}
		}
		if total <= 0 || idle > total {
			return cpuTimes{}, false
		}
		return cpuTimes{Busy: total - idle, Total: total}, true
	}
	return cpuTimes{}, false
}

// collectMemory reads MemTotal and MemAvailable from /proc/meminfo. MemAvailable
// is the kernel's own estimate of what a new allocation could claim; deriving
// used from MemFree instead would count reclaimable page cache as used and
// report almost every Linux host as nearly full.
func collectMemory() Memory {
	file, err := os.Open(procMeminfoPath)
	if err != nil {
		return Memory{}
	}
	defer file.Close()

	var total, available uint64
	var haveTotal, haveAvailable bool
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 {
			continue
		}
		value, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			continue
		}
		// Values are in kB.
		switch fields[0] {
		case "MemTotal:":
			total, haveTotal = value*1024, true
		case "MemAvailable:":
			available, haveAvailable = value*1024, true
		}
		if haveTotal && haveAvailable {
			break
		}
	}
	if !haveTotal || !haveAvailable || total == 0 || available > total {
		return Memory{}
	}
	used := total - available
	return Memory{Available: true, Percent: memoryPercent(used, total), UsedBytes: used, TotalBytes: total}
}

// collectNetCounters sums the receive and transmit byte columns of /proc/net/dev
// across every interface except loopback.
func collectNetCounters() (netCounters, bool) {
	file, err := os.Open(procNetDevPath)
	if err != nil {
		return netCounters{}, false
	}
	defer file.Close()

	totals := netCounters{}
	counted := 0
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		// Lines read "iface: rx_bytes rx_packets ... tx_bytes tx_packets ...";
		// the two header lines have no colon.
		name, rest, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		name = strings.TrimSpace(name)
		if name == "" || name == "lo" {
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) < 9 {
			continue
		}
		rx, rxErr := strconv.ParseUint(fields[0], 10, 64)
		tx, txErr := strconv.ParseUint(fields[8], 10, 64)
		if rxErr != nil || txErr != nil {
			continue
		}
		totals.RxBytes += rx
		totals.TxBytes += tx
		counted++
	}
	if counted == 0 {
		return netCounters{}, false
	}
	return totals, true
}
