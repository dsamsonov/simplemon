// Package widget runs user-defined commands and stores their output.
// type=graph: parses the first line of stdout as float64, stores in a ring buffer.
// type=text:  stores the full output (capped at 200 lines), updated each run.
package widget

import (
	"bytes"
	"context"
	"log"
	"math"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"simplemon/internal/config"
	"simplemon/internal/ringbuf"
)

const maxTextLines = 200

// GraphWidget holds the ring buffer for a graph-type widget.
type GraphWidget struct {
	Cfg  config.WidgetConfig
	Ring *ringbuf.Ring
}

// TextWidget holds the last output for a text-type widget.
type TextWidget struct {
	Cfg         config.WidgetConfig
	mu          sync.RWMutex
	LastOutput  string
	LastRunAt   int64 // Unix timestamp
	LastError   string
}

func (t *TextWidget) set(output, errMsg string, ts time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.LastOutput = output
	t.LastError = errMsg
	t.LastRunAt = ts.Unix()
}

// Snapshot returns a safe copy of the text widget state.
func (t *TextWidget) Snapshot() (output, errMsg string, ts int64) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.LastOutput, t.LastError, t.LastRunAt
}

// Runner manages all configured widgets.
type Runner struct {
	Graphs []*GraphWidget
	Texts  []*TextWidget

	defaultInterval int
	bufSize         int
}

// New creates a Runner from config. bufSize should match collector ring size.
func New(cfg *config.Config) *Runner {
	bufSize := cfg.Collector.RetentionSecs / cfg.Collector.IntervalSeconds
	r := &Runner{
		defaultInterval: cfg.Collector.IntervalSeconds,
		bufSize:         bufSize,
	}
	for _, wc := range cfg.Widgets {
		wc := wc // capture
		switch wc.Type {
		case config.WidgetTypeGraph:
			r.Graphs = append(r.Graphs, &GraphWidget{
				Cfg:  wc,
				Ring: ringbuf.New(bufSize),
			})
		case config.WidgetTypeText:
			r.Texts = append(r.Texts, &TextWidget{Cfg: wc})
		}
	}
	return r
}

// Run starts a goroutine per widget and blocks until ctx is cancelled.
func (r *Runner) Run(ctx context.Context) {
	var wg sync.WaitGroup
	for _, g := range r.Graphs {
		wg.Add(1)
		go func(g *GraphWidget) {
			defer wg.Done()
			r.runGraph(ctx, g)
		}(g)
	}
	for _, t := range r.Texts {
		wg.Add(1)
		go func(t *TextWidget) {
			defer wg.Done()
			r.runText(ctx, t)
		}(t)
	}
	wg.Wait()
}

func (r *Runner) interval(wc config.WidgetConfig) time.Duration {
	secs := wc.IntervalSeconds
	if secs <= 0 {
		secs = r.defaultInterval
	}
	return time.Duration(secs) * time.Second
}

// runGraph collects numeric output in a loop.
func (r *Runner) runGraph(ctx context.Context, g *GraphWidget) {
	iv := r.interval(g.Cfg)
	ticker := time.NewTicker(iv)
	defer ticker.Stop()

	// First run immediately
	r.collectGraph(ctx, g)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.collectGraph(ctx, g)
		}
	}
}

func (r *Runner) collectGraph(ctx context.Context, g *GraphWidget) {
	now := time.Now()
	out, err := runCommand(ctx, g.Cfg.Command)
	if err != nil {
		log.Printf("[widget] %q error: %v", g.Cfg.Name, err)
		g.Ring.Push(now, 0)
		return
	}
	line := strings.TrimSpace(strings.SplitN(out, "\n", 2)[0])
	val, err := strconv.ParseFloat(line, 64)
	if err != nil {
		log.Printf("[widget] %q: cannot parse %q as float: %v", g.Cfg.Name, line, err)
		g.Ring.Push(now, 0)
		return
	}
	// Round to 2 decimal places
	val = math.Round(val*100) / 100
	g.Ring.Push(now, val)
}

// runText collects full command output in a loop.
func (r *Runner) runText(ctx context.Context, t *TextWidget) {
	iv := r.interval(t.Cfg)
	ticker := time.NewTicker(iv)
	defer ticker.Stop()

	r.collectText(ctx, t)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.collectText(ctx, t)
		}
	}
}

func (r *Runner) collectText(ctx context.Context, t *TextWidget) {
	now := time.Now()
	out, err := runCommand(ctx, t.Cfg.Command)
	errMsg := ""
	if err != nil {
		errMsg = err.Error()
		log.Printf("[widget] %q error: %v", t.Cfg.Name, err)
	}
	// Cap to maxTextLines
	if out != "" {
		lines := strings.Split(out, "\n")
		if len(lines) > maxTextLines {
			lines = lines[:maxTextLines]
		}
		out = strings.Join(lines, "\n")
	}
	t.set(out, errMsg, now)
}

// runCommand executes a shell command with a 30-second timeout.
func runCommand(ctx context.Context, command string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", command)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	if err := cmd.Run(); err != nil {
		return buf.String(), err
	}
	return buf.String(), nil
}
