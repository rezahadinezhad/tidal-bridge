//go:build windows

// Package service registers the host as a per-user Task Scheduler task.
//
// Task Scheduler is used instead of the HKCU Run key or the Startup folder:
// both of those are virtualized for MSIX-packaged callers (Claude and Codex),
// so a registration made from an agent session silently never fires. The
// scheduler is a system service, needs no elevation for a task that runs as
// the current user, and starts the host outside any app container.
package service

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unicode/utf16"
)

const TaskName = "Tidal Bridge Host"

var ErrNotInstalled = errors.New("Tidal Bridge host service is not installed (run: tidalbridge service install)")

func schtasks(args ...string) (string, error) {
	cmd := exec.Command(filepath.Join(os.Getenv("SystemRoot"), "System32", "schtasks.exe"), args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func xmlEscape(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '&':
			b.WriteString("&amp;")
		case '<':
			b.WriteString("&lt;")
		case '>':
			b.WriteString("&gt;")
		case '"':
			b.WriteString("&quot;")
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// TaskXML is the task definition: start at this user's logon, restart after
// failures, no run-time limit, keep running on battery, normal priority. The
// service itself is light, but adapters wait only seconds for its routing
// answer: at below-normal priority a saturated laptop starved it, and work
// stayed on the laptop exactly when offloading mattered most.
func TaskXML(account, executable, dataDir string) string {
	return `<?xml version="1.0" encoding="UTF-16"?>
<Task version="1.4" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <RegistrationInfo><Description>Tidal Bridge Host Service: routes compatible developer jobs to approved Android workers.</Description></RegistrationInfo>
  <Triggers><LogonTrigger><Enabled>true</Enabled><UserId>` + xmlEscape(account) + `</UserId></LogonTrigger></Triggers>
  <Principals><Principal id="Author"><UserId>` + xmlEscape(account) + `</UserId><LogonType>InteractiveToken</LogonType><RunLevel>LeastPrivilege</RunLevel></Principal></Principals>
  <Settings>
    <MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>
    <DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>
    <StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>
    <AllowHardTerminate>true</AllowHardTerminate>
    <StartWhenAvailable>true</StartWhenAvailable>
    <RunOnlyIfNetworkAvailable>false</RunOnlyIfNetworkAvailable>
    <IdleSettings><StopOnIdleEnd>false</StopOnIdleEnd><RestartOnIdle>false</RestartOnIdle></IdleSettings>
    <AllowStartOnDemand>true</AllowStartOnDemand>
    <Enabled>true</Enabled>
    <Hidden>false</Hidden>
    <RunOnlyIfIdle>false</RunOnlyIfIdle>
    <WakeToRun>false</WakeToRun>
    <ExecutionTimeLimit>PT0S</ExecutionTimeLimit>
    <Priority>5</Priority>
    <RestartOnFailure><Interval>PT1M</Interval><Count>999</Count></RestartOnFailure>
  </Settings>
  <Actions Context="Author"><Exec><Command>` + xmlEscape(executable) + `</Command><Arguments>--data-dir "` + xmlEscape(dataDir) + `"</Arguments><WorkingDirectory>` + xmlEscape(filepath.Dir(executable)) + `</WorkingDirectory></Exec></Actions>
</Task>
`
}

func account() (string, error) {
	u, err := user.Current()
	if err != nil {
		return "", err
	}
	return u.Username, nil
}

// Install (re)registers the task. It does not start it.
func Install(executable, dataDir string) error {
	if _, err := os.Stat(executable); err != nil {
		return fmt.Errorf("service executable: %w", err)
	}
	name, err := account()
	if err != nil {
		return err
	}
	// schtasks expects UTF-16 task XML.
	units := utf16.Encode([]rune(TaskXML(name, executable, dataDir)))
	var buf bytes.Buffer
	buf.Write([]byte{0xFF, 0xFE})
	binary.Write(&buf, binary.LittleEndian, units)
	tmp, err := os.CreateTemp("", "tidalbridge-task-*.xml")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err = tmp.Write(buf.Bytes()); err != nil {
		tmp.Close()
		return err
	}
	tmp.Close()
	if out, err := schtasks("/Create", "/TN", TaskName, "/XML", tmp.Name(), "/F"); err != nil {
		return fmt.Errorf("register task: %v: %s", err, out)
	}
	return nil
}

func Uninstall() error {
	if !Installed() {
		return nil
	}
	if out, err := schtasks("/Delete", "/TN", TaskName, "/F"); err != nil {
		return fmt.Errorf("delete task: %v: %s", err, out)
	}
	return nil
}

func Installed() bool {
	_, err := schtasks("/Query", "/TN", TaskName)
	return err == nil
}

// Start asks Task Scheduler to launch the host. Repeated calls from many
// short-lived command adapters are rate limited through a marker file.
func Start() error {
	marker := filepath.Join(os.TempDir(), "tidalbridge-service-start")
	if info, err := os.Stat(marker); err == nil && time.Since(info.ModTime()) < 10*time.Second {
		return nil
	}
	os.WriteFile(marker, []byte(time.Now().Format(time.RFC3339)), 0600)
	if !Installed() {
		return ErrNotInstalled
	}
	if out, err := schtasks("/Run", "/TN", TaskName); err != nil {
		return fmt.Errorf("start task: %v: %s", err, out)
	}
	return nil
}

func Stop() error {
	if !Installed() {
		return ErrNotInstalled
	}
	out, err := schtasks("/End", "/TN", TaskName)
	if err != nil && !strings.Contains(strings.ToLower(out), "not running") {
		return fmt.Errorf("stop task: %v: %s", err, out)
	}
	return nil
}

// Status returns the scheduler's own view of the task.
func Status() (string, error) {
	out, err := schtasks("/Query", "/TN", TaskName, "/FO", "LIST")
	if err != nil {
		return "", ErrNotInstalled
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "Status:") {
			return strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "Status:")), nil
		}
	}
	return "Unknown", nil
}
