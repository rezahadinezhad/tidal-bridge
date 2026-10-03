package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

var Client = &http.Client{Timeout: 30 * time.Second}

func Request(ctx context.Context, endpoint, token, method, path string, input, output any) error {
	var body io.Reader
	if input != nil {
		b, e := json.Marshal(input)
		if e != nil {
			return e
		}
		body = bytes.NewReader(b)
	}
	req, e := http.NewRequestWithContext(ctx, method, endpoint+path, body)
	if e != nil {
		return e
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, e := Client.Do(req)
	if e != nil {
		return e
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, b)
	}
	if output != nil {
		return json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(output)
	}
	_, e = io.Copy(io.Discard, resp.Body)
	return e
}
func Upload(ctx context.Context, endpoint, token, hash, path string, size int64) error {
	f, e := os.Open(path)
	if e != nil {
		return e
	}
	defer f.Close()
	req, e := http.NewRequestWithContext(ctx, "PUT", endpoint+"/v1/blobs/"+hash, f)
	if e != nil {
		return e
	}
	req.ContentLength = size
	if size == 0 {
		// A zero ContentLength with a body means "unknown" (chunked, no
		// Content-Length header); an empty file must say 0.
		req.Body = http.NoBody
	}
	req.Header.Set("Authorization", "Bearer "+token)
	client := http.Client{Timeout: 15 * time.Minute}
	resp, e := client.Do(req)
	if e != nil {
		return e
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("blob upload: %s", b)
	}
	return nil
}
func Download(ctx context.Context, endpoint, token, path, destination string) error {
	req, e := http.NewRequestWithContext(ctx, "GET", endpoint+path, nil)
	if e != nil {
		return e
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, e := Client.Do(req)
	if e != nil {
		return e
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("artifact HTTP %d", resp.StatusCode)
	}
	if e = os.MkdirAll(filepath.Dir(destination), 0700); e != nil {
		return e
	}
	f, e := os.Create(destination + ".tmp")
	if e != nil {
		return e
	}
	_, e = io.Copy(f, io.LimitReader(resp.Body, 2<<30))
	closeErr := f.Close()
	if e != nil {
		return e
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(destination+".tmp", destination)
}
func FindADB(configured string) string {
	if configured != "" {
		return configured
	}
	if v := os.Getenv("TIDALBRIDGE_ADB"); v != "" {
		return v
	}
	if path, e := exec.LookPath("adb"); e == nil {
		return path
	}
	home, _ := os.UserHomeDir()
	// The installer downloads platform tools into the data directory when no
	// Android SDK is present.
	for _, root := range []string{os.Getenv("ANDROID_HOME"), os.Getenv("ANDROID_SDK_ROOT"), filepath.Join(os.Getenv("LOCALAPPDATA"), "Android", "Sdk"), filepath.Join(home, ".tidalbridge")} {
		if root != "" {
			for _, name := range []string{"adb.exe", "adb"} {
				p := filepath.Join(root, "platform-tools", name)
				if _, e := os.Stat(p); e == nil {
					return p
				}
			}
		}
	}
	return ""
}
func ADB(ctx context.Context, adb, serial string, args ...string) (string, error) {
	if adb == "" {
		return "", fmt.Errorf("ADB not installed")
	}
	if serial != "" {
		args = append([]string{"-s", serial}, args...)
	}
	cctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, adb, args...)
	HideConsole(cmd)
	b, e := cmd.CombinedOutput()
	if e != nil {
		return "", fmt.Errorf("ADB: %v: %s", e, strings.TrimSpace(string(b)))
	}
	return strings.TrimSpace(string(b)), nil
}

type ADBDevice struct {
	Serial   string `json:"serial"`
	State    string `json:"state"`
	Metadata string `json:"metadata"`
}

func ParseDevices(output string) []ADBDevice {
	result := []ADBDevice{}
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[0] == "List" || strings.HasPrefix(fields[0], "*") {
			continue
		}
		result = append(result, ADBDevice{Serial: fields[0], State: fields[1], Metadata: strings.Join(fields[2:], " ")})
	}
	return result
}

func ExistingForward(output, serial string, workerPort int) string {
	remote := fmt.Sprintf("tcp:%d", workerPort)
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 3 && fields[0] == serial && fields[2] == remote && strings.HasPrefix(fields[1], "tcp:") {
			return strings.TrimPrefix(fields[1], "tcp:")
		}
	}
	return ""
}

func EnsureForward(ctx context.Context, adb, serial string, workerPort int) (string, error) {
	list, _ := ADB(ctx, adb, serial, "forward", "--list")
	if existing := ExistingForward(list, serial, workerPort); existing != "" {
		return existing, nil
	}
	return ADB(ctx, adb, serial, "forward", "tcp:0", fmt.Sprintf("tcp:%d", workerPort))
}

// TrackDevices calls onChange whenever the ADB server reports a change in
// the device list (attach, detach, authorization), with no polling. It
// restarts after the ADB server restarts and returns when ctx ends.
func TrackDevices(ctx context.Context, adb string, onChange func()) {
	for ctx.Err() == nil {
		cmd := exec.CommandContext(ctx, adb, "track-devices")
		HideConsole(cmd)
		if out, err := cmd.StdoutPipe(); err == nil && cmd.Start() == nil {
			buffer := make([]byte, 4096)
			for {
				n, readErr := out.Read(buffer)
				if n > 0 {
					onChange()
				}
				if readErr != nil {
					break
				}
			}
			cmd.Wait()
			onChange() // the server went away: re-check state
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
		}
	}
}
