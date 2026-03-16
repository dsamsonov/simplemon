// Package watcher runs user-defined check commands, inspects their exit code,
// dispatches to the matching action command, and stores the result as a widget.
//
// Flow per watcher tick:
//  1. Run check_command, capture exit code.
//  2. Find the WatcherAction whose exit_code matches.
//     If no match → store "unexpected exit code: N" as a text result.
//  3. Run the action's command, store stdout as text or parse first line as
//     float64 for graph, exactly like regular widgets.
//  4. Record last_exit_code and last_check_at for the API.
package watcher

import (
	"bytes"
	"context"
	"errors"
	"fmt"
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

// State holds the current result of a single watcher.
type State struct {
	mu           sync.RWMutex
	lastExitCode int   // exit code of the most recent check_command run
	lastCheckAt  int64 // unix timestamp of the most recent check_command run

	widgetType config.WidgetType

	// text result
	lastOutput string
	lastError  string

	// graph result
	ring *ringbuf.Ring
	unit string
}

func (s *State) setCheck(code int, ts time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastExitCode = code
	s.lastCheckAt = ts.Unix()
}

func (s *State) setText(output, errMsg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastOutput = output
	s.lastError = errMsg
}

// SnapshotText returns a safe copy of the text state.
func (s *State) SnapshotText() (output, errMsg string, exitCode int, checkAt int64) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lastOutput, s.lastError, s.lastExitCode, s.lastCheckAt
}

// SnapshotGraph returns graph data plus exit-code meta.
func (s *State) SnapshotGraph() (ring *ringbuf.Ring, unit string, exitCode int, checkAt int64) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.ring, s.unit, s.lastExitCode, s.lastCheckAt
}

// WidgetType returns the current widget type (may change between ticks if
// different exit codes map to different widget_type actions).
func (s *State) WidgetType() config.WidgetType {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.widgetType
}

// Watcher is a single configured watcher instance.
type Watcher struct {
	Cfg   config.WatcherConfig
	State *State
}

// Runner manages all configured watchers.
type Runner struct {
	Watchers []*Watcher

	defaultInterval int
	bufSize         int
}

// New creates a Runner from config.
func New(cfg *config.Config) *Runner {
	bufSize := cfg.Collector.RetentionSecs / cfg.Collector.IntervalSeconds
	r := &Runner{
		defaultInterval: cfg.Collector.IntervalSeconds,
		bufSize:         bufSize,
	}

	for _, wc := range cfg.Watchers {
		wc := wc

		// Use first action as the initial widget type.
		firstType := config.WidgetTypeText
		firstUnit := ""
		if len(wc.Actions) > 0 {
			firstType = wc.Actions[0].WidgetType
			firstUnit = wc.Actions[0].Unit
		}

		st := &State{
			widgetType: firstType,
			unit:       firstUnit,
		}
		if firstType == config.WidgetTypeGraph {
			st.ring = ringbuf.New(bufSize)
		}

		r.Watchers = append(r.Watchers, &Watcher{Cfg: wc, State: st})
	}
	return r
}

// Run starts a goroutine per watcher and blocks until ctx is cancelled.
func (r *Runner) Run(ctx context.Context) {
	var wg sync.WaitGroup
	for _, w := range r.Watchers {
		wg.Add(1)
		go func(w *Watcher) {
			defer wg.Done()
			r.runWatcher(ctx, w)
		}(w)
	}
	wg.Wait()
}

func (r *Runner) interval(wc config.WatcherConfig) time.Duration {
	secs := wc.IntervalSeconds
	if secs <= 0 {
		secs = r.defaultInterval
	}
	return time.Duration(secs) * time.Second
}

func (r *Runner) runWatcher(ctx context.Context, w *Watcher) {
	iv := r.interval(w.Cfg)
	ticker := time.NewTicker(iv)
	defer ticker.Stop()

	r.tick(ctx, w)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.tick(ctx, w)
		}
	}
}

func (r *Runner) tick(ctx context.Context, w *Watcher) {
	now := time.Now()

	// 1. Run check_command and capture exit code.
	exitCode, err := runCheckCommand(ctx, w.Cfg.CheckCommand)
	if err != nil && exitCode < 0 {
		log.Printf("[watcher] %q check_command failed to run: %v", w.Cfg.Name, err)
		w.State.setCheck(-1, now)
		w.State.setText("", fmt.Sprintf("check_command failed: %v", err))
		return
	}

	w.State.setCheck(exitCode, now)

	// 2. Find matching action.
	action, found := findAction(w.Cfg.Actions, exitCode)
	if !found {
		msg := fmt.Sprintf("unexpected exit code: %d", exitCode)
		log.Printf("[watcher] %q: %s", w.Cfg.Name, msg)
		w.State.setText("", msg)
		return
	}

	// 3. Update widget type/unit if the matched action differs.
	w.State.mu.Lock()
	if w.State.widgetType != action.WidgetType {
		w.State.widgetType = action.WidgetType
		if action.WidgetType == config.WidgetTypeGraph && w.State.ring == nil {
			w.State.ring = ringbuf.New(r.bufSize)
		}
	}
	if action.Unit != "" {
		w.State.unit = action.Unit
	}
	w.State.mu.Unlock()

	// 4. Run action command.
	output, runErr := runActionCommand(ctx, action.Command)
	errMsg := ""
	if runErr != nil {
		errMsg = runErr.Error()
		log.Printf("[watcher] %q action command error: %v", w.Cfg.Name, runErr)
	}

	// 5. Store result.
	switch action.WidgetType {
	case config.WidgetTypeGraph:
		val, parseErr := parseFloat(output, w.Cfg.Name)
		w.State.mu.Lock()
		if w.State.ring != nil {
			w.State.ring.Push(now, val)
		}
		w.State.mu.Unlock()
		// Surface whichever error is more specific: action command error takes
		// priority; if the command succeeded but output wasn't parseable, show
		// the parse error so the user sees what arrived instead of a number.
		if errMsg == "" {
			errMsg = parseErr
		}
		w.State.setText("", errMsg)

	case config.WidgetTypeText:
		if output != "" {
			lines := strings.Split(output, "\n")
			if len(lines) > maxTextLines {
				lines = lines[:maxTextLines]
			}
			output = strings.Join(lines, "\n")
		}
		w.State.setText(output, errMsg)
	}
}

// -------------------------------------------------------------------
// Helpers
// -------------------------------------------------------------------

func findAction(actions []config.WatcherAction, code int) (config.WatcherAction, bool) {
	for _, a := range actions {
		if a.ExitCode == code {
			return a, true
		}
	}
	return config.WatcherAction{}, false
}

// runCheckCommand runs cmd and returns its exit code.
// Returns (exitCode, nil) on normal exit (including non-zero).
// Returns (-1, err) only if the command could not be started or timed out.
func runCheckCommand(ctx context.Context, command string) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", command)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf

	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return exitErr.ExitCode(), nil
		}
		return -1, err
	}
	return 0, nil
}

// runActionCommand runs cmd and returns stdout+stderr combined.
func runActionCommand(ctx context.Context, command string) (string, error) {
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

// parseFloat parses the first line of output as float64 (for graph widgets).
// Returns (value, "") on success or (0, errorMessage) on failure.
func parseFloat(output, name string) (float64, string) {
	line := strings.TrimSpace(strings.SplitN(output, "\n", 2)[0])
	val, err := strconv.ParseFloat(line, 64)
	if err != nil {
		msg := fmt.Sprintf("cannot parse %q as float", line)
		log.Printf("[watcher] %q: %s", name, msg)
		return 0, msg
	}
	return math.Round(val*100) / 100, ""
}
