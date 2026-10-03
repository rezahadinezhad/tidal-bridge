package config

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestSecretConcurrentProcessesAgree(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "first-start")
	const callers = 12
	commands := make([]*exec.Cmd, callers)
	for i := range commands {
		commands[i] = exec.Command(os.Args[0], "-test.run=^TestSecretProcess$")
		commands[i].Env = append(os.Environ(), "TIDALBRIDGE_SECRET_TEST_DIR="+dir)
	}
	type result struct {
		token string
		err   error
	}
	start := make(chan struct{})
	results := make(chan result, callers)
	for _, command := range commands {
		go func() {
			<-start
			output, err := command.CombinedOutput()
			results <- result{strings.TrimSpace(string(output)), err}
		}()
	}
	close(start)
	persisted := ""
	for range callers {
		result := <-results
		if result.err != nil {
			t.Errorf("concurrent secret creation failed: %v", result.err)
			continue
		}
		if len(result.token) != 48 {
			t.Errorf("secret length = %d, want 48", len(result.token))
		}
		if persisted == "" {
			persisted = result.token
		} else if result.token != persisted {
			t.Error("concurrent processes received different host credentials")
		}
	}
	actual, err := Secret(dir)
	if err != nil || actual != persisted {
		t.Error("saved host credential differs from a startup caller")
	}
}

func TestSecretProcess(t *testing.T) {
	dir := os.Getenv("TIDALBRIDGE_SECRET_TEST_DIR")
	if dir == "" {
		return
	}
	token, err := Secret(dir)
	if err != nil {
		os.Exit(1)
	}
	os.Stdout.WriteString(token)
	os.Exit(0)
}

func TestSecretPreservesExistingCredential(t *testing.T) {
	dir := t.TempDir()
	want := strings.Repeat("a", 48)
	if err := os.WriteFile(filepath.Join(dir, "host.token"), []byte(want+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := Secret(dir)
	if err != nil || got != want {
		t.Fatal("existing credential was not preserved")
	}
}
