package mitm_test

import (
	"crypto/tls"
	"crypto/x509"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jlvihv/qingqiuxia/internal/mitm"
)

func TestGenerateAndSign(t *testing.T) {
	ca, err := mitm.Generate("Test CA")
	if err != nil {
		t.Fatal(err)
	}
	if !ca.Cert.IsCA {
		t.Error("the authority is not a CA")
	}
	// Ten years, so that the certificate outlives a release cycle.
	if ca.Cert.NotAfter.Before(time.Now().AddDate(9, 0, 0)) {
		t.Errorf("the authority expires at %v", ca.Cert.NotAfter)
	}

	issuer := mitm.NewIssuer(ca)
	cert, err := issuer.TLSCertFor("api.example.com:443")
	if err != nil {
		t.Fatal(err)
	}
	if cert.Leaf == nil {
		t.Fatal("no parsed leaf")
	}
	if got := cert.Leaf.Subject.CommonName; got != "api.example.com" {
		t.Errorf("common name = %q, want the host without its port", got)
	}
	if len(cert.Leaf.DNSNames) != 1 || cert.Leaf.DNSNames[0] != "api.example.com" {
		t.Errorf("DNS names = %v", cert.Leaf.DNSNames)
	}

	// The leaf must verify against the authority, through a chain.
	roots := x509.NewCertPool()
	roots.AddCert(ca.Cert)
	if _, err := cert.Leaf.Verify(x509.VerifyOptions{
		Roots:     roots,
		DNSName:   "api.example.com",
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}); err != nil {
		t.Errorf("the leaf does not verify: %v", err)
	}
}

func TestIPHost(t *testing.T) {
	ca, _ := mitm.Generate("Test CA")
	issuer := mitm.NewIssuer(ca)
	cert, err := issuer.TLSCertFor("127.0.0.1:8443")
	if err != nil {
		t.Fatal(err)
	}
	if len(cert.Leaf.IPAddresses) != 1 || cert.Leaf.IPAddresses[0].String() != "127.0.0.1" {
		t.Errorf("IP SANs = %v", cert.Leaf.IPAddresses)
	}
}

func TestCertCache(t *testing.T) {
	ca, _ := mitm.Generate("Test CA")
	issuer := mitm.NewIssuer(ca)
	first, err := issuer.TLSCertFor("a.test")
	if err != nil {
		t.Fatal(err)
	}
	second, _ := issuer.TLSCertFor("a.test")
	if first != second {
		t.Error("the certificate was minted twice for the same host")
	}
	if issuer.CachedCount() != 1 {
		t.Errorf("cached = %d", issuer.CachedCount())
	}
	// A different host gets its own certificate.
	if _, err := issuer.TLSCertFor("b.test"); err != nil {
		t.Fatal(err)
	}
	if issuer.CachedCount() != 2 {
		t.Errorf("cached = %d", issuer.CachedCount())
	}
}

func TestHandshake(t *testing.T) {
	ca, _ := mitm.Generate("Test CA")
	issuer := mitm.NewIssuer(ca)

	// Serve TLS with the issuer's config on a real connection, then connect as
	// a client that trusts the authority.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		srv := tls.Server(c, issuer.TLSConfig())
		srv.Handshake()
		srv.Close()
	}()
	c2, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(ca.Cert)
	client := tls.Client(c2, &tls.Config{
		ServerName: "example.test",
		RootCAs:    roots,
		MinVersion: tls.VersionTLS12,
	})
	defer client.Close()
	if err := client.Handshake(); err != nil {
		t.Fatalf("handshake: %v", err)
	}
	if got := client.ConnectionState().ServerName; got != "example.test" {
		t.Errorf("server name = %q", got)
	}
}

func TestLoadOrCreate(t *testing.T) {
	dir := t.TempDir()
	ca1, err := mitm.LoadOrCreate(dir, "Test CA")
	if err != nil {
		t.Fatal(err)
	}
	// A second call must load the same authority, so a client that installed
	// the certificate once keeps working.
	ca2, err := mitm.LoadOrCreate(dir, "Test CA")
	if err != nil {
		t.Fatal(err)
	}
	if ca1.Cert.SerialNumber.Cmp(ca2.Cert.SerialNumber) != 0 {
		t.Error("a new authority was created instead of loading the saved one")
	}
	for _, name := range []string{"ca.crt", "ca.key"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	// The key must not be world readable.
	info, err := os.Stat(filepath.Join(dir, "ca.key"))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		t.Errorf("the key is readable by others: %o", perm)
	}
}
