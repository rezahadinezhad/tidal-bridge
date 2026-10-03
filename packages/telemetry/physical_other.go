//go:build !windows

package telemetry

// physicalMemoryMB is unknown here; the total the system reports stands.
func physicalMemoryMB() uint64 { return 0 }
