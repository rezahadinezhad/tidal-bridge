package host

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"tidalbridge/packages/protocol"
	adapter "tidalbridge/packages/task-adapter"
	"time"
)

func TestLocalObservationIsCompletedHistoryNotQueuedWork(t *testing.T) {
	h, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.Close() })
	input := protocol.LocalObservation{Spec: protocol.JobSpec{Argv: []string{"python", "-m", "unittest"}, Profile: "automatic:unittest"}, Decision: &protocol.Decision{Target: "LOCAL", Explanation: "Phone thermal gate prevented routing.", LocalCostMS: 1000}, Started: time.Now().Add(-time.Second), DurationMS: 1000, CPUSeconds: .5}
	j, err := h.RecordLocal(input)
	if err != nil {
		t.Fatal(err)
	}
	if j.State != "COMPLETED" || j.Finished == nil || j.Attempts[0].HostCPUSeconds == nil || *j.Attempts[0].HostCPUSeconds != .5 || len(h.pending) != 0 || h.totalActive != 0 || len(h.history) != 1 {
		t.Fatal(j, h.pending, h.history)
	}
	if j.Decision.Explanation != input.Decision.Explanation || j.Decision.LocalCostMS != 1000 {
		t.Fatal("lost the actual local routing reason", j.Decision)
	}
	input.DurationMS = -1
	if _, err = h.RecordLocal(input); err == nil {
		t.Fatal("accepted invalid observation")
	}
	input.DurationMS = 1000
	invalidRAM := math.Inf(1)
	input.PeakRAMMB = &invalidRAM
	if _, err = h.RecordLocal(input); err == nil {
		t.Fatal("accepted invalid memory observation")
	}
}

func TestRemoteSubmissionNeverTransmitsLocalRuntimePaths(t *testing.T) {
	h, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.Close() })
	submitted := make(chan map[string]any, 1)
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" && r.URL.Path == "/v1/jobs" {
			var body struct {
				Spec map[string]any `json:"spec"`
			}
			json.NewDecoder(r.Body).Decode(&body)
			submitted <- body.Spec
		}
		if strings.Contains(r.URL.Path, "/output") {
			fmt.Fprint(w, `{"next_offset":0,"data_b64":""}`)
		} else {
			fmt.Fprint(w, `{"state":"COMPLETED","exit_code":0}`)
		}
	}))
	defer worker.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var output bytes.Buffer
	code, _, err, _ := h.runRemote(ctx, "test-job", protocol.JobSpec{Argv: []string{"tsc", "--noEmit"}, LocalArgv: []string{`C:\private-runtime\node.exe`, `C:\host-dependencies\tsc`}, Workspace: `R:\private-project`}, protocol.WorkerNode{Endpoint: worker.URL, Token: "test-token"}, nil, protocol.Attempt{ID: "test-attempt"}, &output, &output)
	if err != nil || code != 0 {
		t.Fatal(code, err)
	}
	remote := <-submitted
	if _, ok := remote["local_argv"]; ok {
		t.Fatal("Windows runtime paths left host", remote)
	}
	if remote["workspace"] != "" {
		t.Fatal("absolute host workspace transmitted", remote)
	}
}
func TestAutomationIsExplicitAndReversiblePerProject(t *testing.T) {
	h, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.Close() })
	root := t.TempDir()
	if err = h.ConfigureAutomation(AutomationInput{Workspace: root, Enabled: true, Profiles: []string{"typecheck"}, Provision: true}); err != nil {
		t.Fatal(err)
	}
	project, found, err := adapter.Find(root)
	if err != nil || found != root || !project.Enabled || len(project.Tasks) != 1 || !project.Tasks[0].Provision || project.Tasks[0].Idempotent {
		t.Fatal(project, found, err)
	}
	if err = h.ConfigureAutomation(AutomationInput{Workspace: root, Enabled: false}); err != nil {
		t.Fatal(err)
	}
	project, _, _ = adapter.Find(root)
	if project.Enabled || len(project.Tasks) != 1 {
		t.Fatal("disable destroyed task settings", project)
	}
	if err = h.ConfigureAutomation(AutomationInput{Workspace: "relative", Enabled: true}); err == nil {
		t.Fatal("accepted relative project")
	}
}
func TestHostRuntimeOverrideRunsExactLocalCommand(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	literal := "spaces 'quotes' & $(noop)"
	code, err := runLocal(context.Background(), protocol.JobSpec{Argv: []string{"missing-remote-runtime"}, LocalArgv: []string{exe, "-test.run=^TestLocalRuntimeChild$", "--", literal}, Env: map[string]string{"TIDALBRIDGE_TEST_HELPER": "1"}}, &output, &output)
	if err != nil || code != 7 || strings.TrimSpace(output.String()) != literal {
		t.Fatal(code, err, output.String())
	}
}
func TestLocalRuntimeChild(t *testing.T) {
	if os.Getenv("TIDALBRIDGE_TEST_HELPER") == "1" {
		fmt.Println(os.Args[len(os.Args)-1])
		os.Exit(7)
	}
}
