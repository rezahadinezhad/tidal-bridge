// Command adapters are placed first on an agent's PATH. Ordinary tools outside
// explicitly enabled projects run exactly as before through the original
// local runtime; approved, portable commands may run on a worker instead.
package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/shirou/gopsutil/v4/process"
	"tidalbridge/apps/cli"
	"tidalbridge/packages/protocol"
	adapter "tidalbridge/packages/task-adapter"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	code, err := run(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Tidal Bridge adapter:", err)
	}
	os.Exit(code)
}

func run(ctx context.Context) (int, error) {
	exe, err := os.Executable()
	if err != nil {
		return 1, err
	}
	var installation adapter.Installation
	if err = adapter.ReadJSON(filepath.Join(filepath.Dir(exe), "shims.json"), &installation); err != nil {
		return 1, err
	}
	if installation.Version != 1 {
		return 1, fmt.Errorf("unsupported adapter installation")
	}
	installation.SelfDir = filepath.Dir(exe)
	tool := strings.TrimSuffix(strings.ToLower(filepath.Base(exe)), ".exe")
	cwd, err := os.Getwd()
	if err != nil {
		return 1, err
	}
	local, err := adapter.ResolveLocal(installation, tool, cwd, os.Args[1:])
	if err != nil {
		return 127, err
	}
	internal := os.Getenv("TIDALBRIDGE_INTERNAL") == "1"
	if internal || os.Getenv("TIDALBRIDGE_DISABLE") == "1" {
		// Fast path: nested invocations (a Node tool spawning node) and the
		// explicit bypass never consult the scheduler.
		return passthrough(ctx, local, nil)
	}
	// Tidal Bridge decides where a command runs, not the agent running it:
	// TIDALBRIDGE_FORCE_LOCAL is ignored. Pause offloading or list a folder
	// under "Never offloaded" in the dashboard to keep work on this laptop.
	forceRemote, forceLocal := os.Getenv("TIDALBRIDGE_FORCE_REMOTE") == "1", false
	argv := adapter.Canonical(tool, os.Args[1:])
	project, root, projectErr := adapter.Find(cwd)
	if os.IsNotExist(projectErr) {
		// No task file: universal mode may still recognize a safe command.
		if implicit, implicitRoot, ok := adapter.Detect(cwd, argv, adapter.LoadSettings(installation.DataDir)); ok {
			project, root, projectErr = implicit, implicitRoot, nil
		}
	}
	spec, eligible := adapter.Spec(project, root, cwd, argv)
	if !eligible && projectErr == nil && project.Enabled && !project.Detected {
		// An enabled parent task file that does not cover this command leaves
		// a nested project with its own lock file to universal mode.
		if implicit, implicitRoot, ok := adapter.Detect(cwd, argv, adapter.LoadSettings(installation.DataDir)); ok && adapter.Within(implicitRoot, root) && !adapter.Within(root, implicitRoot) {
			project, root = implicit, implicitRoot
			spec, eligible = adapter.Spec(project, root, cwd, argv)
		}
	}
	eligible = eligible && projectErr == nil
	if forceRemote && !eligible {
		return 1, fmt.Errorf("required remote command is not approved, detected or portable")
	}
	if !eligible {
		return passthrough(ctx, local, nil)
	}
	// Approved command: ask the host. A short wait lets the background service
	// start; otherwise the command simply runs locally this time.
	client, clientErr := cli.Connect(ctx, installation.DataDir, 3*time.Second)
	if clientErr != nil {
		if forceRemote {
			return 1, fmt.Errorf("required remote route unavailable: %w", clientErr)
		}
		return passthrough(ctx, local, nil)
	}
	client.Quiet = true
	spec.LocalArgv = local
	switch os.Getenv("TIDALBRIDGE_ENGINE") {
	case "native":
		spec.Engine = "native"
	case "proot":
		spec.Engine = ""
	}
	spec.EnvironmentFingerprint = adapter.EnvironmentFingerprint(cwd, local)
	spec.Policy.ForceLocal = forceLocal
	spec.Policy.ForceRemote = forceRemote
	spec.Shard = adapter.Shardable(spec, root, cwd)
	previewWait := 6 * time.Second
	if forceRemote {
		previewWait = 70 * time.Second
	}
	preview, cancel := context.WithTimeout(ctx, previewWait)
	var decision protocol.Decision
	err = client.Request(preview, "POST", "/v1/explain", spec, &decision)
	cancel()
	if err == nil && decision.Split == 2 && decision.Target != "LOCAL" && decision.Target != "REJECT" && decision.Target != "WAIT" {
		return split(ctx, client, spec, decision, local)
	}
	if err == nil && decision.Target != "LOCAL" && decision.Target != "REJECT" && decision.Target != "WAIT" {
		var summary *tail
		if spec.Shard != "" {
			summary = &tail{}
		}
		code, finished, remoteErr := remote(ctx, client, spec, decision, summary)
		if summary != nil && remoteErr == nil && ctx.Err() == nil && !finished.Evacuated {
			if files, ok := adapter.SuiteFiles(summary.String(), spec.Shard); ok {
				reportSuite(client, spec, files, 0)
			}
		}
		if spec.Service && ctx.Err() == nil {
			return failover(ctx, local, spec.Ports)
		}
		if finished.WriteBack != nil && finished.WriteBack.Conflict != "" && ctx.Err() == nil {
			// The laptop's copy changed while the phone worked; nothing was
			// applied, so the laptop runs the command itself.
			fmt.Fprintln(os.Stderr, "[Tidal Bridge] Running the command on this laptop instead.")
			return passthrough(ctx, local, nil)
		}
		if finished.Evacuated && ctx.Err() == nil {
			// The phone stopped it at a critical temperature; the run there
			// has ended, so the laptop runs the command instead.
			fmt.Fprintln(os.Stderr, "[Tidal Bridge] The phone overheated; running this command on the laptop.")
			return localSuite(ctx, client, spec, local, nil)
		}
		if remoteErr != nil || code == 0 || !(decision.Confirm || finished.Recheck != "") || ctx.Err() != nil {
			return code, remoteErr
		}
		if finished.Recheck == "outside" {
			// Nothing to learn about the phone: it has the file from now on.
			fmt.Fprintln(os.Stderr, "[Tidal Bridge] The phone's copy lacked a file from outside the project folder; it has it from now on. Running this on the laptop.")
			return passthrough(ctx, local, &observed{client: client, spec: spec, decision: &decision})
		}
		// A command's phone failure is trusted only after the laptop has
		// agreed with the phone; until then the laptop's result stands.
		if finished.Recheck == "timeout" {
			fmt.Fprintln(os.Stderr, "[Tidal Bridge] A test ran out of time on the phone; checking the result on this laptop.")
		} else {
			fmt.Fprintln(os.Stderr, "[Tidal Bridge] The phone run failed; checking the result on this laptop.")
		}
		localCode, localErr := passthrough(ctx, local, &observed{client: client, spec: spec, decision: &decision})
		if localErr == nil {
			reportCtx, cancelReport := context.WithTimeout(context.Background(), 2*time.Second)
			client.Request(reportCtx, "POST", "/v1/observations/confirmation", protocol.Confirmation{Spec: spec, RemoteExitCode: code, LocalExitCode: localCode}, nil)
			cancelReport()
			if localCode != 0 {
				fmt.Fprintln(os.Stderr, "[Tidal Bridge] Confirmed on this laptop: it fails the same way, so the failure is real, not caused by the phone.")
			} else {
				fmt.Fprintln(os.Stderr, "[Tidal Bridge] This laptop passes where the phone failed; its result stands, and this command stays on this laptop for a week.")
			}
		}
		return localCode, localErr
	}
	if forceRemote {
		return 1, fmt.Errorf("required remote route unavailable: %s (%v)", decision.Explanation, err)
	}
	var previewDecision *protocol.Decision
	if err == nil {
		previewDecision = &decision
	}
	return localSuite(ctx, client, spec, local, &observed{client: client, spec: spec, decision: previewDecision})
}

func remote(ctx context.Context, client cli.Client, spec protocol.JobSpec, decision protocol.Decision, summary *tail) (int, protocol.Job, error) {
	own, _ := process.NewProcess(int32(os.Getpid()))
	before := 0.0
	if own != nil {
		if times, e := own.Times(); e == nil {
			before = times.User + times.System
		}
	}
	var peak float64
	done := make(chan struct{})
	sampled := make(chan struct{})
	go func() {
		defer close(sampled)
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		for {
			if own != nil {
				if memory, e := own.MemoryInfo(); e == nil {
					peak = max(peak, float64(memory.RSS)/(1<<20))
				}
			}
			select {
			case <-done:
				return
			case <-ticker.C:
			}
		}
	}()
	fmt.Fprintf(os.Stderr, "[Tidal Bridge] %s -> %s\n", strings.Join(spec.Argv, " "), decision.Target)
	var accepted protocol.Job
	out, errOut := io.Writer(os.Stdout), io.Writer(os.Stderr)
	if summary != nil {
		out, errOut = io.MultiWriter(os.Stdout, summary), io.MultiWriter(os.Stderr, summary)
	}
	code, executeErr := client.ExecuteObserved(ctx, spec, out, errOut, func(j protocol.Job) { accepted = j })
	close(done)
	<-sampled
	cpuSeconds := 0.0
	if own != nil {
		if times, e := own.Times(); e == nil {
			cpuSeconds = max(0, times.User+times.System-before)
		}
	}
	if accepted.ID != "" {
		reportCtx, cancelReport := context.WithTimeout(context.Background(), 2*time.Second)
		client.Request(reportCtx, "POST", "/v1/observations/adapter", protocol.AdapterObservation{JobID: accepted.ID, Metrics: protocol.AdapterMetrics{CPUSeconds: cpuSeconds, PeakRAMMB: peak, Scope: "Command adapter process only; daemon, ADB and dashboard excluded."}}, nil)
		cancelReport()
	}
	return code, accepted, executeErr
}

// failover restarts a service on the laptop when its phone run ended for any
// reason other than the user stopping it, because the phone is optional. A
// dev server has no other effects, and the host stops the phone copy once
// its client has gone.
func failover(ctx context.Context, local []string, ports []int) (int, error) {
	fmt.Fprintln(os.Stderr, "[Tidal Bridge] The service stopped on the phone; starting it on this laptop.")
	for deadline := time.Now().Add(15 * time.Second); !portsFree(ports) && time.Now().Before(deadline) && ctx.Err() == nil; {
		time.Sleep(500 * time.Millisecond)
	}
	return passthrough(ctx, local, nil)
}

func portsFree(ports []int) bool {
	for _, port := range ports {
		if conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 300*time.Millisecond); err == nil {
			conn.Close()
			return false
		}
	}
	return true
}

type observed struct {
	client   cli.Client
	spec     protocol.JobSpec
	decision *protocol.Decision
}

// passthrough runs the original local command with inherited stdio. For an
// approved command it reports the measured completion so the scheduler can
// compare local and remote history.
func passthrough(ctx context.Context, local []string, report *observed) (int, error) {
	return runLocal(ctx, local, report, os.Stdin, os.Stdout, os.Stderr)
}

// runLocal is passthrough with the given streams.
func runLocal(ctx context.Context, local []string, report *observed, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	started := time.Now()
	cmd := exec.CommandContext(ctx, local[0], local[1:]...)
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.Env = append(os.Environ(), "TIDALBRIDGE_INTERNAL=1")
	prepare(cmd)
	if err := cmd.Start(); err != nil {
		return 1, err
	}
	var tree *processTree
	if report != nil {
		tree = trackTree(cmd.Process.Pid)
		defer tree.close()
	}
	var peak float64
	done := make(chan struct{})
	measured := make(chan struct{})
	if report != nil {
		go func() {
			defer close(measured)
			p, _ := process.NewProcess(int32(cmd.Process.Pid))
			ticker := time.NewTicker(250 * time.Millisecond)
			defer ticker.Stop()
			for {
				if p != nil {
					if memory, e := p.MemoryInfo(); e == nil {
						peak = max(peak, float64(memory.RSS)/(1<<20))
					}
				}
				select {
				case <-done:
					return
				case <-ticker.C:
				}
			}
		}()
	} else {
		close(measured)
	}
	err := cmd.Wait()
	close(done)
	<-measured
	if ctx.Err() != nil {
		return 130, ctx.Err()
	}
	code := 0
	if err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			code = exit.ExitCode()
		} else {
			return 1, err
		}
	}
	if report != nil {
		// Completion-only observation: the process actually ran here. No fake
		// queued/active job is created, and no stdout or private env is uploaded.
		observation := protocol.LocalObservation{Spec: report.spec, Decision: report.decision, Started: started, DurationMS: float64(time.Since(started).Microseconds()) / 1000, ExitCode: code}
		if cmd.ProcessState != nil {
			observation.CPUSeconds = cmd.ProcessState.UserTime().Seconds() + cmd.ProcessState.SystemTime().Seconds()
		}
		// The whole process tree when available: the root alone misses the
		// compiler or test runner a package script starts.
		if cpu, treePeak, ok := tree.usage(); ok {
			observation.CPUSeconds = max(observation.CPUSeconds, cpu)
			peak = max(peak, treePeak)
		}
		if peak > 0 {
			observation.PeakRAMMB = &peak
		}
		reportCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		report.client.Request(reportCtx, "POST", "/v1/observations/local", observation, nil)
		cancel()
	}
	return code, nil
}
