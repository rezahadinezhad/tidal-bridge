package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"tidalbridge/apps/cli"
	"tidalbridge/apps/dashboard"
	host "tidalbridge/apps/host-daemon"
	mcpserver "tidalbridge/apps/mcp-server"
	"tidalbridge/packages/config"
	"tidalbridge/packages/protocol"
	"tidalbridge/packages/service"
	adapter "tidalbridge/packages/task-adapter"
	"tidalbridge/packages/transport"
	workspacesync "tidalbridge/packages/workspace-sync"
)

func printJSON(v any) { b, _ := json.MarshalIndent(v, "", "  "); fmt.Println(string(b)) }
func main() {
	code, e := run()
	if e != nil {
		fmt.Fprintln(os.Stderr, "tidalbridge:", e)
		if code == 0 {
			code = 1
		}
	}
	os.Exit(code)
}
func run() (int, error) {
	global := flag.NewFlagSet("tidalbridge", flag.ContinueOnError)
	dir := global.String("data-dir", config.DataDir(), "Configuration/state directory")
	if e := global.Parse(os.Args[1:]); e != nil {
		return 1, e
	}
	args := global.Args()
	if len(args) == 0 {
		help()
		return 0, nil
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if args[0] == "serve" {
		e := host.Serve(ctx, *dir, dashboard.Handler(), os.Stdout)
		if errors.Is(e, host.ErrAlreadyRunning) {
			fmt.Println("Tidal Bridge host is already running for", *dir)
			return 0, nil
		}
		return 0, e
	}
	if args[0] == "service" {
		return serviceCommand(ctx, *dir, args[1:])
	}
	if args[0] == "doctor" {
		return doctor(*dir), nil
	}
	if args[0] == "approve" {
		return approve(*dir, args[1:])
	}
	if args[0] == "setup" && len(args) > 1 && args[1] == "agents" {
		exe, _ := os.Executable()
		fmt.Printf("codex mcp add tidalbridge -- \"%s\" --data-dir \"%s\" mcp\n", exe, *dir)
		fmt.Printf("claude mcp add --transport stdio --scope user tidalbridge -- \"%s\" --data-dir \"%s\" mcp\n", exe, *dir)
		return 0, nil
	}
	if args[0] == "mcp" {
		// The MCP server must start even when the host is down; each tool call
		// connects (and starts the background service) on demand.
		return 0, mcpserver.Run(ctx, &cli.Lazy{Dir: *dir, Wait: 8 * time.Second})
	}
	if args[0] == "hook" {
		return hookCommand(*dir, args[1:])
	}
	if args[0] == "cleanup" {
		return cleanupCommand(args[1:])
	}
	if args[0] == "automation" && len(args) > 1 && settingsCommands[args[1]] {
		return automationSettings(*dir, args[1:])
	}
	if args[0] == "scan" {
		// Diagnostic: what a worker copy of this directory would contain.
		if len(args) < 2 {
			return 1, fmt.Errorf("scan requires a directory")
		}
		started := time.Now()
		m, e := workspacesync.ScanWith(ctx, args[1], workspacesync.Options{CacheDir: filepath.Join(*dir, "cache", "hashes")})
		if e != nil {
			return 1, e
		}
		largest := append([]protocol.FileEntry(nil), m.Files...)
		sort.Slice(largest, func(i, j int) bool { return largest[i].Size > largest[j].Size })
		if len(largest) > 5 {
			largest = largest[:5]
		}
		printJSON(map[string]any{"files": len(m.Files), "bytes": m.TotalBytes, "duration_ms": time.Since(started).Milliseconds(), "largest": largest})
		return 0, nil
	}
	client, e := cli.Connect(ctx, *dir, 10*time.Second)
	if e != nil {
		return 1, e
	}
	switch args[0] {
	case "automation":
		if len(args) < 2 || args[1] == "status" {
			var value any
			e := client.Request(ctx, "GET", "/v1/automation", nil, &value)
			if e == nil {
				printJSON(value)
			}
			return 0, e
		}
		if args[1] != "enable" && args[1] != "disable" {
			return 1, fmt.Errorf("automation requires status, enable or disable")
		}
		f := flag.NewFlagSet("automation", flag.ContinueOnError)
		workspace := f.String("workspace", "", "Absolute project directory")
		profiles := f.String("profiles", "", "Comma-separated task profiles")
		provision := f.Bool("provision", false, "Approve locked project dependency installation")
		if e := f.Parse(args[2:]); e != nil {
			return 1, e
		}
		input := host.AutomationInput{Workspace: *workspace, Enabled: args[1] == "enable", Provision: *provision}
		if *profiles != "" {
			input.Profiles = strings.Split(*profiles, ",")
		}
		var value any
		e := client.Request(ctx, "POST", "/v1/automation", input, &value)
		if e == nil {
			printJSON(value)
		}
		return 0, e
	case "dashboard":
		fmt.Println(client.Endpoint + "/#token=" + client.Token)
		return 0, nil
	case "status", "devices", "jobs":
		var v map[string]any
		e := client.Request(ctx, "GET", "/v1/status", nil, &v)
		if e != nil {
			return 1, e
		}
		if args[0] == "devices" {
			printJSON(v["workers"])
		} else if args[0] == "jobs" {
			printJSON(v["jobs"])
		} else {
			printJSON(v)
		}
		return 0, nil
	case "device":
		if len(args) < 2 {
			return 1, fmt.Errorf("device requires a stable ID")
		}
		var v struct {
			Workers []protocol.WorkerNode `json:"workers"`
		}
		if e := client.Request(ctx, "GET", "/v1/status", nil, &v); e != nil {
			return 1, e
		}
		for _, n := range v.Workers {
			if n.ID == args[1] {
				printJSON(n)
				return 0, nil
			}
		}
		return 1, fmt.Errorf("device not found")
	case "job", "explain":
		if len(args) < 2 {
			return 1, fmt.Errorf("job ID required")
		}
		var j protocol.Job
		e := client.Request(ctx, "GET", "/v1/jobs/"+args[1], nil, &j)
		if e == nil {
			if args[0] == "explain" {
				printJSON(j.Decision)
			} else {
				printJSON(j)
			}
		}
		return 0, e
	case "cancel":
		if len(args) < 2 {
			return 1, fmt.Errorf("job ID required")
		}
		return 0, client.Request(ctx, "POST", "/v1/jobs/"+args[1]+"/cancel", map[string]any{}, nil)
	case "mode":
		if len(args) < 2 {
			return 1, fmt.Errorf("mode requires AUTO, CONSERVATIVE, PERFORMANCE or BATTERY_SAVER")
		}
		return 0, client.Request(ctx, "POST", "/v1/settings", map[string]string{"mode": strings.ToUpper(args[1])}, nil)
	case "pause", "resume":
		return 0, client.Request(ctx, "POST", "/v1/settings", map[string]bool{"paused": args[0] == "pause"}, nil)
	case "drain", "enable":
		if len(args) < 2 {
			return 1, fmt.Errorf("device ID required")
		}
		return 0, client.Request(ctx, "POST", "/v1/devices/"+args[1]+"/drain", map[string]bool{"draining": args[0] == "drain"}, nil)
	case "refresh":
		return 0, client.Request(ctx, "POST", "/v1/refresh", map[string]any{}, nil)
	case "run", "route":
		return runJob(ctx, client, args[0], args[1:])
	case "render-matrix":
		return renderMatrix(ctx, client, args[1:])
	case "sync":
		if len(args) < 3 {
			return 1, fmt.Errorf("sync WORKSPACE DEVICE_ID")
		}
		var value any
		e := client.Request(ctx, "POST", "/v1/sync", map[string]string{"workspace": args[1], "device_id": args[2]}, &value)
		if e == nil {
			printJSON(value)
		}
		return 0, e
	case "benchmark":
		device := ""
		if len(args) > 2 && args[1] == "--device" {
			device = args[2]
		}
		var value any
		spec := protocol.JobSpec{Argv: []string{"python", "-c", "print(sum(i*i for i in range(1000000)))"}, TimeoutSeconds: 30, Policy: protocol.Policy{ForceLocal: device == "", ForceRemote: device != "", DeviceID: device, Idempotent: true, Retryable: true}}
		e := client.Request(ctx, "POST", "/v1/jobs", spec, &value)
		if e == nil {
			printJSON(value)
		}
		return 0, e
	default:
		help()
		return 1, fmt.Errorf("unknown command %q", args[0])
	}
}
func renderMatrix(ctx context.Context, c cli.Client, args []string) (int, error) {
	f := flag.NewFlagSet("render-matrix", flag.ContinueOnError)
	url := f.String("url", "", "Host loopback HTTP development URL")
	device := f.String("device", "", "Optional stable device ID")
	matrix := f.String("matrix", "", "JSON viewport array file")
	if e := f.Parse(args); e != nil {
		return 1, e
	}
	viewports := []protocol.Viewport{{Name: "phone-small", Width: 360, Height: 800, DPR: 1}, {Name: "phone", Width: 390, Height: 844, DPR: 1}, {Name: "phone-large", Width: 430, Height: 932, DPR: 1}, {Name: "tablet-portrait", Width: 768, Height: 1024, DPR: 1}, {Name: "tablet-landscape", Width: 1024, Height: 768, DPR: 1}, {Name: "laptop-13", Width: 1280, Height: 800, DPR: 1}, {Name: "laptop-14", Width: 1440, Height: 900, DPR: 1}, {Name: "laptop-15", Width: 1536, Height: 864, DPR: 1}, {Name: "laptop-16", Width: 1728, Height: 1117, DPR: 1}, {Name: "desktop-fhd", Width: 1920, Height: 1080, DPR: 1}}
	if *matrix != "" {
		b, e := os.ReadFile(*matrix)
		if e != nil {
			return 1, e
		}
		if e = json.Unmarshal(b, &viewports); e != nil {
			return 1, e
		}
	}
	if len(viewports) < 1 || len(viewports) > 16 {
		return 1, fmt.Errorf("matrix must contain 1–16 viewports")
	}
	jobs := []protocol.Job{}
	for _, v := range viewports {
		spec := protocol.JobSpec{Render: &protocol.RenderSpec{URL: *url, Viewport: v}, TimeoutSeconds: 90, EstimatedDurationMS: 10000, Policy: protocol.Policy{ForceRemote: true, DeviceID: *device}}
		var j protocol.Job
		if e := c.Request(ctx, "POST", "/v1/jobs", spec, &j); e != nil {
			return 1, e
		}
		jobs = append(jobs, j)
	}
	printJSON(jobs)
	return 0, nil
}
func help() {
	fmt.Println(`Tidal Bridge — adaptive local compute pool

tidalbridge [--data-dir PATH] serve | doctor | status | devices | jobs
tidalbridge run [--workspace PATH] [--remote | --local] [--idempotent] -- COMMAND ARGS
tidalbridge route [run options] -- COMMAND ARGS
tidalbridge job ID | explain ID | cancel ID
tidalbridge mode AUTO | CONSERVATIVE | PERFORMANCE | BATTERY_SAVER
tidalbridge pause | resume | drain DEVICE | enable DEVICE
tidalbridge approve --serial SERIAL --token-file PATH [--port 47832]
tidalbridge refresh | dashboard | mcp | setup agents
tidalbridge service install | uninstall | start | stop | status`)
}
func runJob(ctx context.Context, c cli.Client, command string, args []string) (int, error) {
	f := flag.NewFlagSet(command, flag.ContinueOnError)
	workspace := f.String("workspace", "", "Host project directory")
	remote := f.Bool("remote", false, "Require remote (still checks compatibility/safety)")
	local := f.Bool("local", false, "Require local")
	idempotent := f.Bool("idempotent", false, "Allow safe replay after transport loss")
	timeout := f.Int("timeout", 600, "End-to-end seconds")
	estimate := f.Float64("estimate-ms", 1000, "Estimated local duration")
	device := f.String("device", "", "Worker stable ID")
	provision := f.Bool("provision", false, "Approve network dependency provisioning")
	output := f.String("output", "", "Declared output file")
	runtime := f.String("runtime", "", "Worker userland: termux (default) or debian")
	service := f.Bool("service", false, "Long-running service (dev server): runs until interrupted")
	ports := f.String("port", "", "Comma-separated service ports to serve on this laptop")
	reverse := f.String("reverse", "", "Comma-separated laptop ports the job reaches on 127.0.0.1")
	syncEnv := f.Bool("sync-env", false, "Include the project's .env files in the worker copy")
	scripts := f.Bool("provision-scripts", false, "Allow dependency install scripts during provisioning")
	if e := f.Parse(args); e != nil {
		return 1, e
	}
	parsePorts := func(text string) ([]int, error) {
		var out []int
		for _, part := range strings.Split(text, ",") {
			if part = strings.TrimSpace(part); part == "" {
				continue
			}
			port, err := strconv.Atoi(part)
			if err != nil || port < 1 || port > 65535 {
				return nil, fmt.Errorf("invalid port %q", part)
			}
			out = append(out, port)
		}
		return out, nil
	}
	servicePorts, e := parsePorts(*ports)
	if e != nil {
		return 1, e
	}
	reversePorts, e := parsePorts(*reverse)
	if e != nil {
		return 1, e
	}
	argv := f.Args()
	if len(argv) == 0 {
		return 1, fmt.Errorf("command array required after --")
	}
	if *workspace == "" {
		*workspace, _ = os.Getwd()
	}
	spec := protocol.JobSpec{Argv: argv, Workspace: *workspace, TimeoutSeconds: *timeout, EstimatedDurationMS: *estimate, Policy: protocol.Policy{LocalFallback: true, Idempotent: *idempotent, Retryable: *idempotent, ForceLocal: *local || os.Getenv("TIDALBRIDGE_FORCE_LOCAL") == "1" || os.Getenv("TIDALBRIDGE_DISABLE") == "1", ForceRemote: *remote || os.Getenv("TIDALBRIDGE_FORCE_REMOTE") == "1", DeviceID: *device, Provision: *provision}}
	if *output != "" {
		spec.ExpectedOutputs = []string{*output}
	}
	spec.Runtime = *runtime
	spec.Service = *service
	spec.Ports = servicePorts
	spec.ReversePorts = reversePorts
	spec.Policy.SyncEnvFiles = *syncEnv
	spec.Policy.ProvisionScripts = *scripts
	if spec.Service {
		spec.AttachedClient = true
		spec.Policy.Idempotent, spec.Policy.Retryable = false, false
		if *timeout == 600 {
			spec.TimeoutSeconds = 86400
		}
	}
	if command == "route" {
		var v any
		e := c.Request(ctx, "POST", "/v1/explain", spec, &v)
		if e == nil {
			printJSON(v)
		}
		return 0, e
	}
	return c.Execute(ctx, spec, os.Stdout, os.Stderr)
}
func doctor(dir string) int {
	c, e := config.Load(dir)
	result := map[string]any{"product": "Tidal Bridge", "config_directory": dir, "platform": "Windows/Android distributed execution; no shared RAM", "config_valid": e == nil}
	paths := map[string]string{}
	for _, name := range []string{"git", "node", "python", "go", "rustc", "codex", "claude", "wsl"} {
		path, _ := exec.LookPath(name)
		paths[name] = path
	}
	adb := transport.FindADB(c.ADB)
	paths["adb"] = adb
	result["toolchains"] = paths
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	devices, err := transport.ADB(ctx, adb, "", "devices", "-l")
	result["adb_devices"] = transport.ParseDevices(devices)
	if err != nil {
		result["adb_error"] = err.Error()
	}
	client, clientErr := cli.New(dir)
	var status struct {
		Workers []protocol.WorkerNode `json:"workers"`
	}
	connected := clientErr == nil && client.Request(ctx, "GET", "/v1/status", nil, &status) == nil
	result["daemon_connected"] = connected
	serviceState, serviceErr := service.Status()
	if serviceErr != nil {
		serviceState = serviceErr.Error()
	}
	result["service"] = serviceState
	result["approved_count"] = len(c.Approved)
	result["findings"] = findings(dir, adb, transport.ParseDevices(devices), connected, status.Workers)
	printJSON(result)
	if e != nil {
		return 1
	}
	return 0
}

// findings turns doctor's facts into problems with their fixes.
func findings(dir, adb string, devices []transport.ADBDevice, connected bool, workers []protocol.WorkerNode) []string {
	var found []string
	note := func(format string, args ...any) { found = append(found, fmt.Sprintf(format, args...)) }
	if adb == "" {
		note("adb was not found: install Android SDK platform-tools or set adb in config.yaml.")
	}
	for _, d := range devices {
		if d.State == "unauthorized" {
			note("Phone %s is waiting for you to allow USB debugging on its screen.", d.Serial)
		}
	}
	if adb != "" && len(devices) == 0 {
		note("No phone is connected: plug in the cable and enable USB debugging. Commands run on this laptop meanwhile.")
	}
	if !connected {
		note("The background service is not answering: run `tidalbridge service start`.")
	}
	for _, w := range workers {
		r := w.Profile.Capabilities.Resources
		switch {
		case w.State != "READY" && w.State != "BUSY":
			note("%s is %s; commands run on this laptop until it recovers.", w.ID, w.State)
		case r.Thermal == "severe" || r.Thermal == "critical" || r.Thermal == "emergency":
			note("%s is too hot (%s); it takes no work until it cools down.", w.ID, r.Thermal)
		case w.CoolingUntil != nil && time.Now().Before(*w.CoolingUntil):
			note("%s is cooling down after overheating; it takes work again at %s.", w.ID, w.CoolingUntil.Format("15:04"))
		case r.BatteryPercent != nil && *r.BatteryPercent < 20 && (r.Charging == nil || !*r.Charging):
			note("%s battery is at %d%% and not charging; work stops below 15%%.", w.ID, *r.BatteryPercent)
		}
		if r.InUse != nil && *r.InUse {
			note("%s is in use (screen on), so it runs one job at a time; set phone_use: max in config.yaml to change this.", w.ID)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "shims", "shims.json")); err != nil {
		note("Command adapters are not installed: run scripts/install-automation.ps1.")
	}
	if !adapter.LoadSettings(dir).Universal {
		note("Universal mode is off: only projects with a task file are offloaded (`tidalbridge automation universal on`).")
	}
	var verified struct {
		Quarantine map[string]struct {
			Until time.Time `json:"until"`
		} `json:"quarantine"`
		Agreements map[string]int `json:"agreements"`
	}
	if adapter.ReadJSON(filepath.Join(dir, "verification.json"), &verified) == nil {
		active := 0
		for _, q := range verified.Quarantine {
			if time.Now().Before(q.Until) {
				active++
			}
		}
		if active > 0 {
			note("%d detected command(s) stay local for now because their phone result differed from this laptop's.", active)
		}
	}
	if trees, err := findDevTrees(); err == nil {
		count, memory := 0, 0.0
		for _, t := range trees {
			if t.Forgotten {
				count++
				memory += t.MemoryMB
			}
		}
		if count > 0 {
			note("%d forgotten dev server(s) or watcher(s) use %.0f MB: see `tidalbridge cleanup`.", count, memory)
		}
	}
	if len(found) == 0 {
		found = []string{"No problems found."}
	}
	return found
}

func approve(dir string, args []string) (int, error) {
	f := flag.NewFlagSet("approve", flag.ContinueOnError)
	serial := f.String("serial", "", "Authorized ADB serial")
	tokenFile := f.String("token-file", "", "File containing paired worker secret")
	port := f.Int("port", 47832, "Worker port")
	launcher := f.String("launcher", "", "How the host starts the worker: termux (default) or adb-shell")
	if e := f.Parse(args); e != nil {
		return 1, e
	}
	if *launcher != "" && *launcher != "termux" && *launcher != "adb-shell" {
		return 1, fmt.Errorf("launcher must be termux or adb-shell")
	}
	if *serial == "" || *tokenFile == "" {
		return 1, fmt.Errorf("--serial and --token-file are required")
	}
	b, e := os.ReadFile(*tokenFile)
	if e != nil {
		return 1, e
	}
	c, e := config.Load(dir)
	if e != nil {
		return 1, e
	}
	d := config.Device{Serial: *serial, Token: strings.TrimSpace(string(b)), Port: *port, Launcher: *launcher}
	found := false
	for i, v := range c.Approved {
		if v.Serial == d.Serial {
			c.Approved[i] = d
			found = true
		}
	}
	if !found {
		c.Approved = append(c.Approved, d)
	}
	if e = config.Save(dir, c); e != nil {
		return 1, e
	}
	fmt.Println("Approved", *serial, ". Restart the daemon to reload the device allow-list.")
	return 0, nil
}

func serviceCommand(ctx context.Context, dir string, args []string) (int, error) {
	if len(args) == 0 {
		return 1, fmt.Errorf("service requires install, uninstall, start, stop or status")
	}
	switch args[0] {
	case "install":
		exe, err := os.Executable()
		if err != nil {
			return 1, err
		}
		target := filepath.Join(filepath.Dir(exe), "tidalbridge-service.exe")
		if from, err := config.MigrateLegacy(dir); err != nil {
			return 1, err
		} else if from != "" {
			fmt.Println("Migrated pairing, profiles and history from", from)
		}
		if err := service.Install(target, dir); err != nil {
			return 1, err
		}
		fmt.Println("Registered", service.TaskName, "to start at logon:", target)
		if err := service.Start(); err != nil {
			return 1, err
		}
		if _, err := cli.Connect(ctx, dir, 15*time.Second); err != nil {
			return 1, fmt.Errorf("service registered but the host is not ready: %w", err)
		}
		fmt.Println("Host is running for", dir)
		return 0, nil
	case "uninstall":
		service.Stop()
		return 0, service.Uninstall()
	case "start":
		_, err := cli.Connect(ctx, dir, 15*time.Second)
		return 0, err
	case "stop":
		return 0, service.Stop()
	case "status":
		state, err := service.Status()
		if err != nil {
			return 1, err
		}
		client, clientErr := cli.New(dir)
		printJSON(map[string]any{"task": service.TaskName, "scheduler_state": state, "host_healthy": clientErr == nil && client.Healthy(ctx), "data_dir": dir})
		return 0, nil
	}
	return 1, fmt.Errorf("unknown service command %q", args[0])
}
