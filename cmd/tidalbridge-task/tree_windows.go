//go:build windows

package main

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// jobAccounting is JOBOBJECT_BASIC_ACCOUNTING_INFORMATION.
type jobAccounting struct {
	TotalUserTime             int64 // 100 ns units
	TotalKernelTime           int64
	ThisPeriodTotalUserTime   int64
	ThisPeriodTotalKernelTime int64
	TotalPageFaultCount       uint32
	TotalProcesses            uint32
	ActiveProcesses           uint32
	TotalTerminatedProcesses  uint32
}

// processTree accounts a command and every process it starts through a job
// object. `npm run typecheck` does its work in descendants (cmd, node, tsc);
// the root process alone made a type check look like 0.3 CPU-seconds, too
// small to be worth moving. The job only measures: it sets no limits and
// does not kill the tree when this adapter exits.
type processTree struct{ job windows.Handle }

// trackTree starts accounting for pid and the processes it creates from now on.
func trackTree(pid int) *processTree {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil
	}
	process, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		windows.CloseHandle(job)
		return nil
	}
	defer windows.CloseHandle(process)
	if windows.AssignProcessToJobObject(job, process) != nil {
		windows.CloseHandle(job)
		return nil
	}
	return &processTree{job: job}
}

// usage returns CPU seconds and peak committed memory (MB) of the whole tree.
func (t *processTree) usage() (cpuSeconds, peakMB float64, ok bool) {
	if t == nil {
		return 0, 0, false
	}
	var accounting jobAccounting
	if windows.QueryInformationJobObject(t.job, windows.JobObjectBasicAccountingInformation, uintptr(unsafe.Pointer(&accounting)), uint32(unsafe.Sizeof(accounting)), nil) != nil {
		return 0, 0, false
	}
	var limits windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	if windows.QueryInformationJobObject(t.job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits)), nil) == nil {
		peakMB = float64(limits.PeakJobMemoryUsed) / (1 << 20)
	}
	return float64(accounting.TotalUserTime+accounting.TotalKernelTime) / 1e7, peakMB, true
}

func (t *processTree) close() {
	if t != nil {
		windows.CloseHandle(t.job)
	}
}
