package tlsca

import (
	"crypto/tls"
	"crypto/x509"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

var now = time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)

func newCA(t *testing.T, domains ...string) (*CA, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "ca")
	ca, err := LoadOrCreate(dir, domains, now)
	if err != nil {
		t.Fatal(err)
	}
	return ca, dir
}

func verify(t *testing.T, ca *CA, certPEM []byte, host string, at time.Time) error {
	t.Helper()
	cert, err := parseCert(certPEM)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(ca.Cert)
	_, err = cert.Verify(x509.VerifyOptions{Roots: roots, DNSName: host, CurrentTime: at})
	return err
}

func TestCreateAndReload(t *testing.T) {
	ca, dir := newCA(t, "localhost")
	if !ca.Cert.IsCA || !ca.Cert.MaxPathLenZero || ca.Cert.PermittedDNSDomains[0] != "localhost" {
		t.Fatalf("root %+v", ca.Cert)
	}
	if fi, _ := os.Stat(filepath.Join(dir, keyFile)); fi.Mode().Perm() != 0o600 {
		t.Errorf("key mode %v", fi.Mode())
	}
	again, err := LoadOrCreate(dir, []string{"localhost"}, now.Add(time.Hour))
	if err != nil || !again.Cert.Equal(ca.Cert) {
		t.Fatalf("reload: %v", err)
	}
}

func TestIssueVerifiesForInstanceHosts(t *testing.T) {
	ca, _ := newCA(t, "localhost")
	hosts := []string{"feature.shop.localhost", "*.feature.shop.localhost"}
	certPEM, keyPEM, err := ca.Issue(hosts, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tls.X509KeyPair(certPEM, keyPEM); err != nil {
		t.Fatalf("key pair: %v", err)
	}
	for _, host := range []string{"feature.shop.localhost", "api.feature.shop.localhost"} {
		if err := verify(t, ca, certPEM, host, now); err != nil {
			t.Errorf("%s: %v", host, err)
		}
	}
	if err := verify(t, ca, certPEM, "other.shop.localhost", now); err == nil {
		t.Error("cert must not cover a sibling instance")
	}
}

func TestNameConstraintsBlockForeignDomains(t *testing.T) {
	ca, _ := newCA(t, "localhost")
	certPEM, _, err := ca.Issue([]string{"www.example.com"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := verify(t, ca, certPEM, "www.example.com", now); err == nil || !strings.Contains(err.Error(), "constraint") {
		t.Fatalf("a foreign domain must fail verification, got %v", err)
	}
	if ca.Covers("www.example.com") || !ca.Covers("a.localhost") || !ca.Covers("localhost") {
		t.Error("Covers")
	}
}

func TestValid(t *testing.T) {
	ca, _ := newCA(t, "localhost")
	hosts := []string{"shop.localhost", "*.shop.localhost"}
	certPEM, _, _ := ca.Issue(hosts, now)

	if !ca.Valid(certPEM, hosts, now) {
		t.Error("fresh cert must be valid")
	}
	if ca.Valid(certPEM, hosts, now.Add(leafValidity-renewBefore+time.Hour)) {
		t.Error("cert close to expiry must be renewed")
	}
	if ca.Valid(certPEM, []string{"other.localhost"}, now) {
		t.Error("cert for other hosts must be renewed")
	}
	if ca.Valid([]byte("garbage"), hosts, now) {
		t.Error("garbage is not valid")
	}
	other, _ := newCA(t, "localhost")
	if other.Valid(certPEM, hosts, now) {
		t.Error("cert of another CA must be renewed")
	}
}

func TestLoadErrors(t *testing.T) {
	_, dir := newCA(t, "localhost")

	if _, err := LoadOrCreate(dir, []string{"localhost", "test"}, now); err == nil || !strings.Contains(err.Error(), `does not cover "test"`) {
		t.Errorf("uncovered domain: %v", err)
	}

	broken := t.TempDir()
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(broken, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(CertFile, "x")
	if _, err := LoadOrCreate(broken, nil, now); err == nil {
		t.Error("missing key: want error")
	}
	write(keyFile, "x")
	if _, err := LoadOrCreate(broken, nil, now); err == nil {
		t.Error("garbage cert: want error")
	}
	good, _ := os.ReadFile(filepath.Join(dir, CertFile))
	write(CertFile, string(good))
	if _, err := LoadOrCreate(broken, nil, now); err == nil || !strings.Contains(err.Error(), "not PEM") {
		t.Errorf("garbage key: %v", err)
	}
	write(keyFile, "-----BEGIN EC PRIVATE KEY-----\nAAAA\n-----END EC PRIVATE KEY-----\n")
	if _, err := LoadOrCreate(broken, nil, now); err == nil {
		t.Error("bad key: want error")
	}
	write(CertFile, "-----BEGIN CERTIFICATE-----\nAAAA\n-----END CERTIFICATE-----\n")
	if _, err := LoadOrCreate(broken, nil, now); err == nil {
		t.Error("bad cert DER: want error")
	}
}

func TestCreateErrors(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores permissions")
	}
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreate(filepath.Join(file, "ca"), nil, now); err == nil {
		t.Error("dir below a file: want error")
	}

	ro := filepath.Join(t.TempDir(), "ro")
	if err := os.MkdirAll(ro, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(ro, 0o700) })
	if _, err := LoadOrCreate(ro, nil, now); err == nil {
		t.Error("read-only dir: want error")
	}

	// Key written, certificate not: a directory squats on the certificate's name after the
	// read of ca.pem failed with "is a directory" -> that is a read error, not "missing".
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, CertFile), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreate(dir, nil, now); err == nil {
		t.Error("cert path is a dir: want error")
	}

	missing := filepath.Join(ro, "new")
	if _, err := LoadOrCreate(missing, nil, now); err == nil {
		t.Error("dir not creatable: want error")
	}

	// The certificate path is a dangling symlink: reading says "missing", and the new CA cannot
	// take the place of the directory holding it.
	dangling := t.TempDir()
	if err := os.Symlink(filepath.Join(dangling, "nowhere", "x"), filepath.Join(dangling, CertFile)); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreate(dangling, nil, now); err == nil {
		t.Error("cert not writable: want error")
	}

	if _, err := LoadOrCreate(filepath.Join(ro, "new", "ca"), nil, now); err == nil {
		t.Error("parent not creatable: want error")
	}
}

func TestMismatchedCAFiles(t *testing.T) {
	_, a := newCA(t, "localhost")
	_, b := newCA(t, "localhost")
	keyB, _ := os.ReadFile(filepath.Join(b, keyFile))
	if err := os.WriteFile(filepath.Join(a, keyFile), keyB, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreate(a, []string{"localhost"}, now); err == nil || !strings.Contains(err.Error(), "do not belong together") {
		t.Fatalf("mixed files: %v", err)
	}
	// Issuing with a key of another CA fails too.
	caB, _, _ := Load(b, nil)
	other, _ := create(filepath.Join(t.TempDir(), "other"), []string{"localhost"}, now)
	mixed := &CA{Cert: caB.Cert, CertPEM: caB.CertPEM, key: other.key}
	if _, _, err := mixed.Issue([]string{"x.localhost"}, now); err == nil {
		t.Fatal("want error for a key that does not match the certificate")
	}
}

func TestConcurrentCreatorsAgree(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ca")
	cas := make([]*CA, 8)
	errs := make([]error, 8)
	var wg sync.WaitGroup
	for i := range cas {
		wg.Go(func() { cas[i], errs[i] = LoadOrCreate(dir, []string{"localhost"}, now) })
	}
	wg.Wait()
	for i := range cas {
		if errs[i] != nil || !cas[i].Cert.Equal(cas[0].Cert) {
			t.Fatalf("creator %d: %v", i, errs[i])
		}
	}
	if ca, ok, err := Load(dir, []string{"localhost"}); err != nil || !ok || !ca.Cert.Equal(cas[0].Cert) {
		t.Fatalf("on disk: %v %v", ok, err)
	}
	if entries, _ := os.ReadDir(filepath.Dir(dir)); len(entries) != 1 {
		t.Errorf("leftovers: %v", entries)
	}
	if _, ok, err := Load(filepath.Join(t.TempDir(), "none"), nil); ok || err != nil {
		t.Errorf("no CA: %v %v", ok, err)
	}
}

func TestParseCertificate(t *testing.T) {
	ca, _ := newCA(t, "localhost")
	if c, err := ParseCertificate(ca.CertPEM); err != nil || !c.Equal(ca.Cert) {
		t.Fatalf("%v", err)
	}
}
