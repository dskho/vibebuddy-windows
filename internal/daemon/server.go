package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strconv"
	"time"
)

// Handler builds the full HTTP route table.
func (d *Daemon) Handler() http.Handler {
	mux := http.NewServeMux()

	// Public: health probe (the only unauthenticated phone endpoint).
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "version": Version})
	})

	// Local dashboard (HTML carries no secrets).
	mux.HandleFunc("/", d.serveIndex)
	mux.HandleFunc("/pair", d.servePair)

	// Secrets only ever leave the machine to loopback clients.
	mux.HandleFunc("/qr.png", d.loopbackOnly(d.serveQR))
	mux.HandleFunc("/api/local/snapshot", d.loopbackOnly(d.serveLocalSnapshot))
	mux.HandleFunc("/api/local/pair-info", d.loopbackOnly(d.servePairInfo))
	mux.HandleFunc("/api/local/decision", d.loopbackOnly(d.serveLocalDecision))

	// Hook endpoints: bearer header or ?token= (for hooks that cannot set headers).
	hookAuth := d.flexAuth
	mux.Handle("/hook", hookAuth(http.HandlerFunc(d.serveHook)))
	mux.Handle("/approval", hookAuth(http.HandlerFunc(d.serveApproval)))
	mux.Handle("/statusline", hookAuth(http.HandlerFunc(d.serveStatusline)))
	mux.Handle("/terminal", hookAuth(http.HandlerFunc(d.serveTerminal)))

	// Phone endpoints: bearer auth.
	phoneAuth := d.bearerAuth
	mux.Handle("/device", phoneAuth(http.HandlerFunc(d.serveDevice)))
	mux.Handle("/snapshot", phoneAuth(http.HandlerFunc(d.serveSnapshot)))
	mux.Handle("/ws", phoneAuth(http.HandlerFunc(d.serveWS)))
	mux.Handle("/decision", phoneAuth(http.HandlerFunc(d.serveDecision)))
	mux.Handle("/answer", phoneAuth(http.HandlerFunc(d.serveAnswer)))
	mux.Handle("/acknowledge", phoneAuth(http.HandlerFunc(d.serveAcknowledge)))
	mux.Handle("/acknowledge-wait", phoneAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})))
	mux.Handle("/attention", phoneAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})))
	mux.Handle("/notified", phoneAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})))
	mux.Handle("/activity", phoneAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})))
	mux.Handle("/jump", phoneAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"outcome": "unsupported"})
	})))
	mux.Handle("/dispatch", phoneAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "dispatch not supported on Windows yet"})
	})))
	mux.Handle("/connection-sync", phoneAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent) // no proposal
	})))
	mux.Handle("/connection-sync/receipt", phoneAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})))

	// Graceful-degradation endpoints the phone tolerates as 404/503.
	for _, p := range []string{"/task-read-capabilities", "/task-read", "/history", "/changes", "/completion", "/recent-output"} {
		mux.Handle(p, phoneAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.NotFound(w, r)
		})))
	}
	mux.Handle("/content-style", phoneAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	})))
	mux.Handle("/presentation", phoneAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unavailable", http.StatusConflict)
	})))

	return d.loggingMiddleware(mux)
}

// statusRecorder captures the response status for logging.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

// loggingMiddleware logs every request (never secrets): method, path,
// remote IP, response status, and whether a bearer <redacted> was present.
func (d *Daemon) loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		auth := "no-auth"
		if r.Header.Get("Authorization") != "" {
			auth = "auth"
		}
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			host = r.RemoteAddr
		}
		log.Printf("%s %s from %s -> %d (%s)", r.Method, r.URL.Path, host, rec.status, auth)
	})
}

// --- auth ---

func (d *Daemon) checkBearer(r *http.Request) bool {
	if d.token == "" {
		return false
	}
	return r.Header.Get("Authorization") == "Bearer "+d.token
}

// bearerAuth requires the Authorization header (phone + /ws).
func (d *Daemon) bearerAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !d.checkBearer(r) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// flexAuth allows the bearer header or a ?token= query parameter, for hooks
// that cannot set headers.
func (d *Daemon) flexAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if d.checkBearer(r) {
			next.ServeHTTP(w, r)
			return
		}
		if q := r.URL.Query().Get("token"); q != "" && q == d.token {
			next.ServeHTTP(w, r)
			return
		}
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	})
}

// loopbackOnly restricts secret-bearing local endpoints to this machine.
func (d *Daemon) loopbackOnly(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			host = r.RemoteAddr
		}
		if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next(w, r)
	}
}

// --- phone endpoints ---

func (d *Daemon) serveDevice(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	var reg DeviceRegistration
	_ = json.Unmarshal(body, &reg) // also accepts a bare APNs token string
	w.WriteHeader(http.StatusOK)
}

func (d *Daemon) serveSnapshot(w http.ResponseWriter, r *http.Request) {
	// quotaProviders filter is accepted and ignored in MVP (we return all).
	writeJSON(w, http.StatusOK, d.store.Snapshot(d.sourceID))
}

func (d *Daemon) serveDecision(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	var req DecisionRequest
	if err := json.Unmarshal(body, &req); err != nil || req.RequestID == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	status, _ := d.store.ResolveApproval(req)
	d.broadcastSnapshot()
	w.WriteHeader(status) // no body: the phone reads the status code
}

func (d *Daemon) serveAnswer(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	var req AnswerRequest
	if err := json.Unmarshal(body, &req); err != nil || req.RequestID == "" || req.SessionID == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	status := d.store.AnswerIntent(req)
	d.broadcastSnapshot()
	httpStatus := http.StatusOK
	switch status {
	case "unknown":
		httpStatus = http.StatusServiceUnavailable
	case "refused", "failed":
		httpStatus = http.StatusConflict
	}
	writeJSON(w, httpStatus, AnswerResponse{RequestID: req.RequestID, Status: status})
}

func (d *Daemon) serveAcknowledge(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	var req AcknowledgeRequest
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	outcome := "accepted"
	if req.SourceID != "" && req.SourceID != d.sourceID {
		outcome = "sourceMismatch"
	} else {
		outcome = d.store.Acknowledge(d.sourceID, req.SessionID)
	}
	d.broadcastSnapshot()
	writeJSON(w, http.StatusOK, AcknowledgeResponse{Outcome: outcome})
}

// --- hook endpoints ---

func readBody(r *http.Request) []byte {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	return body
}

func (d *Daemon) serveHook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	agent := r.URL.Query().Get("agent")
	if agent == "" {
		agent = "claude"
	}
	d.ApplyHookEvent(agent, readBody(r))
	w.WriteHeader(http.StatusOK)
}

func (d *Daemon) serveApproval(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	agent := r.URL.Query().Get("agent")
	hold := 25
	if h := r.URL.Query().Get("hold"); h != "" {
		if n, err := strconv.Atoi(h); err == nil {
			hold = n
		}
	}
	body := d.WaitApproval(r.Context(), agent, readBody(r), hold)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(body)) // empty body = silence: the CLI falls back to its own prompt
}

func (d *Daemon) serveStatusline(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	agent := r.URL.Query().Get("agent")
	if agent == "" {
		agent = "claude"
	}
	d.store.RecordStatusline(agent, string(readBody(r)))
	w.WriteHeader(http.StatusOK)
}

func (d *Daemon) serveTerminal(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(r.Body, 1<<20))
	w.WriteHeader(http.StatusOK)
}

// --- local dashboard endpoints (loopback only) ---

func (d *Daemon) serveLocalSnapshot(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, d.store.Snapshot(d.sourceID))
}

func (d *Daemon) serveLocalDecision(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	var req DecisionRequest
	if err := json.Unmarshal(body, &req); err != nil || req.RequestID == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	status, _ := d.store.ResolveApproval(req)
	d.broadcastSnapshot()
	w.WriteHeader(status)
}

func (d *Daemon) servePairInfo(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"host": d.lanIP, "port": d.cfg.Port})
}

// --- server lifecycle ---

// Serve starts the HTTP server and blocks until ctx is cancelled.
func (d *Daemon) Serve(ctx context.Context) error {
	srv := &http.Server{
		Addr:              fmt.Sprintf(":%d", d.cfg.Port),
		Handler:           d.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
	}()
	log.Printf("vibebuddy-windows v%s listening on :%d (support dir %s, LAN %s)", Version, d.cfg.Port, d.cfg.SupportDir, d.lanIP)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}
