package config

import (
	"fmt"
	"os"
	"regexp"

	"gopkg.in/yaml.v3"
)

const DefaultConfigPath = "/etc/simplemon/simplemon.yaml"

type Config struct {
	Listen     ListenConfig     `yaml:"listen"`
	Interfaces InterfacesConfig `yaml:"interfaces"`
	Collector  CollectorConfig  `yaml:"collector"`
	Widgets    []WidgetConfig   `yaml:"widgets"`
}

// WidgetType defines how the widget output is handled.
type WidgetType string

const (
	WidgetTypeGraph WidgetType = "graph" // parse first line as float64, store in ring buffer
	WidgetTypeText  WidgetType = "text"  // store full output (up to 200 lines)
)

// WidgetConfig describes a single user-defined widget.
type WidgetConfig struct {
	Name            string     `yaml:"name"`             // display name
	Type            WidgetType `yaml:"type"`             // "graph" or "text"
	Command         string     `yaml:"command"`          // shell command to execute
	IntervalSeconds int        `yaml:"interval_seconds"` // 0 = use collector interval
	Unit            string     `yaml:"unit"`             // optional, e.g. "°C", "MB/s"
}

type ListenConfig struct {
	Address string `yaml:"address"`
	Port    int    `yaml:"port"`
}

type InterfacesConfig struct {
	Include []string `yaml:"include"` // literal names or regexp patterns
}

type CollectorConfig struct {
	IntervalSeconds int `yaml:"interval_seconds"`
	RetentionSecs   int `yaml:"retention_seconds"`
}

func Load(path string) (*Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open config %q: %w", path, err)
	}
	defer f.Close()

	cfg := defaultConfig()
	dec := yaml.NewDecoder(f)
	dec.KnownFields(true)
	if err := dec.Decode(cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func defaultConfig() *Config {
	return &Config{
		Listen: ListenConfig{
			Address: "127.0.0.1",
			Port:    8095,
		},
		Interfaces: InterfacesConfig{
			Include: []string{".*"}, // all by default
		},
		Collector: CollectorConfig{
			IntervalSeconds: 3,
			RetentionSecs:   1800, // 30 minutes
		},
	}
}

func (c *Config) validate() error {
	if c.Listen.Port < 1 || c.Listen.Port > 65535 {
		return fmt.Errorf("invalid port: %d", c.Listen.Port)
	}
	if c.Collector.IntervalSeconds < 1 {
		return fmt.Errorf("interval_seconds must be >= 1")
	}
	if c.Collector.RetentionSecs < 60 {
		return fmt.Errorf("retention_seconds must be >= 60")
	}
	for _, pat := range c.Interfaces.Include {
		if _, err := regexp.Compile(pat); err != nil {
			return fmt.Errorf("bad interface pattern %q: %w", pat, err)
		}
	}
	for i, w := range c.Widgets {
		if w.Name == "" {
			return fmt.Errorf("widget[%d]: name is required", i)
		}
		if w.Type != WidgetTypeGraph && w.Type != WidgetTypeText {
			return fmt.Errorf("widget %q: type must be \"graph\" or \"text\"", w.Name)
		}
		if w.Command == "" {
			return fmt.Errorf("widget %q: command is required", w.Name)
		}
		if w.IntervalSeconds < 0 {
			return fmt.Errorf("widget %q: interval_seconds must be >= 0", w.Name)
		}
	}
	return nil
}

// ListenAddr returns "address:port" string.
func (c *Config) ListenAddr() string {
	return fmt.Sprintf("%s:%d", c.Listen.Address, c.Listen.Port)
}

// MatchInterface returns true if ifname matches any of the configured patterns.
func (c *Config) MatchInterface(ifname string) bool {
	for _, pat := range c.Interfaces.Include {
		re := regexp.MustCompile(pat)
		if re.MatchString(ifname) {
			return true
		}
	}
	return false
}
