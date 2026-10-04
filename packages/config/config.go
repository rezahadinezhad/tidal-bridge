package config

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

type Device struct {
	Serial   string `yaml:"serial" json:"serial"`
	Token    string `yaml:"token" json:"-"`
	Port     int    `yaml:"port" json:"port"`
	Endpoint string `yaml:"endpoint,omitempty" json:"endpoint,omitempty"`
	Mock     bool   `yaml:"mock" json:"mock"`
	// Launcher starts the worker when it does not answer: "termux" (bring
	// the Termux app forward; its service supervisor restarts the worker) or
	// "adb-shell" (the host starts the worker through the ADB shell, outside
	// Android's app CPU and memory restrictions).
	Launcher string `yaml:"launcher,omitempty" json:"launcher,omitempty"`
}
type Config struct {
	Bind   string `yaml:"bind" json:"bind"`
	Mode   string `yaml:"mode" json:"mode"`
	Paused bool   `yaml:"paused" json:"paused"`
	// NoReuse turns off reusing the result of a test or check whose inputs
	// did not change since a phone ran it.
	NoReuse           bool    `yaml:"no_reuse,omitempty" json:"no_reuse,omitempty"`
	ADB               string  `yaml:"adb" json:"adb"`
	QueueSize         int     `yaml:"queue_size" json:"queue_size"`
	LocalConcurrency  int     `yaml:"local_concurrency" json:"local_concurrency"`
	MaxConcurrency    int     `yaml:"max_concurrency" json:"max_concurrency"`
	WorkerConcurrency int     `yaml:"worker_concurrency" json:"worker_concurrency"`
	ReserveRAMMB      uint64  `yaml:"reserve_ram_mb" json:"reserve_ram_mb"`
	MinimumBenefit    float64 `yaml:"minimum_benefit" json:"minimum_benefit"`
	// PhoneUse "max" keeps full worker concurrency while the phone is in use;
	// the default runs one job at a time then.
	PhoneUse string `yaml:"phone_use,omitempty" json:"phone_use,omitempty"`
	// FixedCapacity keeps WorkerConcurrency jobs regardless of the phone's
	// state; by default fewer run while it is in use, warm, on battery or
	// short of memory.
	FixedCapacity bool     `yaml:"fixed_capacity,omitempty" json:"fixed_capacity,omitempty"`
	Approved      []Device `yaml:"approved" json:"approved"`
}

func Default() Config {
	return Config{Bind: "127.0.0.1:47831", Mode: "AUTO", QueueSize: 64, LocalConcurrency: 1, MaxConcurrency: 4, WorkerConcurrency: 3, ReserveRAMMB: 1024, MinimumBenefit: 0.15}
}
func Load(dir string) (Config, error) {
	c := Default()
	b, err := os.ReadFile(filepath.Join(dir, "config.yaml"))
	if err == nil {
		err = yaml.Unmarshal(b, &c)
	} else if os.IsNotExist(err) {
		err = nil
	}
	if err != nil {
		return c, err
	}
	return c, c.Validate()
}
func (c Config) Validate() error {
	host, _, err := net.SplitHostPort(c.Bind)
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		return fmt.Errorf("bind must be a loopback IP address and port")
	}
	switch c.Mode {
	case "AUTO", "CONSERVATIVE", "PERFORMANCE", "BATTERY_SAVER":
	default:
		return fmt.Errorf("invalid scheduling mode")
	}
	if c.PhoneUse != "" && c.PhoneUse != "gentle" && c.PhoneUse != "max" {
		return fmt.Errorf("phone_use must be gentle or max")
	}
	if c.QueueSize < 1 || c.QueueSize > 4096 || c.LocalConcurrency < 1 || c.MaxConcurrency < 1 || c.MaxConcurrency > 32 || c.WorkerConcurrency < 1 || c.WorkerConcurrency > 8 || c.MinimumBenefit < 0 || c.MinimumBenefit > 1 {
		return fmt.Errorf("invalid capacity or benefit settings")
	}
	for _, d := range c.Approved {
		if d.Serial == "" || len(d.Token) < 32 || d.Port < 1024 || d.Port > 65535 {
			return fmt.Errorf("approved device needs serial, token of at least 32 characters, and valid worker port")
		}
		if d.Endpoint != "" && (!d.Mock || !strings.HasPrefix(d.Endpoint, "http://127.0.0.1:")) {
			return fmt.Errorf("direct endpoints are restricted to explicit loopback mocks")
		}
	}
	return nil
}
func Save(dir string, c Config) error {
	if err := c.Validate(); err != nil {
		return err
	}
	b, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
	return Atomic(filepath.Join(dir, "config.yaml"), b)
}
func Atomic(path string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
func SaveJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return Atomic(path, b)
}
func Secret(dir string) (string, error) {
	path := filepath.Join(dir, "host.token")
	b, err := os.ReadFile(path)
	if err == nil {
		return strings.TrimSpace(string(b)), nil
	}
	if !os.IsNotExist(err) {
		return "", err
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	// Publish a complete credential without replacing a concurrent creator's
	// token. A shared .tmp name or overwrite would split launcher/host identity.
	file, err := os.CreateTemp(dir, ".host-token-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(file.Name())
	token := Random()
	if _, err = file.WriteString(token); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return "", err
	}
	if closeErr != nil {
		return "", closeErr
	}
	if err = os.Link(file.Name(), path); err == nil {
		return token, nil
	}
	if !os.IsExist(err) {
		return "", err
	}
	// The winning process linked a fully written file; every caller uses it.
	b, err = os.ReadFile(path)
	return strings.TrimSpace(string(b)), err
}
func Random() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
