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
	version = "1.0.1"
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

	col, err := collector.New(cfg)
	if err != nil {
		log.Fatalf("collector init error: %v", err)
	}

	wr := widget.New(cfg)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go col.Run(ctx)
	go wr.Run(ctx)

	srv := api.New(cfg, col, wr)
	go func() {
		if err := srv.ListenAndServe(); err != nil {
			log.Printf("[main] server stopped: %v", err)
			cancel()
		}
	}()

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
