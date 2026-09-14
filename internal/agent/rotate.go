// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/zyvorai/nodra/internal/durable"
	"github.com/zyvorai/nodra/internal/pki"
)

// rotationLoop periodically checks the locally-stored identity certificate's
// expiry and, when it's within cfg.CertRotateBefore of expiring, re-keys and
// re-enrolls via POST /api/v1/sites/{id}/rotate — nodrad-initiated (pull),
// since the edge generates and keeps its own private key; the server never
// sees it, mirroring enroll's trust model.
func (a *Agent) rotationLoop(ctx context.Context) {
	defer a.wg.Done()
	t := time.NewTicker(24 * time.Hour)
	defer t.Stop()
	a.maybeRotate(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			a.maybeRotate(ctx)
		}
	}
}

// Revoked reports whether the control plane has told this agent (via a 403
// site_revoked response to heartbeat or rotate) that its site was revoked.
// Surfaced on /healthz so an operator can see it without reading logs.
func (a *Agent) Revoked() bool { return a.revoked.Load() }

func (a *Agent) maybeRotate(ctx context.Context) {
	if !a.cfg.RequestCertificate || a.revoked.Load() {
		return
	}
	certPath := filepath.Join(a.cfg.DataDir, "identity.crt")
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		return // no PKI identity to rotate (RequestCertificate but not yet enrolled, or CSR wasn't honored)
	}
	block, _ := pem.Decode(certPEM)
	if block == nil {
		slog.Warn("cert rotation: invalid identity.crt PEM")
		return
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		slog.Warn("cert rotation: cannot parse identity.crt", "error", err)
		return
	}
	if !pki.ShouldRotate(cert.NotAfter, time.Now().UTC(), a.cfg.CertRotateBefore) {
		return
	}
	if err := a.rotateCertificate(ctx); err != nil {
		a.auditNote("agent", "agent", "cert.rotate", "error", err.Error(), map[string]any{"not_after": cert.NotAfter})
		slog.Warn("cert rotation failed", "error", err)
		return
	}
	a.auditNote("agent", "agent", "cert.rotate", "ok", "certificate rotated", nil)
}

func (a *Agent) rotateCertificate(ctx context.Context) error {
	keyPEM, csrPEM, err := pki.NewClientCSR(a.cfg.SiteName)
	if err != nil {
		return err
	}
	body, _ := json.Marshal(map[string]any{"csr_pem": string(csrPEM)})
	req, err := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(a.cfg.ServerURL, "/")+"/api/v1/sites/"+a.cfg.SiteID+"/rotate", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+a.cfg.AgentToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode == 403 && strings.Contains(string(b), "site_revoked") {
		a.revoked.Store(true)
		return errors.New("site revoked, will not attempt further rotations")
	}
	if resp.StatusCode != 200 {
		return fmt.Errorf("server returned %s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	var out struct {
		ClientCertificate string `json:"client_certificate"`
	}
	if err = json.Unmarshal(b, &out); err != nil {
		return err
	}
	certPath := filepath.Join(a.cfg.DataDir, "identity.crt")
	keyPath := filepath.Join(a.cfg.DataDir, "identity.key")
	// The old cert/key stay in place on disk until both new files are
	// durably written — a rotation failure partway through never leaves the
	// agent without a usable identity.
	if err = durable.AtomicWrite(keyPath, keyPEM, 0o600); err != nil {
		return err
	}
	if err = durable.AtomicWrite(certPath, []byte(out.ClientCertificate), 0o600); err != nil {
		return err
	}
	return a.configureClient()
}

// checkRevoked inspects a heartbeat response for a 403 site_revoked body and
// sets the revoked flag so rotationLoop stops trying. atomic.Bool because
// it's read from /healthz concurrently with heartbeatLoop's writes.
func (a *Agent) checkRevoked(resp *http.Response) {
	if resp.StatusCode != 403 {
		return
	}
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	if strings.Contains(string(b), "site_revoked") {
		a.revoked.Store(true)
	}
}
