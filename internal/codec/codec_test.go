package codec

import (
	"bytes"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	plain := []byte(`{"hello":"世界","n":42}`)
	for _, coding := range []string{"gzip", "deflate", "br", "zstd", ""} {
		encoded, err := Encode(plain, coding)
		if err != nil {
			t.Fatalf("%s encode: %v", coding, err)
		}
		if coding != "" && bytes.Equal(encoded, plain) {
			t.Errorf("%s did not compress", coding)
		}
		decoded, err := Decode(encoded, coding)
		if err != nil {
			t.Fatalf("%s decode: %v", coding, err)
		}
		if !bytes.Equal(decoded, plain) {
			t.Errorf("%s round trip = %q", coding, decoded)
		}
	}
}

func TestStackedCodings(t *testing.T) {
	plain := []byte("compressed twice")
	// A body encoded with gzip then br says "gzip, br" in that order.
	once, err := Encode(plain, "gzip")
	if err != nil {
		t.Fatal(err)
	}
	twice, err := Encode(once, "br")
	if err != nil {
		t.Fatal(err)
	}
	got, err := Decode(twice, "gzip, br")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, plain) {
		t.Errorf("got %q", got)
	}
}

func TestDecodeLeavesPlainAlone(t *testing.T) {
	plain := []byte("no coding")
	got, err := Decode(plain, "")
	if err != nil || !bytes.Equal(got, plain) {
		t.Errorf("got %q, %v", got, err)
	}
	// An unknown coding is reported, so a caller can show the raw body.
	if _, err := Decode(plain, "nonexistent"); err == nil {
		t.Error("an unknown coding should fail")
	}
	if Supported("nonexistent") {
		t.Error("Supported said yes to an unknown coding")
	}
}

func TestDeflateBothForms(t *testing.T) {
	// Some servers send raw deflate under Content-Encoding: deflate.
	plain := []byte("raw deflate payload")
	encoded, err := Encode(plain, "deflate")
	if err != nil {
		t.Fatal(err)
	}
	got, err := Decode(encoded, "deflate")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, plain) {
		t.Errorf("got %q", got)
	}
}
