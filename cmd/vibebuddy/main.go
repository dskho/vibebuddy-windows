// Command vibebuddy is the Windows companion daemon for AI coding agents.
//
// It listens on :9876 (VIBEBUDDY_PORT), receives hook events from the CLIs,
// and serves the iPhone app over the LAN using the same wire protocol as the
// Mac vibebuddyd (see PROTOCOL.md).
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/dskho/vibebuddy-windows/internal/daemon"
)

func main() {
	cfg := daemon.DefaultConfig()
	flag.IntVar(&cfg.Port, "port", cfg.Port, "listen port (or VIBEBUDDY_PORT)")
	flag.StringVar(&cfg.SupportDir, "support-dir", cfg.SupportDir, "state directory (or VIBEBUDDY_SUPPORT_DIR)")
	flag.StringVar(&cfg.AdvertiseHost, "advertise-host", cfg.AdvertiseHost, "IP embedded in pairing QR (or VIBEBUDDY_HOST, e.g. Tailscale IP)")
	flag.Parse()

	d, err := daemon.New(cfg)
	if err != nil {
		log.Fatalf("init: %v", err)
	}

	fmt.Printf("vibebuddy-windows daemon v%s\n", daemon.Version)
	fmt.Printf("  dashboard : http://127.0.0.1:%d/\n", cfg.Port)
	fmt.Printf("  pairing   : http://127.0.0.1:%d/pair\n", cfg.Port)
	fmt.Printf("  support   : %s\n", cfg.SupportDir)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := d.Serve(ctx); err != nil {
		log.Fatalf("serve: %v", err)
	}
}
