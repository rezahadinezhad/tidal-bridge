package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"sync"
	"testing"
	"time"

	"tidalbridge/apps/cli"
	"tidalbridge/packages/protocol"
)

// TestHelperRunner stands in for a test runner on this laptop: it prints the
// summary in TIDALBRIDGE_HELPER and exits with TIDALBRIDGE_HELPER_EXIT.
func TestHelperRunner(t *testing.T) {
	if os.Getenv("TIDALBRIDGE_HELPER") == "" {
		return
	}
	fmt.Println(os.Getenv("TIDALBRIDGE_HELPER"))
	code := 0
	fmt.Sscan(os.Getenv("TIDALBRIDGE_HELPER_EXIT"), &code)
	os.Exit(code)
}

// fakeHost serves what a split run asks the host for; the phone's part
// prints output and exits with code.
type fakeHost struct {
	mu     sync.Mutex
	phone  protocol.JobSpec
	suites []protocol.SuiteObservation
	locals int
}

func (f *fakeHost) serve(output string, code int) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		now := time.Now()
		switch r.URL.Path {
		case "/v1/jobs":
			json.NewDecoder(r.Body).Decode(&f.phone)
			json.NewEncoder(w).Encode(protocol.Job{ID: "j1", State: "QUEUED"})
		case "/v1/jobs/j1":
			json.NewEncoder(w).Encode(protocol.Job{ID: "j1", State: "COMPLETED", ExitCode: &code, Finished: &now, Attempts: []protocol.Attempt{{ID: "a1"}}})
		case "/v1/jobs/j1/output":
			offset, data := 0, ""
			fmt.Sscan(r.URL.Query().Get("offset"), &offset)
			if r.URL.Query().Get("stream") == "stdout" && offset == 0 {
				data = output
			}
			json.NewEncoder(w).Encode(map[string]any{"data_b64": base64.StdEncoding.EncodeToString([]byte(data)), "next_offset": offset + len(data)})
		case "/v1/observations/suite":
			var s protocol.SuiteObservation
			json.NewDecoder(r.Body).Decode(&s)
			f.suites = append(f.suites, s)
			w.Write([]byte("{}"))
		case "/v1/observations/local":
			f.locals++
			w.Write([]byte("{}"))
		default:
			w.Write([]byte("{}"))
		}
	}))
}

func TestSplitJoinsBothParts(t *testing.T) {
	for _, c := range []struct {
		name                  string
		laptop, phone         string
		laptopExit, phoneExit int
		want                  int
		report                *protocol.SuiteObservation
	}{
		{"both pass and cover the suite", " Test Files  2 passed (2)", " Test Files  3 passed (3)", 0, 0, 0, &protocol.SuiteObservation{Files: 5}},
		{"a failing part fails the suite", " Test Files  1 failed | 1 passed (2)", " Test Files  3 passed (3)", 1, 0, 1, nil},
		{"the phone's failing part fails it too", " Test Files  2 passed (2)", " Test Files  1 failed | 2 passed (3)", 0, 1, 1, nil},
		{"parts that miss files run the suite whole", " Test Files  2 passed (2)", " Test Files  2 passed (2)", 0, 0, 0, &protocol.SuiteObservation{Files: 2, SplitSum: 4}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("TIDALBRIDGE_HELPER", c.laptop)
			t.Setenv("TIDALBRIDGE_HELPER_EXIT", fmt.Sprint(c.laptopExit))
			host := &fakeHost{}
			server := host.serve(c.phone+"\n", c.phoneExit)
			defer server.Close()
			client := cli.Client{Endpoint: server.URL, Token: "t", Quiet: true}
			spec := protocol.JobSpec{Argv: []string{"vitest", "run"}, Shard: "vitest", Profile: "detected:vitest"}
			local := []string{os.Args[0], "-test.run=^TestHelperRunner$", "--"}
			code, err := split(context.Background(), client, spec, protocol.Decision{Target: "worker-a", Split: 2, SplitFiles: 5}, local)
			if err != nil || code != c.want {
				t.Fatal(code, err)
			}
			host.mu.Lock()
			defer host.mu.Unlock()
			if !slices.Equal(host.phone.Argv, []string{"vitest", "run", "--shard=2/2", "--passWithNoTests"}) || !host.phone.Policy.ForceRemote || host.phone.Policy.DeviceID != "worker-a" || host.locals != 1 {
				t.Fatal("part 2 goes to the chosen phone, part 1 is recorded here", host.phone.Argv, host.phone.Policy, host.locals)
			}
			if c.report == nil && len(host.suites) > 0 || c.report != nil && (len(host.suites) != 1 || host.suites[0].Files != c.report.Files || host.suites[0].SplitSum != c.report.SplitSum) {
				t.Fatal("suite size reported", host.suites)
			}
		})
	}
}
