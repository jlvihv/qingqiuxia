package main

import "crypto/tls"

// tlsConfig returns the client's TLS configuration.
func tlsConfig(insecure bool) *tls.Config {
	return &tls.Config{InsecureSkipVerify: insecure}
}
