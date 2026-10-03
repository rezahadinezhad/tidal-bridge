//go:build windows

package main

import (
	"os/exec"
	"testing"
)

func TestProcessTreeCountsDescendants(t *testing.T) {
	// The outer cmd only waits; the inner one does the work.
	cmd := exec.Command("cmd", "/d", "/c", "cmd /d /c for /l %i in (1,1,400000) do @rem")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	tree := trackTree(cmd.Process.Pid)
	defer tree.close()
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	cpu, peak, ok := tree.usage()
	if !ok {
		t.Fatal("job accounting unavailable")
	}
	root := cmd.ProcessState.UserTime().Seconds() + cmd.ProcessState.SystemTime().Seconds()
	if cpu <= root || cpu < 0.05 || peak <= 0 {
		t.Fatalf("tree cpu %.3fs, root cpu %.3fs, peak %.1f MB", cpu, root, peak)
	}
}
