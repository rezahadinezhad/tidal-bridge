//go:build !windows

package main

// processTree is Windows-only; elsewhere the root process measurements apply.
type processTree struct{}

func trackTree(int) *processTree { return nil }

func (t *processTree) usage() (cpuSeconds, peakMB float64, ok bool) { return 0, 0, false }

func (t *processTree) close() {}
