package rule

import (
	"github.com/jlvihv/qingqiuxia/internal/codec"
	"github.com/jlvihv/qingqiuxia/internal/msg"
)

// The actions that edit bodies take the decoded text, and put the content
// coding back. These two keep the codec package's names short at the call
// sites that read as prose.

func codecSupported(contentEncoding string) bool { return codec.Supported(contentEncoding) }

func decodeBody(m *msg.Message) ([]byte, error) {
	return codec.Decode(m.Body, m.Header.Get("Content-Encoding"))
}

// setEncodedBody applies text to a message as its body, re-applying the
// content coding the message arrived with.
func setEncodedBody(m *msg.Message, text []byte) error {
	encoded, err := codec.Encode(text, m.Header.Get("Content-Encoding"))
	if err != nil {
		return err
	}
	m.SetBody(encoded)
	return nil
}
