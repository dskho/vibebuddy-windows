// Package daemon implements the vibebuddy-windows local companion daemon.
//
// It re-implements the wire protocol of the Mac `vibebuddyd` from
// semantic-craft/iOS-vibebuddy (see PROTOCOL.md): agent hooks POST lifecycle
// events to /hook, the iPhone app pairs over the LAN with a bearer token and
// receives snapshots over HTTP + WebSocket, and phone decisions flow back to
// the CLIs through the blocking /approval gate.
package daemon

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"
)

// Version is the daemon version, exposed via /health and the startup log so
// the phone's browser can confirm which build is running.
const Version = "0.2.1"

// Config holds the daemon's runtime configuration.
type Config struct {
	Port       int
	SupportDir string // %APPDATA%\vibebuddy (override with VIBEBUDDY_SUPPORT_DIR)
	HooksDir   string // where the .ps1 hook scripts live (for the dashboard)
	// AdvertiseHost overrides the IP embedded in the pairing QR code and the
	// /device response. Set it to the PC's Tailscale IP (or pass
	// VIBEBUDDY_HOST) to pair the iPhone over Tailscale instead of the LAN.
	AdvertiseHost string
}

// Daemon is the running companion: HTTP server + session state.
type Daemon struct {
	cfg      Config
	token    string
	sourceID string
	store    *Store
	hub      *Hub
	lanIP    string
	started  time.Time
}

// DefaultConfig resolves ports and directories from the environment.
func DefaultConfig() Config {
	port := 9876
	if v := os.Getenv("VIBEBUDDY_PORT"); v != "" {
		var p int
		if _, err := fmt.Sscanf(v, "%d", &p); err == nil && p > 0 {
			port = p
		}
	}
	dir := os.Getenv("VIBEBUDDY_SUPPORT_DIR")
	if dir == "" {
		if cfg, err := os.UserConfigDir(); err == nil {
			dir = filepath.Join(cfg, "vibebuddy")
		} else {
			dir = filepath.Join(os.TempDir(), "vibebuddy")
		}
	}
	return Config{Port: port, SupportDir: dir, AdvertiseHost: os.Getenv("VIBEBUDDY_HOST")}
}

// New creates a Daemon, loading (or generating) the bearer token and the
// stable source identity.
func New(cfg Config) (*Daemon, error) {
	if err := os.MkdirAll(cfg.SupportDir, 0o755); err != nil {
		return nil, fmt.Errorf("support dir: %w", err)
	}
	d := &Daemon{cfg: cfg, store: NewStore(), hub: NewHub(), started: time.Now()}
	d.store.onChange = d.broadcastSnapshot

	token, err := d.loadOrCreateSecret("token", 32)
	if err != nil {
		return nil, err
	}
	d.token = token

	sourceID, err := d.loadOrCreateSecret("source-id", 16)
	if err != nil {
		return nil, err
	}
	d.sourceID = sourceID

	d.lanIP = cfg.AdvertiseHost
	if d.lanIP == "" {
		d.lanIP = detectLANIP()
	}
	return d, nil
}

// loadOrCreateSecret reads a hex secret file, generating and persisting it
// (0600) when missing.
func (d *Daemon) loadOrCreateSecret(name string, nbytes int) (string, error) {
	path := filepath.Join(d.cfg.SupportDir, name)
	if b, err := os.ReadFile(path); err == nil {
		if s := string(b); len(s) >= 16 {
			// trim trailing whitespace/newlines
			for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r' || s[len(s)-1] == ' ') {
				s = s[:len(s)-1]
			}
			if s != "" {
				return s, nil
			}
		}
	}
	raw := make([]byte, nbytes)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("rand: %w", err)
	}
	s := hex.EncodeToString(raw)
	if err := os.WriteFile(path, []byte(s+"\n"), 0o600); err != nil {
		return "", fmt.Errorf("write %s: %w", name, err)
	}
	return s, nil
}

// detectLANIP returns the first non-loopback IPv4 address, "" if none.
func detectLANIP() string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			var ip net.IP
			switch v := a.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			if ip == nil || ip.IsLoopback() {
				continue
			}
			if v4 := ip.To4(); v4 != nil {
				return v4.String()
			}
		}
	}
	return ""
}

// nowSeconds returns the current time as Unix seconds (float64), matching the
// wire format used by the Swift client (secondsSince1970).
func nowSeconds() float64 {
	return float64(time.Now().UnixNano()) / 1e9
}
