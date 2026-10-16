package daemon

import (
	"bytes"
	"encoding/json"
	"os"

	"github.com/skip2/go-qrcode"
)

// PairingPayloadJSON returns the QR code content the iPhone app scans.
func (d *Daemon) PairingPayloadJSON() []byte {
	host := d.lanIP
	if host == "" {
		host = "127.0.0.1"
	}
	macName, _ := os.Hostname()
	if macName == "" {
		macName = "Windows PC"
	}
	payload := PairingPayload{
		Host:    host,
		Port:    d.cfg.Port,
		Token:   d.token,
		MacName: macName,
	}
	b, _ := json.Marshal(payload)
	return b
}

// QRCodePNG renders the pairing payload as a QR code PNG.
func (d *Daemon) QRCodePNG() ([]byte, error) {
	qr, err := qrcode.New(string(d.PairingPayloadJSON()), qrcode.Medium)
	if err != nil {
		return nil, err
	}
	qr.DisableBorder = false
	var buf bytes.Buffer
	if err := qr.Write(256, &buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
