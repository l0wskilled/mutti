package app

import (
	"crypto/x509"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/l0wskilled/mutti/internal/tlsca"
)

type fakeTrust struct {
	installed map[string]bool
	err       error
}

func (f *fakeTrust) Install(certFile string) error {
	if f.installed == nil {
		f.installed = map[string]bool{}
	}
	f.installed[certFile] = true
	return f.err
}

func (f *fakeTrust) Uninstall(certFile string) error {
	delete(f.installed, certFile)
	return f.err
}

func TestTrust(t *testing.T) {
	h := newHarness(t, webModel())
	store := &fakeTrust{}
	h.app.Trusts = store
	h.app.Host.LookPath = func(string) (string, error) { return "/usr/bin/certutil", nil }

	res, err := h.app.Trust(ctx, false)
	if err != nil || !res.Installed || !res.Browsers || !store.installed[res.CertFile] {
		t.Fatalf("install: %+v %v", res, err)
	}
	if _, err := os.Stat(res.CertFile); err != nil {
		t.Fatal(err)
	}
	res, err = h.app.Trust(ctx, true)
	if err != nil || res.Installed || store.installed[res.CertFile] {
		t.Fatalf("uninstall: %+v %v", res, err)
	}

	store.err = errors.New("sudo failed")
	if _, err := h.app.Trust(ctx, false); appErrCode(err) != CodeTrustFailed {
		t.Fatal(err)
	}
	broken := newHarness(t, webModel())
	broken.app.Trusts = store
	writeFile(t, broken.app.caDir(), "a file where a dir belongs")
	if _, err := broken.app.Trust(ctx, false); err == nil {
		t.Fatal("want error")
	}
}

func TestTrustCheck(t *testing.T) {
	h := newHarness(t, webModel())
	check := func() Check {
		r := &Report{}
		h.app.checkTrust(r)
		return r.Checks[0]
	}
	if c := check(); c.Status != CheckWarn || !strings.Contains(c.Hint, "mutti trust") {
		t.Errorf("untrusted: %+v", c)
	}

	// The check creates no CA; mutti trust or the router does.
	if _, err := os.Stat(h.app.caDir()); !os.IsNotExist(err) {
		t.Fatalf("doctor created a CA: %v", err)
	}
	if _, err := tlsca.LoadOrCreate(h.app.caDir(), []string{"localhost"}, testNow); err != nil {
		t.Fatal(err)
	}
	certPEM, _ := os.ReadFile(filepath.Join(h.app.caDir(), tlsca.CertFile))
	ca, _ := tlsca.ParseCertificate(certPEM)
	h.app.Host.SystemRoots = func() (*x509.CertPool, error) {
		p := x509.NewCertPool()
		p.AddCert(ca)
		return p, nil
	}
	if c := check(); c.Status != CheckOK {
		t.Errorf("trusted: %+v", c)
	}

	h.app.Host.SystemRoots = func() (*x509.CertPool, error) { return nil, errors.New("no roots") }
	if c := check(); c.Status != CheckWarn || c.Detail != "no roots" {
		t.Errorf("roots error: %+v", c)
	}

	// A CA whose key does not match its certificate cannot be loaded.
	other := newHarness(t, webModel())
	if _, err := tlsca.LoadOrCreate(other.app.caDir(), []string{"localhost"}, testNow); err != nil {
		t.Fatal(err)
	}
	otherKey, _ := os.ReadFile(filepath.Join(other.app.caDir(), "ca-key.pem"))
	writeFile(t, filepath.Join(h.app.caDir(), "ca-key.pem"), string(otherKey))
	h.app.Host.SystemRoots = func() (*x509.CertPool, error) { return x509.NewCertPool(), nil }
	if c := check(); c.Status != CheckWarn {
		t.Errorf("broken CA: %+v", c)
	}

	broken := newHarness(t, webModel())
	writeFile(t, broken.app.caDir(), "file")
	if _, err := broken.app.trusted(); err == nil {
		t.Error("CA dir unusable: want error")
	}
}
