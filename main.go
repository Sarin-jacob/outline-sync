// Command outline-sync exports Outline collections as Markdown and pushes
// them to git repositories on a schedule.
package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"
	_ "time/tzdata" // lets TZ=Europe/Berlin etc. work in minimal images
)

func main() {
	log.SetFlags(log.Ldate | log.Ltime)

	configPath := flag.String("config", envOr("CONFIG_PATH", "config.yml"), "path to config file")
	reposDir := flag.String("repos", envOr("REPOS_DIR", "repos"), "directory holding the local git repositories")
	once := flag.Bool("once", false, "run a single sync cycle and exit")
	flag.Parse()

	log.Println("Starting outline-sync")
	cfg, err := LoadConfig(*configPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	s := NewSyncer(cfg, *reposDir)
	for {
		s.RunCycle(ctx)
		if *once || ctx.Err() != nil {
			break
		}
		log.Printf("Sleeping for %s...", cfg.Interval())
		select {
		case <-ctx.Done():
		case <-time.After(cfg.Interval()):
		}
		if ctx.Err() != nil {
			break
		}
	}
	log.Println("Stopped")
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
