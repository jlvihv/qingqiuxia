package mitm

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"strings"
	"time"
)

// Issuer mints leaf certificates for the hosts the proxy intercepts, and caches
// them.
//
// Leaf keys are per host, not per connection: minting an ECDSA key costs about
// as much as a handshake step, and a page loads dozens of connections to the
// same host. The cache is bounded and its entries expire with their
// certificates.
type Issuer struct {
	ca    *CA
	cache *leafCache
	// now is the clock, replaceable for tests.
	now func() time.Time
	// keyCurve is what leaf keys use; P-256 is what every client accepts.
	keyCurve elliptic.Curve
}

// NewIssuer returns an issuer that signs with ca.
func NewIssuer(ca *CA) *Issuer {
	return &Issuer{ca: ca, cache: newLeafCache(512), now: time.Now, keyCurve: elliptic.P256()}
}

// PEM returns the authority certificate in PEM form.
func (i *Issuer) PEM() []byte { return i.ca.PEM }

// CA returns the authority the issuer signs with.
func (i *Issuer) CA() *CA { return i.ca }

// CachedCount reports how many host certificates are cached.
func (i *Issuer) CachedCount() int { return i.cache.Len() }

// TLSCertFor returns a certificate for host, minting one when the cache has
// none. host may carry a port, which is dropped: a certificate names a host,
// not a port.
func (i *Issuer) TLSCertFor(host string) (*tls.Certificate, error) {
	name := cacheKeyFor(host)
	if name == "" {
		return nil, fmt.Errorf("mitm: no host to certify")
	}
	if cert := i.cache.get(name, i.now()); cert != nil {
		return cert, nil
	}
	cert, entry, err := i.mint(name)
	if err != nil {
		return nil, err
	}
	i.cache.put(name, entry)
	return cert, nil
}

// mint creates a certificate for one host name or address.
func (i *Issuer) mint(name string) (*tls.Certificate, *cacheEntry, error) {
	key, err := ecdsa.GenerateKey(i.keyCurve, rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("mitm: generate key for %s: %w", name, err)
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, nil, err
	}
	now := i.now()
	notAfter := now.Add(LeafLifetime)
	if !i.ca.Cert.NotAfter.IsZero() && notAfter.After(i.ca.Cert.NotAfter) {
		notAfter = i.ca.Cert.NotAfter
	}
	// The leaf carries the issuer's name, with the host as its common name. A
	// client that trusts Reqable's authority, or any other, then sees a chain
	// whose subject and issuer agree, rather than an organisation naming a
	// different tool below an issuer that names none of it.
	subject := i.ca.Cert.Subject
	subject.CommonName = name
	subject.SerialNumber = ""
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      subject,
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		// A leaf is never a CA, and clients reject one that says it is.
		BasicConstraintsValid: true,
		IsCA:                  false,
		// Firefox reads the subject key identifier and warns without it.
		SubjectKeyId: hashSubjectKeyID(&key.PublicKey),
		// An authority from elsewhere may not carry a subject key identifier;
		// the leaf still gets one, derived the same way, so that a verifier
		// can walk from the leaf to the issuer.
		AuthorityKeyId: authorityKeyID(i.ca.Cert),
	}
	if ip := net.ParseIP(name); ip != nil {
		tmpl.IPAddresses = []net.IP{ip}
	} else {
		// A wildcard name in a CONNECT is written as the host it was for, but
		// some clients ask for "*.example.com": strip the star, since a
		// certificate cannot contain it in a SAN.
		tmpl.DNSNames = []string{strings.TrimPrefix(name, "*.")}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, i.ca.Cert, &key.PublicKey, i.ca.Key)
	if err != nil {
		return nil, nil, fmt.Errorf("mitm: sign certificate for %s: %w", name, err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, nil, fmt.Errorf("mitm: parse signed certificate for %s: %w", name, err)
	}
	cert := &tls.Certificate{
		Certificate: [][]byte{der, i.ca.Cert.Raw},
		PrivateKey:  key,
		Leaf:        leaf,
	}
	return cert, &cacheEntry{cert: cert, expires: notAfter}, nil
}

// TLSConfig returns a server configuration that presents a certificate for the
// host each client asks for in the handshake.
func (i *Issuer) TLSConfig() *tls.Config {
	return &tls.Config{
		// The next protos are filled in by the caller, which knows whether it
		// speaks HTTP/2 to the client.
		GetCertificate: func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
			name := hello.ServerName
			if name == "" {
				// A client that sends no server name gets the address it
				// dialled, which is what it will check against.
				name = cacheKeyFor(hello.Conn.LocalAddr().String())
			}
			return i.TLSCertFor(name)
		},
		MinVersion: tls.VersionTLS12,
	}
}

// authorityKeyID is the issuer's subject key identifier, or, when the
// certificate has none, the one it would have been given.
func authorityKeyID(cert *x509.Certificate) []byte {
	if len(cert.SubjectKeyId) > 0 {
		return cert.SubjectKeyId
	}
	return hashSubjectKeyID(cert.PublicKey)
}
