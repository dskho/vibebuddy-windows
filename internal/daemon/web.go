package daemon

import (
	"embed"
	"net/http"
)

//go:embed web/index.html web/pair.html web/wstest.html
var webFS embed.FS

func (d *Daemon) serveIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	b, _ := webFS.ReadFile("web/index.html")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(b)
}

func (d *Daemon) servePair(w http.ResponseWriter, r *http.Request) {
	b, _ := webFS.ReadFile("web/pair.html")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(b)
}

func (d *Daemon) serveQR(w http.ResponseWriter, r *http.Request) {
	png, err := d.QRCodePNG()
	if err != nil {
		http.Error(w, "qr unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(png)
}
