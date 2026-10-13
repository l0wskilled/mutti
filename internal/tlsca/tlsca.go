// Package tlsca runs mutti's local certificate authority: one root per machine, one leaf
// certificate per instance. The root is name-constrained to mutti's base domains, so even a
// leaked root key cannot vouch for real sites.
package tlsca

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"math/big"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

const (
	caValidity   = 10 * 365 * 24 * time.Hour
	leafValidity = 397 * 24 * time.Hour // the longest lifetime every browser accepts
	renewBefore  = 30 * 24 * time.Hour
)

// File names inside the CA directory.
const (
	CertFile = "ca.pem"
	keyFile  = "ca-key.pem"
)

// CA is the local root.
type CA struct {
	Cert    *x509.Certificate
	CertPEM []byte
	key     *ecdsa.PrivateKey
}

// LoadOrCreate returns the CA in dir, creating it for the given domains (name constraints) if
// there is none. A CA that does not cover the domains is an error, never silently replaced:
// browsers and the system already trust it.
func LoadOrCreate(dir string, domains []string, now time.Time) (*CA, error) {
	ca, ok, err := Load(dir, domains)
	if err != nil || ok {
		return ca, err
	}
	return create(dir, domains, now)
}

// Load returns the CA in dir; ok is false when there is none.
func Load(dir string, domains []string) (ca *CA, ok bool, err error) {
	certPEM, err := os.ReadFile(filepath.Join(dir, CertFile))
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, false, nil
	case err != nil:
		return nil, false, err
	}
	keyPEM, err := os.ReadFile(filepath.Join(dir, keyFile))
	if err != nil {
		return nil, false, err
	}
	ca, err = parse(certPEM, keyPEM)
	if err != nil {
		return nil, false, fmt.Errorf("%s: %w", dir, err)
	}
	for _, d := range domains {
		if !slices.Contains(ca.Cert.PermittedDNSDomains, d) {
			return nil, false, fmt.Errorf("the local CA in %s does not cover %q; delete the directory and run mutti trust again", dir, d)
		}
	}
	return ca, true, nil
}

func create(dir string, domains []string, now time.Time) (*CA, error) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader) // reads crypto/rand, cannot fail
	tmpl := &x509.Certificate{
		SerialNumber:                serial(),
		Subject:                     pkix.Name{Organization: []string{"mutti local CA"}, CommonName: "mutti local CA " + now.Format("2006-01-02")},
		NotBefore:                   now.Add(-time.Hour),
		NotAfter:                    now.Add(caValidity),
		KeyUsage:                    x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid:       true,
		IsCA:                        true,
		MaxPathLenZero:              true,
		PermittedDNSDomainsCritical: true,
		PermittedDNSDomains:         domains,
	}
	// Self-signed with a key made just now: signing only fails for a mismatched key pair.
	certPEM, keyPEM, _ := sign(tmpl, tmpl, key, key)

	// Written next to dir and moved into place in one step, so two mutti processes creating
	// the CA at once cannot leave one's key next to the other's certificate: the second rename
	// fails, and that process takes the first one's CA.
	if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		return nil, err
	}
	tmp, err := os.MkdirTemp(filepath.Dir(dir), ".ca-*")
	if err == nil {
		defer func() { _ = os.RemoveAll(tmp) }()
		err = errors.Join(
			os.WriteFile(filepath.Join(tmp, keyFile), keyPEM, 0o600),
			os.WriteFile(filepath.Join(tmp, CertFile), certPEM, 0o644), //nolint:gosec // the root certificate is public
		)
	}
	if err != nil {
		return nil, err
	}
	if err := os.Rename(tmp, dir); err != nil {
		if ca, ok, loadErr := Load(dir, domains); loadErr == nil && ok {
			return ca, nil
		}
		return nil, err
	}
	return parse(certPEM, keyPEM)
}

func parse(certPEM, keyPEM []byte) (*CA, error) {
	cert, err := parseCert(certPEM)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(keyPEM)
	if block == nil {
		return nil, errors.New("CA key is not PEM")
	}
	key, err := x509.ParseECPrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	if !key.PublicKey.Equal(cert.PublicKey) {
		return nil, errors.New("CA key and certificate do not belong together")
	}
	return &CA{Cert: cert, CertPEM: certPEM, key: key}, nil
}

// ParseCertificate reads one PEM certificate.
func ParseCertificate(certPEM []byte) (*x509.Certificate, error) { return parseCert(certPEM) }

func parseCert(certPEM []byte) (*x509.Certificate, error) {
	block, _ := pem.Decode(certPEM)
	if block == nil {
		return nil, errors.New("certificate is not PEM")
	}
	return x509.ParseCertificate(block.Bytes)
}

func serial() *big.Int {
	n, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128)) // crypto/rand cannot fail
	return n
}

// Issue signs a server certificate for hosts.
func (ca *CA) Issue(hosts []string, now time.Time) (certPEM, keyPEM []byte, err error) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{
		SerialNumber: serial(),
		Subject:      pkix.Name{Organization: []string{"mutti"}, CommonName: hosts[0]},
		DNSNames:     hosts,
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(leafValidity),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	return sign(tmpl, ca.Cert, key, ca.key)
}

// sign creates the certificate for key, signed by parent's signer, and PEM-encodes both. It
// fails when signer does not belong to parent, e.g. for a CA whose files were mixed up.
func sign(tmpl, parent *x509.Certificate, key, signer *ecdsa.PrivateKey) (certPEM, keyPEM []byte, err error) {
	der, err := x509.CreateCertificate(rand.Reader, tmpl, parent, &key.PublicKey, signer)
	if err != nil {
		return nil, nil, err
	}
	keyDER, _ := x509.MarshalECPrivateKey(key) // a valid P-256 key always marshals
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), nil
}

// Valid reports whether certPEM is a certificate of this CA for exactly hosts that does not
// need renewal yet.
func (ca *CA) Valid(certPEM []byte, hosts []string, now time.Time) bool {
	cert, err := parseCert(certPEM)
	if err != nil {
		return false
	}
	if !bytes.Equal(cert.AuthorityKeyId, ca.Cert.SubjectKeyId) || now.Add(renewBefore).After(cert.NotAfter) {
		return false
	}
	return slices.Equal(cert.DNSNames, hosts)
}

// Covers reports whether host lies under one of the CA's base domains.
func (ca *CA) Covers(host string) bool {
	for _, d := range ca.Cert.PermittedDNSDomains {
		if host == d || strings.HasSuffix(host, "."+d) {
			return true
		}
	}
	return false
}
