// Package api implements the HTTP server that exposes metrics as JSON.
package api

import (
	"encoding/json"
	"log"
	"math"
	"net/http"
	"time"

	"simplemon/internal/collector"
	"simplemon/internal/config"
	"simplemon/internal/ringbuf"
	"simplemon/internal/widget"
)

// Server wraps the HTTP server and wires it to the collector.
type Server struct {
	cfg       *config.Config
	collector *collector.Collector
	widgets   *widget.Runner
	startedAt time.Time
	srv       *http.Server
}

// New creates an API Server.
func New(cfg *config.Config, col *collector.Collector, wr *widget.Runner) *Server {
	s := &Server{
		cfg:       cfg,
		collector: col,
		widgets:   wr,
		startedAt: time.Now(),
	}

	mux := http.NewServeMux()

	// GET /info          – static data: uptime, interface info, core count. Poll rarely (~1/min).
	// GET /metrics/last  – last 20 points (60 sec). Poll every 3 sec from frontend.
	// GET /metrics/full  – full history (up to retention_seconds). Request once on page load.
	// GET /widgets       – all custom widget data (graphs + text). Poll every 3 sec.
	// GET /health        – liveness check
	mux.HandleFunc("/info", s.handleInfo)
	mux.HandleFunc("/metrics/last", s.handleMetricsLast)
	mux.HandleFunc("/metrics/full", s.handleMetricsFull)
	mux.HandleFunc("/widgets", s.handleWidgets)
	mux.HandleFunc("/health", s.handleHealth)

	s.srv = &http.Server{
		Addr:         cfg.ListenAddr(),
		Handler:      corsMiddleware(mux),
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	return s
}

// ListenAndServe starts the HTTP server (blocks).
func (s *Server) ListenAndServe() error {
	log.Printf("[api] listening on http://%s", s.cfg.ListenAddr())
	return s.srv.ListenAndServe()
}

// Shutdown gracefully stops the server.
func (s *Server) Shutdown() {
	ctx, cancel := ctxWithTimeout(5 * time.Second)
	defer cancel()
	_ = s.srv.Shutdown(ctx)
}

// -------------------------------------------------------------------
// /health
// -------------------------------------------------------------------

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}

// -------------------------------------------------------------------
// /info  – static / rarely-changing data
// -------------------------------------------------------------------

// InfoPayload contains data that does not change frequently.
type InfoPayload struct {
	CollectedAt        int64                          `json:"collected_at"`
	BackendUptimeSecs  float64                        `json:"backend_uptime_seconds"` // time since simplemon started
	SystemUptimeSecs   uint64                         `json:"system_uptime_seconds"`
	IntervalSecs       int                            `json:"interval_secs"`
	RetentionSecs      int                            `json:"retention_seconds"`
	BufSize            int                            `json:"buf_size"`
	NumCPUCores        int                            `json:"num_cpu_cores"`
	Interfaces         map[string]collector.IfaceInfo `json:"interfaces"`
}

func (s *Server) handleInfo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	sysUptime, _ := collector.GetSystemUptime()
	ifInfos, _ := collector.GetIfaceInfo(s.cfg.MatchInterface)
	m := s.collector.Metrics()

	p := InfoPayload{
		CollectedAt:       time.Now().Unix(),
		BackendUptimeSecs: time.Since(s.startedAt).Seconds(),
		SystemUptimeSecs:  sysUptime,
		IntervalSecs:      s.cfg.Collector.IntervalSeconds,
		RetentionSecs:     s.cfg.Collector.RetentionSecs,
		BufSize:           s.cfg.Collector.RetentionSecs / s.cfg.Collector.IntervalSeconds,
		NumCPUCores:       m.NumCores,
		Interfaces:        ifInfos,
	}

	writeJSON(w, p)
}

// -------------------------------------------------------------------
// /metrics  – time-series data
//
// Query params:
//   ?points=N   return last N samples per counter (default 20, 0 = full history)
// -------------------------------------------------------------------

// MetricsPayload is the time-series JSON object.
type MetricsPayload struct {
	CollectedAt       int64                      `json:"collected_at"`
	BackendUptimeSecs float64                    `json:"backend_uptime_seconds"`
	Points            int                        `json:"points"`     // actual number of points returned
	CPU               CPUPayload                 `json:"cpu"`
	RAM               MemPayload                 `json:"ram"`
	Swap              MemPayload                 `json:"swap"`
	Interfaces        map[string]IfacePayload    `json:"interfaces"`
}

type CPUPayload struct {
	Timestamps []int64     `json:"timestamps"`
	Total      []float64   `json:"total"`  // overall usage %,  2 decimal places
	Cores      [][]float64 `json:"cores"`  // [core_index][sample]
}

type MemPayload struct {
	// Total is a scalar – it almost never changes, no need to repeat it per sample.
	TotalBytes uint64    `json:"total_bytes"`
	UsedBytes  []float64 `json:"used_bytes"`
	FreeBytes  []float64 `json:"free_bytes"`
	UsedPct    []float64 `json:"used_pct"`
}

type IfacePayload struct {
	Timestamps  []int64   `json:"timestamps"`
	BytesRecv   []float64 `json:"bytes_recv"`
	BytesSent   []float64 `json:"bytes_sent"`
	PacketsRecv []float64 `json:"packets_recv"`
	PacketsSent []float64 `json:"packets_sent"`
	ErrIn       []float64 `json:"err_in"`
	ErrOut      []float64 `json:"err_out"`
	RxRateBits  []float64 `json:"rx_rate_bits"` // bits/s; /1000=kbps, /1e6=Mbps
	TxRateBits  []float64 `json:"tx_rate_bits"`
}

const lastPoints = 20

func (s *Server) handleMetricsLast(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, s.buildMetrics(lastPoints))
}

func (s *Server) handleMetricsFull(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, s.buildMetrics(0))
}

func (s *Server) buildMetrics(points int) *MetricsPayload {
	m := s.collector.Metrics()

	bufSize := s.cfg.Collector.RetentionSecs / s.cfg.Collector.IntervalSeconds

	// points=0 means full history
	if points == 0 || points > bufSize {
		points = bufSize
	}

	// Actual points may be less than requested if buffer is not yet full
	actualPoints := m.CPUTotal.Fill()
	if points < actualPoints {
		actualPoints = points
	}

	p := &MetricsPayload{
		CollectedAt:       time.Now().Unix(),
		BackendUptimeSecs: time.Since(s.startedAt).Seconds(),
		Points:            actualPoints,
		Interfaces:        make(map[string]IfacePayload),
	}

	// --- CPU ---
	cpuSnap := snapLast(m.CPUTotal, points)
	p.CPU.Timestamps = snapTimestamps(m.CPUTotal, points)
	p.CPU.Total = roundSlice(cpuSnap, 2)
	p.CPU.Cores = make([][]float64, len(m.CPUCores))
	for i, ring := range m.CPUCores {
		p.CPU.Cores[i] = roundSlice(snapLast(ring, points), 2)
	}

	// --- RAM ---
	// TotalBytes: take the last non-zero value as a scalar
	p.RAM.TotalBytes = lastNonZeroUint(m.RAMTotal)
	p.RAM.UsedBytes = roundSlice(snapLast(m.RAMUsed, points), 0)
	p.RAM.FreeBytes = roundSlice(snapLast(m.RAMFree, points), 0)
	p.RAM.UsedPct = roundSlice(snapLast(m.RAMPct, points), 2)

	// --- Swap ---
	p.Swap.TotalBytes = lastNonZeroUint(m.SwapTotal)
	p.Swap.UsedBytes = roundSlice(snapLast(m.SwapUsed, points), 0)
	p.Swap.FreeBytes = roundSlice(snapLast(m.SwapFree, points), 0)
	p.Swap.UsedPct = roundSlice(snapLast(m.SwapPct, points), 2)

	// --- Interfaces ---
	ifacesCopy := s.collector.SnapshotIfaces()
	for name, im := range ifacesCopy {
		p.Interfaces[name] = IfacePayload{
			Timestamps:  snapTimestamps(im.BytesRecv, points),
			BytesRecv:   roundSlice(snapLast(im.BytesRecv, points), 0),
			BytesSent:   roundSlice(snapLast(im.BytesSent, points), 0),
			PacketsRecv: roundSlice(snapLast(im.PacketsRecv, points), 0),
			PacketsSent: roundSlice(snapLast(im.PacketsSent, points), 0),
			ErrIn:       roundSlice(snapLast(im.ErrIn, points), 0),
			ErrOut:      roundSlice(snapLast(im.ErrOut, points), 0),
			RxRateBits:  roundSlice(snapLast(im.RxRate, points), 2),
			TxRateBits:  roundSlice(snapLast(im.TxRate, points), 2),
		}
	}

	return p
}

// -------------------------------------------------------------------
// Ring buffer helpers
// -------------------------------------------------------------------

// snapSlice returns the last n samples from ring respecting actual fill level.
// When the buffer is not yet full, data lives in [0..fill-1]; we take the
// last n of those filled slots so we never return trailing zeros.
func snapSlice(r *ringbuf.Ring, n int) []ringbuf.Sample {
	all := r.Snapshot()
	fill := r.Fill()
	// Clamp to how many samples actually exist
	if n > fill {
		n = fill
	}
	if n <= 0 {
		return nil
	}
	// Data is in all[0..fill-1] when not full, or rotated when full.
	// Snapshot() already returns chronological order, so filled data is
	// always in all[0..fill-1] when not full, and all[0..size-1] when full.
	src := all[:fill]
	if n >= fill {
		return src
	}
	return src[fill-n:]
}

// snapLast returns the last n values from ring (oldest → newest).
func snapLast(r *ringbuf.Ring, n int) []float64 {
	samples := snapSlice(r, n)
	out := make([]float64, len(samples))
	for i, s := range samples {
		out[i] = s.Value
	}
	return out
}

// snapTimestamps returns the last n timestamps from ring.
func snapTimestamps(r *ringbuf.Ring, n int) []int64 {
	samples := snapSlice(r, n)
	out := make([]int64, len(samples))
	for i, s := range samples {
		out[i] = s.Ts
	}
	return out
}

// roundSlice rounds every value to prec decimal places.
// prec=0 returns integer-valued float64 (no decimals in JSON).
func roundSlice(in []float64, prec int) []float64 {
	if prec < 0 {
		return in
	}
	factor := math.Pow(10, float64(prec))
	out := make([]float64, len(in))
	for i, v := range in {
		out[i] = math.Round(v*factor) / factor
	}
	return out
}

// lastNonZeroUint returns the last non-zero value as uint64 (for TotalBytes).
func lastNonZeroUint(r *ringbuf.Ring) uint64 {
	all := r.Snapshot()
	for i := len(all) - 1; i >= 0; i-- {
		if all[i].Value > 0 {
			return uint64(all[i].Value)
		}
	}
	return 0
}

// -------------------------------------------------------------------
// Response helpers
// -------------------------------------------------------------------

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-cache")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("[api] encode error: %v", err)
	}
}

// -------------------------------------------------------------------
// Middleware
// -------------------------------------------------------------------

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}


// -------------------------------------------------------------------
// /widgets
// -------------------------------------------------------------------

// WidgetsPayload is the top-level response for /widgets.
type WidgetsPayload struct {
	CollectedAt int64               `json:"collected_at"`
	Graphs      []GraphWidgetPayload `json:"graphs"`
	Texts       []TextWidgetPayload  `json:"texts"`
}

// GraphWidgetPayload carries time-series data for a graph widget.
type GraphWidgetPayload struct {
	Name       string    `json:"name"`
	Unit       string    `json:"unit,omitempty"`
	Command    string    `json:"command"`
	Points     int       `json:"points"`
	Timestamps []int64   `json:"timestamps"`
	Values     []float64 `json:"values"`
}

// TextWidgetPayload carries the last text output of a text widget.
type TextWidgetPayload struct {
	Name      string `json:"name"`
	Command   string `json:"command"`
	Output    string `json:"output"`
	Error     string `json:"error,omitempty"`
	UpdatedAt int64  `json:"updated_at"`
}

func (s *Server) handleWidgets(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, s.buildWidgets())
}

func (s *Server) buildWidgets() *WidgetsPayload {
	p := &WidgetsPayload{
		CollectedAt: time.Now().Unix(),
		Graphs:      []GraphWidgetPayload{},
		Texts:       []TextWidgetPayload{},
	}

	if s.widgets == nil {
		return p
	}

	for _, g := range s.widgets.Graphs {
		fill := g.Ring.Fill()
		timestamps := snapTimestamps(g.Ring, fill)
		values := snapLast(g.Ring, fill)
		p.Graphs = append(p.Graphs, GraphWidgetPayload{
			Name:       g.Cfg.Name,
			Unit:       g.Cfg.Unit,
			Command:    g.Cfg.Command,
			Points:     fill,
			Timestamps: timestamps,
			Values:     values,
		})
	}

	for _, t := range s.widgets.Texts {
		output, errMsg, ts := t.Snapshot()
		p.Texts = append(p.Texts, TextWidgetPayload{
			Name:      t.Cfg.Name,
			Command:   t.Cfg.Command,
			Output:    output,
			Error:     errMsg,
			UpdatedAt: ts,
		})
	}

	return p
}
