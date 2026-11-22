package mitm

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// ErrReqableNotFound reports that Reqable has no certificate on this machine.
// It is not a failure: the caller can mint an authority of its own instead.
var ErrReqableNotFound = errors.New("mitm: Reqable's certificate was not found")

// ReqableCertificatePath is where Reqable keeps its authority, given its data
// directory.
//
// capture.bin is a small JSON document holding the certificate and its PKCS#8
// private key in PEM form, in the clear. Reqable writes it on first launch and
// keeps it across upgrades, because the certificate installed in clients has to
// keep working: a format change would invalidate every installation. That is
// what makes reading it, rather than asking the user to export a p12, the
// quieter way to share the authority.
func ReqableCertificatePath(dir string) string {
	return filepath.Join(dir, "certificate", "capture.bin")
}

// ReqableDir returns Reqable's data directory for the running platform.
func ReqableDir() (string, error) {
	switch runtime.GOOS {
	case "darwin":
		dir, err := os.UserConfigDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(dir, "com.reqable.macosx"), nil
	case "windows":
		dir, err := os.UserConfigDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(dir, "Reqable"), nil
	default:
		// Reqable follows the XDG data directory on Linux, not the config one.
		if dir := os.Getenv("XDG_DATA_HOME"); dir != "" {
			return filepath.Join(dir, "reqable"), nil
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, ".local", "share", "reqable"), nil
	}
}

// reqableFile is the shape of capture.bin. Only the two PEM fields are read:
// the rest (a password for exported p12 files, the fingerprint, the generation
// hash) belongs to Reqable, not to the authority itself.
type reqableFile struct {
	Cert string `json:"cert"`
	Key  string `json:"pkey"`
}

// LoadReqable reads the authority from Reqable's data directory, which is
// found when dir is empty. It also returns the file it read, for logging.
func LoadReqable(dir string) (*CA, string, error) {
	if dir == "" {
		found, err := ReqableDir()
		if err != nil {
			return nil, "", fmt.Errorf("%w: cannot locate Reqable's data directory: %v", ErrReqableNotFound, err)
		}
		dir = found
	}
	path := ReqableCertificatePath(dir)
	ca, err := LoadReqableFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, path, fmt.Errorf("%w: %s", ErrReqableNotFound, path)
	}
	if err != nil {
		return nil, path, err
	}
	return ca, path, nil
}

// LoadReqableFile reads an authority from a capture.bin file.
func LoadReqableFile(path string) (*CA, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("mitm: read Reqable certificate: %w", err)
	}
	var f reqableFile
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("mitm: parse Reqable certificate %s: %w", path, err)
	}
	if f.Cert == "" || f.Key == "" {
		return nil, fmt.Errorf("mitm: %s holds no certificate or no private key", path)
	}
	ca, err := Parse([]byte(f.Cert), []byte(f.Key))
	if err != nil {
		return nil, fmt.Errorf("mitm: Reqable certificate %s: %w", path, err)
	}
	return ca, nil
}
