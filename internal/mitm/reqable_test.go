package mitm_test

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jlvihv/qingqiuxia/internal/mitm"
)

// rsaAuthority builds an RSA authority the way Reqable's capture.bin holds one:
// a self-signed CA with no subject key identifier, so that the fallback for
// issuers that do not carry one is exercised too.
func rsaAuthority(t *testing.T) (*x509.Certificate, *rsa.PrivateKey) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			Country:            []string{"CN"},
			Organization:       []string{"Reqable, LLC"},
			OrganizationalUnit: []string{"https://reqable.com/"},
			CommonName:         "Reqable Test CA",
		},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.AddDate(5, 0, 0),
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return cert, key
}

// writeReqableFile writes capture.bin in Reqable's shape below dir.
func writeReqableFile(t *testing.T, dir string, cert *x509.Certificate, key *rsa.PrivateKey) string {
	t.Helper()
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw})
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	body, err := json.Marshal(map[string]string{
		"cert": string(certPEM),
		"pkey": string(keyPEM),
		// Fields the authority does not need, but the file really has.
		"password":    "unused",
		"fingerprint": "unused",
	})
	if err != nil {
		t.Fatal(err)
	}
	path := mitm.ReqableCertificatePath(dir)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadReqable(t *testing.T) {
	cert, key := rsaAuthority(t)
	dir := t.TempDir()
	path := writeReqableFile(t, dir, cert, key)

	ca, gotPath, err := mitm.LoadReqable(dir)
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != path {
		t.Errorf("path = %q, want %q", gotPath, path)
	}
	if _, ok := ca.Key.(*rsa.PrivateKey); !ok {
		t.Fatalf("key type = %T, want the RSA key Reqable wrote", ca.Key)
	}
	if ca.Cert.Subject.CommonName != "Reqable Test CA" {
		t.Errorf("common name = %q", ca.Cert.Subject.CommonName)
	}

	issuer := mitm.NewIssuer(ca)
	leaf, err := issuer.TLSCertFor("api.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if got := leaf.Leaf.Issuer.CommonName; got != "Reqable Test CA" {
		t.Errorf("the leaf was signed by %q", got)
	}
	// The leaf takes the authority's name, with the host as its common name, so
	// that the chain reads as one authority rather than two tools.
	if got := leaf.Leaf.Subject.CommonName; got != "api.example.com" {
		t.Errorf("leaf common name = %q", got)
	}
	if got := strings.Join(leaf.Leaf.Subject.Organization, ","); got != "Reqable, LLC" {
		t.Errorf("leaf organization = %q, want the authority's", got)
	}
	if got := strings.Join(leaf.Leaf.Subject.OrganizationalUnit, ","); got != "https://reqable.com/" {
		t.Errorf("leaf organizational unit = %q, want the authority's", got)
	}
	// The authority has no subject key identifier, so the leaf's authority key
	// identifier has to be derived from its public key.
	if len(leaf.Leaf.AuthorityKeyId) == 0 {
		t.Error("the leaf has no authority key identifier")
	}

	roots := x509.NewCertPool()
	roots.AddCert(ca.Cert)
	if _, err := leaf.Leaf.Verify(x509.VerifyOptions{
		Roots:     roots,
		DNSName:   "api.example.com",
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}); err != nil {
		t.Errorf("the leaf does not verify against the shared authority: %v", err)
	}
}

func TestLoadReqableMissing(t *testing.T) {
	_, _, err := mitm.LoadReqable(t.TempDir())
	if !errors.Is(err, mitm.ErrReqableNotFound) {
		t.Fatalf("error = %v, want ErrReqableNotFound", err)
	}
}

func TestLoadReqableRejectsMismatchedKey(t *testing.T) {
	cert, _ := rsaAuthority(t)
	_, other := rsaAuthority(t)
	dir := t.TempDir()
	writeReqableFile(t, dir, cert, other)

	_, _, err := mitm.LoadReqable(dir)
	if err == nil {
		t.Fatal("a certificate and a key from different authorities were accepted")
	}
	if !strings.Contains(err.Error(), "does not belong") {
		t.Errorf("error = %v, want a complaint about the key", err)
	}
}

func TestParsePKCS1Key(t *testing.T) {
	cert, key := rsaAuthority(t)
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})

	ca, err := mitm.Parse(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := ca.Key.(*rsa.PrivateKey); !ok {
		t.Errorf("key type = %T", ca.Key)
	}
}
