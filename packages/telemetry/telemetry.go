package telemetry

import (
	"context"
	"os"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/mem"
	"github.com/shirou/gopsutil/v4/process"
	"tidalbridge/packages/protocol"
)

type Snapshot struct {
	Resources        protocol.Resources `json:"resources"`
	DaemonRSSMB      float64            `json:"daemon_rss_mb"`
	DaemonHeapMB     float64            `json:"daemon_heap_mb"`
	DaemonCPUPercent float64            `json:"daemon_cpu_percent"`
	Goroutines       int                `json:"goroutines"`
	Sampled          time.Time          `json:"sampled"`
}
type Monitor struct {
	mu        sync.RWMutex
	value     Snapshot
	fastUntil atomic.Int64
}

// Boost samples every second for d (while a dashboard shows live values);
// otherwise every ten seconds, which is all the scheduler needs.
func (m *Monitor) Boost(d time.Duration) { m.fastUntil.Store(time.Now().Add(d).UnixNano()) }

func (m *Monitor) Snapshot() Snapshot { m.mu.RLock(); defer m.mu.RUnlock(); return m.value }
func (m *Monitor) Run(ctx context.Context) {
	p, _ := process.NewProcess(int32(os.Getpid()))
	m.sample(p)
	last := time.Now()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			if now.UnixNano() < m.fastUntil.Load() || now.Sub(last) >= 10*time.Second {
				m.sample(p)
				last = now
			}
		}
	}
}
func (m *Monitor) sample(p *process.Process) {
	s := Snapshot{Sampled: time.Now()}
	s.Resources.LogicalCores = runtime.NumCPU()
	if v, e := mem.VirtualMemory(); e == nil {
		s.Resources.RAMTotalMB = v.Total >> 20
		s.Resources.RAMPhysicalMB = physicalMemoryMB()
		s.Resources.RAMAvailableMB = v.Available >> 20
	}
	if v, e := disk.Usage(configDataPath()); e == nil {
		s.Resources.StorageAvailableMB = v.Free >> 20
	}
	if v, e := cpu.Percent(0, false); e == nil && len(v) > 0 {
		s.Resources.CPUPercent = v[0]
	}
	if p != nil {
		if v, e := p.MemoryInfo(); e == nil {
			s.DaemonRSSMB = float64(v.RSS) / (1 << 20)
		}
		s.DaemonCPUPercent, _ = p.Percent(0)
	}
	var stats runtime.MemStats
	runtime.ReadMemStats(&stats)
	s.DaemonHeapMB = float64(stats.HeapAlloc) / (1 << 20)
	s.Goroutines = runtime.NumGoroutine()
	m.mu.Lock()
	m.value = s
	m.mu.Unlock()
}
func configDataPath() string {
	if p, err := os.UserHomeDir(); err == nil && p != "" {
		return p
	}
	return "."
}
