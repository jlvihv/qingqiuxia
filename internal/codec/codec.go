// Package codec encodes and decodes message bodies for inspection and editing.
//
// The proxy keeps bodies as they went on the wire; a rule or a viewer wants
// the decoded text. Both directions live here so that a body edited as text
// goes back out with the encoding it arrived with.
package codec

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/andybalholm/brotli"
	"github.com/klauspost/compress/zstd"

	"github.com/jlvihv/qingqiuxia/internal/msg"
)

// MaxDecoded bounds what decompression will produce, so that a compressed
// bomb cannot exhaust memory. Bodies larger than this are left encoded.
const MaxDecoded = 32 << 20 // 32 MiB

// ErrTooLarge reports that a body would decode past MaxDecoded.
var ErrTooLarge = errors.New("codec: decoded body too large")

// supported lists the content codings this package handles.
var supported = map[string]bool{
	"gzip":     true,
	"x-gzip":   true,
	"deflate":  true,
	"br":       true,
	"zstd":     true,
	"identity": true,
	"":         true,
}

// Supported reports whether every coding in a Content-Encoding value is one
// this package can decode.
func Supported(contentEncoding string) bool {
	for _, c := range codings(contentEncoding) {
		if !supported[c] {
			return false
		}
	}
	return true
}

// codings splits a Content-Encoding value into its codings, in the order they
// were applied.
func codings(ce string) []string {
	if ce == "" {
		return nil
	}
	parts := strings.Split(ce, ",")
	out := parts[:0]
	for _, p := range parts {
		if p = strings.ToLower(strings.TrimSpace(p)); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// Decode returns the body with its content coding removed. A body that is not
// encoded, or uses a coding this package does not support, is returned as is:
// callers show what they have rather than fail.
func Decode(body []byte, contentEncoding string) ([]byte, error) {
	cs := codings(contentEncoding)
	if len(cs) == 0 {
		return body, nil
	}
	// Codings are listed in the order applied, so undo them backwards.
	for i := len(cs) - 1; i >= 0; i-- {
		var err error
		switch cs[i] {
		case "identity":
			continue
		case "gzip", "x-gzip":
			body, err = readAllLimit(newGzipReader(body))
		case "deflate":
			body, err = inflate(body)
		case "br":
			body, err = readAllLimit(brotli.NewReader(bytes.NewReader(body)))
		case "zstd":
			body, err = decodeZstd(body)
		default:
			return body, fmt.Errorf("codec: unsupported content coding %q", cs[i])
		}
		if err != nil {
			return nil, err
		}
	}
	return body, nil
}

// Encode applies a content coding to a body, as a rule's text edit needs
// before the body goes back on the wire.
func Encode(body []byte, contentEncoding string) ([]byte, error) {
	cs := codings(contentEncoding)
	if len(cs) == 0 {
		return body, nil
	}
	for _, c := range cs {
		var err error
		switch c {
		case "identity":
			continue
		case "gzip", "x-gzip":
			var buf bytes.Buffer
			zw := gzip.NewWriter(&buf)
			if _, err = zw.Write(body); err == nil {
				err = zw.Close()
			}
			body = buf.Bytes()
			if err != nil {
				return nil, err
			}
		case "deflate":
			var buf bytes.Buffer
			zw := zlib.NewWriter(&buf)
			if _, err = zw.Write(body); err == nil {
				err = zw.Close()
			}
			body = buf.Bytes()
			if err != nil {
				return nil, err
			}
		case "br":
			var buf bytes.Buffer
			bw := brotli.NewWriter(&buf)
			if _, err = bw.Write(body); err == nil {
				err = bw.Close()
			}
			body = buf.Bytes()
			if err != nil {
				return nil, err
			}
		case "zstd":
			var buf bytes.Buffer
			zw, err := zstd.NewWriter(&buf)
			if err != nil {
				return nil, err
			}
			if _, err = zw.Write(body); err == nil {
				err = zw.Close()
			}
			body = buf.Bytes()
			if err != nil {
				return nil, err
			}
		default:
			return nil, fmt.Errorf("codec: unsupported content coding %q", c)
		}
	}
	return body, nil
}

// Reencode replaces a message's body with edited text, restoring the content
// coding it arrived with and keeping Content-Length in step. The caller is
// expected to have got text from EditableBody.
func Reencode(m *msg.Message, text []byte) error {
	encoded, err := Encode(text, m.Header.Get("Content-Encoding"))
	if err != nil {
		return err
	}
	if len(encoded) == len(m.Body) && bytes.Equal(encoded, m.Body) {
		return nil
	}
	m.SetBody(encoded)
	return nil
}

// SetTextBody replaces a message's body with text and the given content type,
// choosing a coding the request's original encoding can accept.
func SetTextBody(m *msg.Message, text []byte, contentType string) error {
	if contentType != "" {
		m.SetHeader("Content-Type", contentType)
	}
	return Reencode(m, text)
}

func newGzipReader(b []byte) io.Reader {
	r, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		return bytes.NewReader(nil)
	}
	return r
}

// inflate handles both the zlib-wrapped and the raw deflate bodies servers
// send under Content-Encoding: deflate, which the spec does not settle.
func inflate(b []byte) ([]byte, error) {
	if zr, err := zlib.NewReader(bytes.NewReader(b)); err == nil {
		defer zr.Close()
		return readAllLimit(zr)
	}
	fr := flate.NewReader(bytes.NewReader(b))
	defer fr.Close()
	return readAllLimit(fr)
}

func decodeZstd(b []byte) ([]byte, error) {
	zr, err := zstd.NewReader(bytes.NewReader(b), zstd.WithDecoderMaxMemory(MaxDecoded))
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	return readAllLimit(zr)
}

// readAllLimit reads r up to MaxDecoded+1 bytes and reports ErrTooLarge when
// it got more than MaxDecoded.
func readAllLimit(r io.Reader) ([]byte, error) {
	buf, err := io.ReadAll(io.LimitReader(r, MaxDecoded+1))
	if err != nil {
		return nil, err
	}
	if len(buf) > MaxDecoded {
		return nil, ErrTooLarge
	}
	return buf, nil
}
