//go:build windows

package process

import (
	"errors"
	"fmt"
	"os/exec"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Windows Job Object constants (process-group equivalent for managed trees).
const (
	jobObjectExtendedLimitInformationClass = 9
	createNewProcessGroup                  = 0x00000200
	createNoWindow                         = 0x08000000
)

type windowsGroup struct {
	mu  sync.Mutex
	job windows.Handle
}

type ioCounters struct {
	ReadOperationCount  uint64
	WriteOperationCount uint64
	OtherOperationCount uint64
	ReadTransferCount   uint64
	WriteTransferCount  uint64
	OtherTransferCount  uint64
}

type jobObjectBasicLimitInformation struct {
	PerProcessUserTimeLimit int64
	PerJobUserTimeLimit     int64
	LimitFlags              uint32
	MinimumWorkingSetSize   uintptr
	MaximumWorkingSetSize   uintptr
	ActiveProcessLimit      uint32
	Affinity                uintptr
	PriorityClass           uint32
	SchedulingClass         uint32
}

type jobObjectExtendedLimitInfo struct {
	BasicLimitInformation jobObjectBasicLimitInformation
	IoInfo                ioCounters
	ProcessMemoryLimit    uintptr
	JobMemoryLimit        uintptr
	PeakProcessMemoryUsed uintptr
	PeakJobMemoryUsed     uintptr
}

func preparePlatform(cmd *exec.Cmd) platformGroup {
	// CREATE_NEW_PROCESS_GROUP keeps console control behavior predictable;
	// the Job Object is what reaps the full tree on close. Merge rather than
	// replace: a caller may already have set CmdLine to control how the shell
	// receives its command, and dropping that changes what actually runs.
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= createNewProcessGroup
	// CREATE_NO_WINDOW: every process started through this package is a piped
	// background child -- shell tools, MCP stdio servers, LSP servers, preview and
	// lifecycle commands -- and none of them has a user at a console. Without this
	// each one allocates its own console, which the desktop build shows as a
	// console window flashing on screen for the lifetime of the command.
	//
	// Safe alongside the flag above because nothing here delivers console control
	// events: terminate and kill both work through the Job Object, falling back to
	// Process.Kill. It is only invalid combined with CREATE_NEW_CONSOLE or
	// DETACHED_PROCESS, neither of which this package sets.
	cmd.SysProcAttr.CreationFlags |= createNoWindow
	return &windowsGroup{}
}

func (g *windowsGroup) started(cmd *exec.Cmd) error {
	if g == nil || cmd == nil || cmd.Process == nil {
		return nil
	}
	job, err := createKillOnCloseJob()
	if err != nil {
		return err
	}
	if err := assignProcessToJob(job, cmd.Process.Pid); err != nil {
		_ = windows.CloseHandle(job)
		return err
	}
	g.mu.Lock()
	g.job = job
	g.mu.Unlock()
	return nil
}

func (g *windowsGroup) terminate(cmd *exec.Cmd, done <-chan error, grace time.Duration) error {
	// Prefer job-level kill so grandchildren exit with the shell.
	_ = g.kill(cmd)
	if done == nil {
		return nil
	}
	timer := time.NewTimer(grace)
	defer timer.Stop()
	select {
	case err := <-done:
		return err
	case <-timer.C:
		_ = g.kill(cmd)
		return <-done
	}
}

func (g *windowsGroup) kill(cmd *exec.Cmd) error {
	g.mu.Lock()
	job := g.job
	g.mu.Unlock()
	if job != 0 {
		// Closing the job with KILL_ON_JOB_CLOSE terminates members.
		g.mu.Lock()
		if g.job == job {
			g.job = 0
		}
		g.mu.Unlock()
		return windows.CloseHandle(job)
	}
	if cmd != nil && cmd.Process != nil {
		return cmd.Process.Kill()
	}
	return nil
}

func (g *windowsGroup) close() error {
	return g.kill(nil)
}

func createKillOnCloseJob() (windows.Handle, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0, fmt.Errorf("create job object: %w", err)
	}
	var info jobObjectExtendedLimitInfo
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if err := setJobInformation(job, jobObjectExtendedLimitInformationClass, unsafe.Pointer(&info), uint32(unsafe.Sizeof(info))); err != nil {
		_ = windows.CloseHandle(job)
		return 0, err
	}
	return job, nil
}

func setJobInformation(job windows.Handle, class uint32, info unsafe.Pointer, length uint32) error {
	r1, _, err := windows.NewLazySystemDLL("kernel32.dll").NewProc("SetInformationJobObject").Call(
		uintptr(job),
		uintptr(class),
		uintptr(info),
		uintptr(length),
	)
	if r1 == 0 {
		if err != nil {
			return fmt.Errorf("SetInformationJobObject: %w", err)
		}
		return errors.New("SetInformationJobObject failed")
	}
	return nil
}

func assignProcessToJob(job windows.Handle, pid int) error {
	access := uint32(windows.PROCESS_SET_QUOTA | windows.PROCESS_TERMINATE | windows.PROCESS_DUP_HANDLE | windows.PROCESS_QUERY_INFORMATION)
	handle, err := windows.OpenProcess(access, false, uint32(pid))
	if err != nil {
		return fmt.Errorf("OpenProcess: %w", err)
	}
	defer windows.CloseHandle(handle)
	if err := windows.AssignProcessToJobObject(job, handle); err != nil {
		return fmt.Errorf("AssignProcessToJobObject: %w", err)
	}
	return nil
}
