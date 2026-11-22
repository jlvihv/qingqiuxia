// Package mitm issues the certificates that let the proxy read TLS traffic: one
// authority the client trusts, and a leaf per host, minted on demand and cached.
package mitm

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha1"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// CA is a certificate authority the proxy uses to sign the certificates it
// presents to clients.
type CA struct {
	// Cert is the authority's own certificate.
	Cert *x509.Certificate
	// Key signs leaf certificates. It is an interface because the authority
	// need not be one of ours: Reqable's, and every other MITM tool's, is RSA,
	// and refusing a key because it is not the curve we would have picked
	// would defeat the point of reusing a certificate clients already trust.
	Key crypto.Signer
	// PEM is the authority certificate in PEM form, for the client to install.
	PEM []byte
}

// cacheEntry is a certificate as crypto/tls wants it, with its expiry.
type cacheEntry struct {
	cert    *tls.Certificate
	expires time.Time
}

// LeafLifetime is how long a minted leaf certificate is valid. A leaf is only
// ever used for one intercepted connection, but browsers cache them, so it
// outlives a session.
const LeafLifetime = 24 * time.Hour

// Generate creates a new authority. It takes a moment: the key is 384-bit
// ECDSA, small enough to generate quickly and strong enough to satisfy clients.
func Generate(commonName string) (*CA, error) {
	if commonName == "" {
		commonName = "Qingqiuxia Root CA"
	}
	key, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("mitm: generate authority key: %w", err)
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:   commonName,
			Organization: []string{commonName},
		},
		// A debugging authority is installed by hand, once, so it must outlast
		// the release cycle of the tool, not a browser session.
		NotBefore: now.Add(-time.Hour),
		NotAfter:  now.AddDate(10, 0, 0),
		KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign |
			x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            0,
		MaxPathLenZero:        true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, fmt.Errorf("mitm: create authority certificate: %w", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, fmt.Errorf("mitm: parse authority certificate: %w", err)
	}
	ca := &CA{Cert: cert, Key: key}
	ca.PEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	return ca, nil
}

// Load reads an authority from a certificate and key file in PEM form.
func Load(certPath, keyPath string) (*CA, error) {
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		return nil, err
	}
	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, err
	}
	return Parse(certPEM, keyPEM)
}

// Parse builds an authority from PEM data.
func Parse(certPEM, keyPEM []byte) (*CA, error) {
	cb, _ := pem.Decode(certPEM)
	if cb == nil || cb.Type != "CERTIFICATE" {
		return nil, errors.New("mitm: no certificate in PEM data")
	}
	cert, err := x509.ParseCertificate(cb.Bytes)
	if err != nil {
		return nil, fmt.Errorf("mitm: parse certificate: %w", err)
	}
	if !cert.IsCA {
		return nil, errors.New("mitm: the certificate is not a certificate authority")
	}
	kb, _ := pem.Decode(keyPEM)
	if kb == nil {
		return nil, errors.New("mitm: no key in PEM data")
	}
	var key crypto.Signer
	switch kb.Type {
	case "EC PRIVATE KEY":
		k, err := x509.ParseECPrivateKey(kb.Bytes)
		if err != nil {
			return nil, fmt.Errorf("mitm: parse key: %w", err)
		}
		key = k
	case "RSA PRIVATE KEY":
		k, err := x509.ParsePKCS1PrivateKey(kb.Bytes)
		if err != nil {
			return nil, fmt.Errorf("mitm: parse key: %w", err)
		}
		key = k
	case "PRIVATE KEY":
		k, err := x509.ParsePKCS8PrivateKey(kb.Bytes)
		if err != nil {
			return nil, fmt.Errorf("mitm: parse key: %w", err)
		}
		signer, ok := k.(crypto.Signer)
		if !ok {
			return nil, fmt.Errorf("mitm: a %T cannot sign certificates", k)
		}
		key = signer
	default:
		return nil, fmt.Errorf("mitm: unsupported key type %q", kb.Type)
	}
	if !samePublicKey(cert.PublicKey, key.Public()) {
		return nil, errors.New("mitm: the key does not belong to the certificate")
	}
	return &CA{Cert: cert, Key: key, PEM: pem.EncodeToMemory(cb)}, nil
}

// samePublicKey reports whether two public keys are the same key, which is what
// says that a private key may sign for a certificate.
func samePublicKey(a, b crypto.PublicKey) bool {
	ab, err := x509.MarshalPKIXPublicKey(a)
	if err != nil {
		return false
	}
	bb, err := x509.MarshalPKIXPublicKey(b)
	if err != nil {
		return false
	}
	return bytes.Equal(ab, bb)
}

// LoadOrCreate loads the authority from dir, creating and saving one when the
// files are not there yet, so that a client that installed the certificate
// once keeps working across restarts.
func LoadOrCreate(dir, commonName string) (*CA, error) {
	certPath := filepath.Join(dir, "ca.crt")
	keyPath := filepath.Join(dir, "ca.key")
	if _, err := os.Stat(certPath); err == nil {
		if ca, err := Load(certPath, keyPath); err == nil {
			return ca, nil
		}
		// A broken pair is replaced rather than fatal: the tool still works,
		// and the user reinstalls the certificate.
	}
	ca, err := Generate(commonName)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	// PKCS#8 is what every key type is written as: it is the one encoding
	// Parse reads for all of them, and older EC PRIVATE KEY files still load.
	keyDER, err := x509.MarshalPKCS8PrivateKey(ca.Key)
	if err != nil {
		return nil, err
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		return nil, err
	}
	if err := os.WriteFile(certPath, ca.PEM, 0o644); err != nil {
		return nil, err
	}
	return ca, nil
}

// randomSerial makes a serial number for a certificate, chosen at random as
// the CA/Browser Forum requires of an issuer.
func randomSerial() (*big.Int, error) {
	limit := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, limit)
	if err != nil {
		return nil, fmt.Errorf("mitm: random serial: %w", err)
	}
	// Serial numbers must be positive.
	return serial.Add(serial, big.NewInt(1)), nil
}

// leafCache keeps minted certificates per host. Its size is bounded, since a
// page can reference hosts without end; the oldest entry goes first.
type leafCache struct {
	mu    sync.Mutex
	certs map[string]*cacheEntry
	order []string
	limit int
}

func newLeafCache(limit int) *leafCache {
	if limit <= 0 {
		limit = 512
	}
	return &leafCache{certs: make(map[string]*cacheEntry, limit), limit: limit}
}

// get returns a cached certificate for host while it is valid.
func (c *leafCache) get(host string, now time.Time) *tls.Certificate {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.certs[host]
	if !ok {
		return nil
	}
	// Re-mint a little before expiry, so a connection never starts with a
	// certificate that has just lapsed.
	if now.Add(time.Minute).After(e.expires) {
		delete(c.certs, host)
		return nil
	}
	return e.cert
}

func (c *leafCache) put(host string, e *cacheEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.certs[host]; !exists {
		if len(c.order) >= c.limit {
			oldest := c.order[0]
			c.order = c.order[1:]
			delete(c.certs, oldest)
		}
		c.order = append(c.order, host)
	}
	c.certs[host] = e
}

// Len reports how many certificates are cached.
func (c *leafCache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.certs)
}

// cacheKeyFor names the host a connection asked for.
func cacheKeyFor(host string) string {
	h, _, err := net.SplitHostPort(host)
	if err == nil {
		host = h
	}
	return host
}

// hashSubjectKeyID derives a stable subject key identifier from a public key:
// the SHA-1 of its SubjectPublicKeyInfo. Nothing verifies the value; it is
// there because clients expect a certificate to carry one.
func hashSubjectKeyID(key crypto.PublicKey) []byte {
	raw, err := x509.MarshalPKIXPublicKey(key)
	if err != nil {
		return nil
	}
	sum := sha1.Sum(raw)
	return sum[:]
}
