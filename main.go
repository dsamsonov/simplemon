package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"

	"simplemon/internal/api"
	"simplemon/internal/collector"
	"simplemon/internal/config"
	"simplemon/internal/widget"
)

var (
	version = "1.0.0"
	cfgPath = flag.String("config", config.DefaultConfigPath, "path to configuration file")
	ver     = flag.Bool("version", false, "print version and exit")
)

func main() {
	flag.Parse()

	if *ver {
		log.Printf("simplemon version %s", version)
		os.Exit(0)
	}

	log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds | log.Lshortfile)
	log.Printf("simplemon %s starting, config: %s", version, *cfgPath)

	// Load configuration
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatalf("config error: %v", err)
	}
	log.Printf("listen: %s, interval: %ds, retention: %ds",
		cfg.ListenAddr(),
		cfg.Collector.IntervalSeconds,
		cfg.Collector.RetentionSecs,
	)
	if len(cfg.Widgets) > 0 {
		log.Printf("widgets: %d configured", len(cfg.Widgets))
	}

	// Create collector
	col, err := collector.New(cfg)
	if err != nil {
		log.Fatalf("collector init error: %v", err)
	}

	// Create widget runner
	wr := widget.New(cfg)

	// Context for graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start collector loop
	go col.Run(ctx)

	// Start widget runner (no-op if no widgets configured)
	go wr.Run(ctx)

	// Start API server
	srv := api.New(cfg, col, wr)
	go func() {
		if err := srv.ListenAndServe(); err != nil {
			log.Printf("[main] server stopped: %v", err)
			cancel()
		}
	}()

	// Handle signals
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)

	for {
		sig := <-sigCh
		switch sig {
		case syscall.SIGHUP:
			log.Println("[main] SIGHUP received – reloading config is not yet implemented, ignoring")
		case syscall.SIGINT, syscall.SIGTERM:
			log.Printf("[main] signal %s received, shutting down", sig)
			cancel()
			srv.Shutdown()
			log.Println("[main] bye")
			os.Exit(0)
		}
	}
}
