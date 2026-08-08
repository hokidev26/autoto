//go:build windows

package sysmetrics

import (
	"net"
	"unsafe"

	"golang.org/x/sys/windows"
)

// GetSystemTimes and GlobalMemoryStatusEx are not wrapped by x/sys/windows, so
// they are declared here with the same NewLazySystemDLL pattern the rest of the
// repository uses for one-off kernel32 calls.
var (
	kernel32                 = windows.NewLazySystemDLL("kernel32.dll")
	procGetSystemTimes       = kernel32.NewProc("GetSystemTimes")
	procGlobalMemoryStatusEx = kernel32.NewProc("GlobalMemoryStatusEx")
)

// memoryStatusEx mirrors MEMORYSTATUSEX. Length must be set to the struct size
// before the call or GlobalMemoryStatusEx rejects it.
type memoryStatusEx struct {
	Length               uint32
	MemoryLoad           uint32
	TotalPhys            uint64
	AvailPhys            uint64
	TotalPageFile        uint64
	AvailPageFile        uint64
	TotalVirtual         uint64
	AvailVirtual         uint64
	AvailExtendedVirtual uint64
}

func filetimeTicks(value windows.Filetime) float64 {
	return float64(uint64(value.HighDateTime)<<32 | uint64(value.LowDateTime))
}

func collectCPUTimes() (cpuTimes, bool) {
	var idle, kernel, user windows.Filetime
	r1, _, _ := procGetSystemTimes.Call(
		uintptr(unsafe.Pointer(&idle)),
		uintptr(unsafe.Pointer(&kernel)),
		uintptr(unsafe.Pointer(&user)),
	)
	if r1 == 0 {
		return cpuTimes{}, false
	}

	idleTicks := filetimeTicks(idle)
	// Windows folds idle time into the kernel figure, so kernel+user is the
	// total and busy is that total minus idle. Adding kernel and user and
	// calling it "busy" is the classic mistake here and reports a permanently
	// busy machine.
	total := filetimeTicks(kernel) + filetimeTicks(user)
	if total <= 0 || idleTicks > total {
		return cpuTimes{}, false
	}
	return cpuTimes{Busy: total - idleTicks, Total: total}, true
}

func collectMemory() Memory {
	status := memoryStatusEx{}
	status.Length = uint32(unsafe.Sizeof(status))
	r1, _, _ := procGlobalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&status)))
	if r1 == 0 || status.TotalPhys == 0 || status.AvailPhys > status.TotalPhys {
		return Memory{}
	}
	used := status.TotalPhys - status.AvailPhys
	return Memory{
		Available:  true,
		Percent:    memoryPercent(used, status.TotalPhys),
		UsedBytes:  used,
		TotalBytes: status.TotalPhys,
	}
}

func collectNetCounters() (netCounters, bool) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return netCounters{}, false
	}

	totals := netCounters{}
	counted := 0
	for _, candidate := range interfaces {
		// Loopback traffic is not network throughput, and an interface that is
		// down contributes a frozen counter that only adds noise when it comes
		// back up.
		if candidate.Flags&net.FlagLoopback != 0 || candidate.Flags&net.FlagUp == 0 {
			continue
		}
		row := windows.MibIfRow2{InterfaceIndex: uint32(candidate.Index)}
		// MibIfRow2 rather than MibIfRow: the older struct's octet counters are
		// 32-bit and wrap every few gigabytes on a fast link.
		if err := windows.GetIfEntry2Ex(windows.MibIfEntryNormal, &row); err != nil {
			continue
		}
		totals.RxBytes += row.InOctets
		totals.TxBytes += row.OutOctets
		counted++
	}
	if counted == 0 {
		return netCounters{}, false
	}
	return totals, true
}
