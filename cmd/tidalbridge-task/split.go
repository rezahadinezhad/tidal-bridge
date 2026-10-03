package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"tidalbridge/apps/cli"
	"tidalbridge/packages/protocol"
	adapter "tidalbridge/packages/task-adapter"
)

// tail keeps the last limit bytes written to it (64 KB by default): enough
// for the summary a test runner prints last.
type tail struct {
	mu      sync.Mutex
	b       []byte
	limit   int
	dropped bool
}

func (t *tail) size() int {
	if t.limit > 0 {
		return t.limit
	}
	return 64 << 10
}

func (t *tail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.b = append(t.b, p...)
	if len(t.b) > 2*t.size() {
		t.b = append(t.b[:0], t.b[len(t.b)-t.size():]...)
		t.dropped = true
	}
	return len(p), nil
}

func (t *tail) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.b) > t.size() {
		return string(t.b[len(t.b)-t.size():])
	}
	return string(t.b)
}

func (t *tail) truncated() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.dropped || len(t.b) > t.size()
}

func reportSuite(client cli.Client, spec protocol.JobSpec, files, splitSum int) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	client.Request(ctx, "POST", "/v1/observations/suite", protocol.SuiteObservation{Spec: spec, Files: files, SplitSum: splitSum}, nil)
}

// terminal reports whether f is an interactive console rather than a pipe.
func terminal(f *os.File) bool {
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

// localSuite runs a command here. For a splittable test suite whose output
// goes to an agent rather than a terminal, it also learns how many test files
// the suite has, which a later split run is checked against.
func localSuite(ctx context.Context, client cli.Client, spec protocol.JobSpec, local []string, report *observed) (int, error) {
	if spec.Shard == "" || terminal(os.Stdout) {
		return passthrough(ctx, local, report)
	}
	summary := &tail{}
	code, err := runLocal(ctx, local, report, os.Stdin, io.MultiWriter(os.Stdout, summary), io.MultiWriter(os.Stderr, summary))
	if err == nil && ctx.Err() == nil {
		if files, ok := adapter.SuiteFiles(summary.String(), spec.Shard); ok {
			reportSuite(client, spec, files, 0)
		}
	}
	return code, err
}

// split runs a test suite in two parts at once: part 1 on this laptop, which
// has room for it now, and part 2 on the phone, so the result comes sooner and
// the phone runs cooler. A part the phone cannot finish runs here. When both
// pass, together they must cover as many test files as the whole suite did,
// or the whole suite runs here to be sure.
func split(ctx context.Context, client cli.Client, spec protocol.JobSpec, decision protocol.Decision, local []string) (int, error) {
	dash := spec.Argv[0] == "npm" && !slices.Contains(spec.Argv, "--")
	part := func(argv []string, index int) []string { return adapter.ShardArgs(argv, dash, index, 2) }
	phoneSpec, laptopSpec := spec, spec
	phoneSpec.Argv, phoneSpec.LocalArgv = part(spec.Argv, 2), part(local, 2)
	phoneSpec.Policy.ForceRemote, phoneSpec.Policy.DeviceID = true, decision.Target
	laptopSpec.Argv = part(spec.Argv, 1)
	fmt.Fprintf(os.Stderr, "[Tidal Bridge] %s -> split in two: part 1 on this laptop, part 2 on %s\n", strings.Join(spec.Argv, " "), decision.Target)
	started := time.Now()
	phoneOut, phoneErr := &tail{limit: 4 << 20}, &tail{limit: 4 << 20}
	type outcome struct {
		code     int
		err      error
		finished protocol.Job
		took     time.Duration
	}
	phone := make(chan outcome, 1)
	go func() {
		var finished protocol.Job
		code, err := client.ExecuteObserved(ctx, phoneSpec, phoneOut, phoneErr, func(j protocol.Job) { finished = j })
		phone <- outcome{code, err, finished, time.Since(started)}
	}()
	laptop := &tail{}
	note := &protocol.Decision{Target: "LOCAL", Explanation: "Part 1 of a split run; part 2 ran on the phone at the same time."}
	code1, err1 := runLocal(ctx, part(local, 1), &observed{client: client, spec: laptopSpec, decision: note}, nil, io.MultiWriter(os.Stdout, laptop), io.MultiWriter(os.Stderr, laptop))
	took1 := time.Since(started)
	p := <-phone
	if ctx.Err() != nil {
		return 130, ctx.Err()
	}
	if err1 != nil {
		return code1, err1
	}
	code2, out2 := p.code, phoneOut.String()+phoneErr.String()
	if p.err != nil || p.finished.Evacuated {
		fmt.Fprintln(os.Stderr, "[Tidal Bridge] The phone could not finish part 2; running it on this laptop.")
		rest := &tail{}
		var err error
		if code2, err = runLocal(ctx, part(local, 2), nil, nil, io.MultiWriter(os.Stdout, rest), io.MultiWriter(os.Stderr, rest)); err != nil {
			return code2, err
		}
		out2 = rest.String()
	} else {
		fmt.Fprintln(os.Stderr, "[Tidal Bridge] Part 2, from the phone:")
		if phoneOut.truncated() || phoneErr.truncated() {
			fmt.Fprintln(os.Stderr, "[Tidal Bridge] (the beginning of its output is omitted)")
		}
		io.WriteString(os.Stdout, phoneOut.String())
		io.WriteString(os.Stderr, phoneErr.String())
	}
	if code1 != 0 || code2 != 0 {
		fmt.Fprintf(os.Stderr, "[Tidal Bridge] The suite failed: part 1 (this laptop) exit %d, part 2 (phone) exit %d.\n", code1, code2)
		return max(code1, code2), nil
	}
	files1, ok1 := adapter.SuiteFiles(laptop.String(), spec.Shard)
	files2, ok2 := adapter.SuiteFiles(out2, spec.Shard)
	if ok1 && ok2 && files1+files2 == decision.SplitFiles {
		fmt.Fprintf(os.Stderr, "[Tidal Bridge] Both parts passed: %d test files in all (this laptop %s, phone %s).\n", files1+files2, took1.Round(time.Second), p.took.Round(time.Second))
		reportSuite(client, spec, files1+files2, 0)
		return 0, nil
	}
	covered := files1 + files2
	if !ok1 || !ok2 {
		covered = -1
	}
	fmt.Fprintf(os.Stderr, "[Tidal Bridge] The parts covered %d test files where the whole suite had %d; running it whole on this laptop to be sure.\n", max(0, covered), decision.SplitFiles)
	whole := &tail{}
	code, err := runLocal(ctx, local, nil, nil, io.MultiWriter(os.Stdout, whole), io.MultiWriter(os.Stderr, whole))
	if err == nil && ctx.Err() == nil {
		files, ok := adapter.SuiteFiles(whole.String(), spec.Shard)
		if !ok {
			files = decision.SplitFiles
		}
		reportSuite(client, spec, files, covered)
	}
	return code, err
}
