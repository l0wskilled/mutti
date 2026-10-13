package app

import (
	"context"
	"crypto/x509"
	"path/filepath"

	"github.com/l0wskilled/mutti/internal/tlsca"
)

// TrustStore changes which certificate authorities the system and the browsers trust.
type TrustStore interface {
	Install(certFile string) error
	Uninstall(certFile string) error
}

// TrustResult describes what mutti trust changed.
type TrustResult struct {
	CertFile  string `json:"certFile"`
	Installed bool   `json:"installed"` // false: removed
	// Browsers is false when certutil is missing, so Firefox and Chromium were not updated.
	Browsers bool `json:"browsers"`
}

func (a *App) caDir() string { return filepath.Join(a.Dirs.Data, "ca") }

// Trust makes the system and the browsers trust mutti's local CA, or stops them from it.
func (a *App) Trust(_ context.Context, uninstall bool) (*TrustResult, error) {
	if _, err := tlsca.LoadOrCreate(a.caDir(), []string{a.Domain}, a.Now()); err != nil {
		return nil, err
	}
	res := &TrustResult{CertFile: filepath.Join(a.caDir(), tlsca.CertFile), Installed: !uninstall}
	_, err := a.Host.LookPath("certutil")
	res.Browsers = err == nil
	change := a.Trusts.Install
	if uninstall {
		change = a.Trusts.Uninstall
	}
	if err := change(res.CertFile); err != nil {
		return nil, &Error{Code: CodeTrustFailed, Message: "cannot change the trusted certificates", Hint: "it needs sudo; run it in a terminal", Err: err}
	}
	return res, nil
}

// trusted reports whether the system trusts certificates of mutti's CA; without a CA, nothing is.
func (a *App) trusted() (bool, error) {
	ca, ok, err := tlsca.Load(a.caDir(), []string{a.Domain})
	if err != nil || !ok {
		return false, err
	}
	roots, err := a.Host.SystemRoots()
	if err != nil {
		return false, err
	}
	host := "check." + a.Domain
	certPEM, _, _ := ca.Issue([]string{host}, a.Now()) // a loaded CA's key fits it: cannot fail
	cert, _ := tlsca.ParseCertificate(certPEM)         // just issued, always parses
	_, err = cert.Verify(x509.VerifyOptions{Roots: roots, DNSName: host, CurrentTime: a.Now()})
	return err == nil, nil
}
