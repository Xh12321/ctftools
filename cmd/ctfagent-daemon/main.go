package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/Xh12321/ctftools/internal/daemon"
)

func main() {
	var (
		dataDir = flag.String("data-dir", envOr("CTF_AGENT_DATA_DIR", ""), "daemon data directory")
		addr    = flag.String("addr", envOr("CTF_DAEMON_ADDR", "127.0.0.1:7521"), "listen address")
		token   = flag.String("token", envOr("CTF_DAEMON_TOKEN", ""), "API bearer token (auto-generated if empty)")
	)
	flag.Parse()

	cfg := daemon.DefaultConfig()
	if *dataDir != "" {
		cfg.DataDir = *dataDir
	}
	if *addr != "" {
		cfg.Addr = *addr
	}
	if *token != "" {
		cfg.Token = *token
	}
	if cfg.DataDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			log.Fatal(err)
		}
		cfg.DataDir = filepath.Join(home, ".ctf-btfly")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := daemon.Start(ctx, cfg); err != nil {
		fmt.Fprintf(os.Stderr, "ctfagent-daemon: %v\n", err)
		os.Exit(1)
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
