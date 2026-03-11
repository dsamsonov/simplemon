// Package collector gathers system metrics and stores them in ring buffers.
package collector

import (
	"context"
	"log"
	"net"
	"regexp"
	"sync"
	"time"

	"simplemon/internal/config"
	"simplemon/internal/ringbuf"
	"github.com/shirou/gopsutil/v3/cpu"
	"github.com/shirou/gopsutil/v3/host"
	"github.com/shirou/gopsutil/v3/mem"
	psnet "github.com/shirou/gopsutil/v3/net"
)

// -------------------------------------------------------------------
// Metric store
// -------------------------------------------------------------------

// Metrics holds all ring buffers.
type Metrics struct {
	mu sync.RWMutex

	// CPU
	CPUTotal  *ringbuf.Ring   // overall %
	CPUCores  []*ringbuf.Ring // per-core %

	// RAM
	RAMTotal *ringbuf.Ring // bytes total (constant, but stored for convenience)
	RAMUsed  *ringbuf.Ring // bytes used
	RAMFree  *ringbuf.Ring // bytes free
	RAMPct   *ringbuf.Ring // usage %

	// Swap
	SwapTotal *ringbuf.Ring
	SwapUsed  *ringbuf.Ring
	SwapFree  *ringbuf.Ring
	SwapPct   *ringbuf.Ring

	// Network – keyed by interface name
	Ifaces map[string]*IfaceMetrics

	// Static info
	NumCores int
}

// IfaceMetrics holds per-interface ring buffers + static info.
type IfaceMetrics struct {
	// Counters
	BytesRecv   *ringbuf.Ring
	BytesSent   *ringbuf.Ring
	PacketsRecv *ringbuf.Ring
	PacketsSent *ringbuf.Ring
	ErrIn       *ringbuf.Ring
	ErrOut      *ringbuf.Ring

	// Rates (bytes/s computed between two samples)
	RxRate *ringbuf.Ring
	TxRate *ringbuf.Ring
}

// IfaceInfo carries static / semi-static interface metadata.
type IfaceInfo struct {
	Name    string `json:"name"`
	MAC     string `json:"mac"`
	IPs     []string `json:"ips"`
	Speed   uint64 `json:"speed_mbps"` // Mbps, 0 if unknown
	IsUp    bool   `json:"is_up"`
}

// -------------------------------------------------------------------
// Collector
// -------------------------------------------------------------------

// Collector runs the metric collection loop.
type Collector struct {
	cfg     *config.Config
	metrics *Metrics

	// compiled interface regexps
	ifPatterns []*regexp.Regexp

	// previous network counters for rate calculation
	prevCounters map[string]psnet.IOCountersStat
	prevTime     time.Time
}

// New creates a Collector and pre-allocates all ring buffers.
func New(cfg *config.Config) (*Collector, error) {
	bufSize := cfg.Collector.RetentionSecs / cfg.Collector.IntervalSeconds
	if bufSize < 1 {
		bufSize = 1
	}

	// Detect CPU cores
	cores, err := cpu.Counts(true)
	if err != nil || cores < 1 {
		cores = 1
	}

	m := &Metrics{
		CPUTotal:  ringbuf.New(bufSize),
		CPUCores:  make([]*ringbuf.Ring, cores),
		RAMTotal:  ringbuf.New(bufSize),
		RAMUsed:   ringbuf.New(bufSize),
		RAMFree:   ringbuf.New(bufSize),
		RAMPct:    ringbuf.New(bufSize),
		SwapTotal: ringbuf.New(bufSize),
		SwapUsed:  ringbuf.New(bufSize),
		SwapFree:  ringbuf.New(bufSize),
		SwapPct:   ringbuf.New(bufSize),
		Ifaces:    make(map[string]*IfaceMetrics),
		NumCores:  cores,
	}
	for i := range m.CPUCores {
		m.CPUCores[i] = ringbuf.New(bufSize)
	}

	// Compile interface patterns
	var patterns []*regexp.Regexp
	for _, pat := range cfg.Interfaces.Include {
		re, _ := regexp.Compile(pat)
		patterns = append(patterns, re)
	}

	c := &Collector{
		cfg:          cfg,
		metrics:      m,
		ifPatterns:   patterns,
		prevCounters: make(map[string]psnet.IOCountersStat),
	}

	// Discover interfaces now and pre-allocate buffers
	c.discoverInterfaces(bufSize)

	return c, nil
}

// Metrics returns the shared metrics structure (read-only intended for API).
func (c *Collector) Metrics() *Metrics {
	return c.metrics
}

// SnapshotIfaces returns a safe copy of the ifaces map under read lock.
func (c *Collector) SnapshotIfaces() map[string]*IfaceMetrics {
	c.metrics.mu.RLock()
	defer c.metrics.mu.RUnlock()
	cp := make(map[string]*IfaceMetrics, len(c.metrics.Ifaces))
	for k, v := range c.metrics.Ifaces {
		cp[k] = v
	}
	return cp
}

// Run starts the collection loop; blocks until ctx is cancelled.
func (c *Collector) Run(ctx context.Context) {
	interval := time.Duration(c.cfg.Collector.IntervalSeconds) * time.Second
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Collect once immediately
	c.collect()

	for {
		select {
		case <-ctx.Done():
			return
		case t := <-ticker.C:
			_ = t
			c.collect()
		}
	}
}

// -------------------------------------------------------------------
// Internal helpers
// -------------------------------------------------------------------

func (c *Collector) matchIface(name string) bool {
	for _, re := range c.ifPatterns {
		if re.MatchString(name) {
			return true
		}
	}
	return false
}

func (c *Collector) discoverInterfaces(bufSize int) {
	ifaces, err := psnet.Interfaces()
	if err != nil {
		log.Printf("[collector] interfaces discovery error: %v", err)
		return
	}
	c.metrics.mu.Lock()
	defer c.metrics.mu.Unlock()
	for _, iface := range ifaces {
		if !c.matchIface(iface.Name) {
			continue
		}
		if _, exists := c.metrics.Ifaces[iface.Name]; !exists {
			c.metrics.Ifaces[iface.Name] = &IfaceMetrics{
				BytesRecv:   ringbuf.New(bufSize),
				BytesSent:   ringbuf.New(bufSize),
				PacketsRecv: ringbuf.New(bufSize),
				PacketsSent: ringbuf.New(bufSize),
				ErrIn:       ringbuf.New(bufSize),
				ErrOut:      ringbuf.New(bufSize),
				RxRate:      ringbuf.New(bufSize),
				TxRate:      ringbuf.New(bufSize),
			}
		}
	}
}

func (c *Collector) collect() {
	now := time.Now()

	c.collectCPU(now)
	c.collectMem(now)
	c.collectNet(now)
}

func (c *Collector) collectCPU(now time.Time) {
	// Overall (interval=false → since last call)
	totals, err := cpu.Percent(0, false)
	if err == nil && len(totals) > 0 {
		c.metrics.CPUTotal.Push(now, totals[0])
	}

	// Per-core
	perCore, err := cpu.Percent(0, true)
	if err == nil {
		for i, pct := range perCore {
			if i < len(c.metrics.CPUCores) {
				c.metrics.CPUCores[i].Push(now, pct)
			}
		}
	}
}

func (c *Collector) collectMem(now time.Time) {
	// RAM
	vm, err := mem.VirtualMemory()
	if err == nil {
		c.metrics.RAMTotal.Push(now, float64(vm.Total))
		c.metrics.RAMUsed.Push(now, float64(vm.Used))
		c.metrics.RAMFree.Push(now, float64(vm.Free))
		c.metrics.RAMPct.Push(now, vm.UsedPercent)
	}

	// Swap
	sw, err := mem.SwapMemory()
	if err == nil {
		c.metrics.SwapTotal.Push(now, float64(sw.Total))
		c.metrics.SwapUsed.Push(now, float64(sw.Used))
		c.metrics.SwapFree.Push(now, float64(sw.Free))
		c.metrics.SwapPct.Push(now, sw.UsedPercent)
	}
}

func (c *Collector) collectNet(now time.Time) {
	counters, err := psnet.IOCounters(true)
	if err != nil {
		log.Printf("[collector] net counters error: %v", err)
		return
	}

	bufSize := c.cfg.Collector.RetentionSecs / c.cfg.Collector.IntervalSeconds

	c.metrics.mu.Lock()
	defer c.metrics.mu.Unlock()

	elapsed := now.Sub(c.prevTime).Seconds()
	if elapsed <= 0 {
		elapsed = float64(c.cfg.Collector.IntervalSeconds)
	}

	for _, cnt := range counters {
		if !c.matchIface(cnt.Name) {
			continue
		}

		// Ensure buffer exists (interface may appear later)
		if _, ok := c.metrics.Ifaces[cnt.Name]; !ok {
			c.metrics.Ifaces[cnt.Name] = &IfaceMetrics{
				BytesRecv:   ringbuf.New(bufSize),
				BytesSent:   ringbuf.New(bufSize),
				PacketsRecv: ringbuf.New(bufSize),
				PacketsSent: ringbuf.New(bufSize),
				ErrIn:       ringbuf.New(bufSize),
				ErrOut:      ringbuf.New(bufSize),
				RxRate:      ringbuf.New(bufSize),
				TxRate:      ringbuf.New(bufSize),
			}
		}

		im := c.metrics.Ifaces[cnt.Name]
		im.BytesRecv.Push(now, float64(cnt.BytesRecv))
		im.BytesSent.Push(now, float64(cnt.BytesSent))
		im.PacketsRecv.Push(now, float64(cnt.PacketsRecv))
		im.PacketsSent.Push(now, float64(cnt.PacketsSent))
		im.ErrIn.Push(now, float64(cnt.Errin))
		im.ErrOut.Push(now, float64(cnt.Errout))

		// Rate calculation
		if prev, ok := c.prevCounters[cnt.Name]; ok && !c.prevTime.IsZero() {
			// rates in bits per second (×8) for easy kbps/Mbps conversion on frontend
			rxRate := float64(cnt.BytesRecv-prev.BytesRecv) / elapsed * 8
			txRate := float64(cnt.BytesSent-prev.BytesSent) / elapsed * 8
			if rxRate < 0 {
				rxRate = 0
			}
			if txRate < 0 {
				txRate = 0
			}
			im.RxRate.Push(now, rxRate)
			im.TxRate.Push(now, txRate)
		} else {
			im.RxRate.Push(now, 0)
			im.TxRate.Push(now, 0)
		}

		c.prevCounters[cnt.Name] = cnt
	}
	c.prevTime = now
}

// -------------------------------------------------------------------
// Static info helpers (called by API)
// -------------------------------------------------------------------

// GetIfaceInfo returns current static info for all monitored interfaces.
func GetIfaceInfo(matchFn func(string) bool) (map[string]IfaceInfo, error) {
	result := make(map[string]IfaceInfo)

	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}

	psIfaces, _ := psnet.Interfaces()
	psMap := make(map[string]psnet.InterfaceStat)
	for _, pi := range psIfaces {
		psMap[pi.Name] = pi
	}

	for _, iface := range ifaces {
		if !matchFn(iface.Name) {
			continue
		}

		info := IfaceInfo{
			Name: iface.Name,
			MAC:  iface.HardwareAddr.String(),
			IsUp: iface.Flags&net.FlagUp != 0,
		}

		addrs, _ := iface.Addrs()
		for _, addr := range addrs {
			info.IPs = append(info.IPs, addr.String())
		}

		// Try to get speed from sysfs
		info.Speed = readIfaceSpeed(iface.Name)

		result[iface.Name] = info
	}
	return result, nil
}

// GetSystemUptime returns host uptime in seconds.
func GetSystemUptime() (uint64, error) {
	return host.Uptime()
}

// readIfaceSpeed reads speed from /sys/class/net/<name>/speed (Linux).
func readIfaceSpeed(name string) uint64 {
	path := "/sys/class/net/" + name + "/speed"
	data, err := readFile(path)
	if err != nil {
		return 0
	}
	var speed uint64
	// data is like "1000\n"
	for _, b := range data {
		if b >= '0' && b <= '9' {
			speed = speed*10 + uint64(b-'0')
		}
	}
	return speed
}

func readFile(path string) ([]byte, error) {
	f, err := openFile(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	buf := make([]byte, 64)
	n, err := f.Read(buf)
	if err != nil && n == 0 {
		return nil, err
	}
	return buf[:n], nil
}
