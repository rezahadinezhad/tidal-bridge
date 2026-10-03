package telemetry

import (
	"sync"
	"syscall"
	"unsafe"
)

var installed = sync.OnceValue(func() uint64 {
	var kb uint64
	proc := syscall.NewLazyDLL("kernel32.dll").NewProc("GetPhysicallyInstalledSystemMemory")
	if ok, _, _ := proc.Call(uintptr(unsafe.Pointer(&kb))); ok == 0 {
		return 0
	}
	return kb >> 10
})

// physicalMemoryMB is the installed memory, as Task Manager shows it; Windows
// can use slightly less (firmware and integrated graphics keep some).
func physicalMemoryMB() uint64 { return installed() }
