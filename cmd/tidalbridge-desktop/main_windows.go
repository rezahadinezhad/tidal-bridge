//go:build windows

// The desktop entry point is a short-lived Windows GUI launcher.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
	"unsafe"

	"tidalbridge/apps/cli"
	"tidalbridge/packages/config"
	"tidalbridge/packages/protocol"
)

func main() {
	dir := flag.String("data-dir", config.DataDir(), "Host configuration directory")
	check := flag.Bool("check", false, "Check the installed launcher without opening a window")
	flag.Parse()
	if err := launch(*dir, *check); err != nil {
		if *check {
			fmt.Fprintln(os.Stderr, err)
		} else {
			message, _ := syscall.UTF16PtrFromString("Tidal Bridge could not open.\n\n" + err.Error())
			title, _ := syscall.UTF16PtrFromString("Tidal Bridge")
			syscall.NewLazyDLL("user32.dll").NewProc("MessageBoxW").Call(0, uintptr(unsafe.Pointer(message)), uintptr(unsafe.Pointer(title)), 0x10)
		}
		os.Exit(1)
	}
}

func edgePath() string {
	for _, root := range []string{os.Getenv("ProgramFiles(x86)"), os.Getenv("ProgramFiles"), os.Getenv("LOCALAPPDATA")} {
		path := filepath.Join(root, "Microsoft", "Edge", "Application", "msedge.exe")
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return path
		}
	}
	return ""
}

func ready(client cli.Client) error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var status struct {
		Product  string `json:"product"`
		Protocol int    `json:"protocol_version"`
	}
	if err := client.Request(ctx, "GET", "/v1/health", nil, &status); err != nil {
		return err
	}
	if status.Product != "Tidal Bridge" || status.Protocol != protocol.Version {
		return fmt.Errorf("the listener did not identify a compatible Tidal Bridge host")
	}
	return nil
}

func endpointFor(dir string) string {
	c, err := config.Load(dir)
	if err != nil {
		return "http://127.0.0.1:47831"
	}
	return "http://" + c.Bind
}

func listenerPresent(endpoint string) bool {
	address, err := url.Parse(endpoint)
	if err != nil {
		return false
	}
	connection, err := net.DialTimeout("tcp", address.Host, time.Second)
	if err != nil {
		return false
	}
	connection.Close()
	return true
}

func waitReady(client *cli.Client, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		// Startup may overlap configuration or credential publication. Reload
		// from this same data directory instead of retrying a stale snapshot.
		var err error
		if client.Dir != "" {
			var current cli.Client
			current, err = cli.New(client.Dir)
			if err == nil {
				*client = current
			}
		}
		if err == nil {
			err = ready(*client)
		}
		if err == nil {
			return nil
		} else if time.Now().After(deadline) {
			return fmt.Errorf("host readiness failed: %w; see %s", err, filepath.Join(client.Dir, "service.stderr.log"))
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func launch(dir string, check bool) error {
	// Prefer the registered background service: Task Scheduler starts it
	// outside any app container. Without a registration, start the windowless
	// service binary directly.
	client, err := cli.Connect(context.Background(), dir, 20*time.Second)
	if err != nil {
		if !listenerPresent(endpointFor(dir)) {
			executable, err := os.Executable()
			if err != nil {
				return err
			}
			command := exec.Command(filepath.Join(filepath.Dir(executable), "tidalbridge-service.exe"), "--data-dir", dir)
			command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000 | 0x00000008}
			if err := command.Start(); err != nil {
				return err
			}
			command.Process.Release()
		}
		client = cli.Client{Dir: dir}
		if err := waitReady(&client, 30*time.Second); err != nil {
			return err
		}
	}
	browser := edgePath()
	if browser == "" {
		return fmt.Errorf("Microsoft Edge is required for the desktop window; use tidalbridge dashboard with another browser")
	}
	if check {
		return json.NewEncoder(os.Stdout).Encode(map[string]any{"host_ready": true, "app_window_available": true, "browser": "Microsoft Edge"})
	}
	command := exec.Command(browser, "--app="+client.Endpoint+"/#token="+client.Token, "--user-data-dir="+filepath.Join(dir, "desktop-profile"), "--no-first-run", "--no-default-browser-check", "--disable-background-mode")
	if err := command.Start(); err != nil {
		return err
	}
	return command.Process.Release()
}
