package msg

import "strings"

// MediaType strips the parameters from a Content-Type value: "text/html;
// charset=utf-8" becomes "text/html".
func MediaType(ct string) string {
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = ct[:i]
	}
	return strings.ToLower(strings.TrimSpace(ct))
}

// IsText reports whether a media type's body is worth showing and editing as
// text. It errs towards yes: a debugger that shows bytes as mojibake is less
// annoying than one that refuses to let you edit a body.
func IsText(mediaType string) bool {
	if mediaType == "" {
		return true
	}
	if strings.HasPrefix(mediaType, "text/") {
		return true
	}
	switch mediaType {
	case "application/json", "application/xml", "application/javascript",
		"application/x-www-form-urlencoded", "application/graphql",
		"application/x-ndjson", "application/ld+json",
		"application/x-www-form-urlencoded;charset=utf-8":
		return true
	}
	return strings.HasSuffix(mediaType, "+json") ||
		strings.HasSuffix(mediaType, "+xml") ||
		strings.HasSuffix(mediaType, "+text")
}

// IsStreaming reports whether a response is a live stream that must not be
// buffered: buffering server-sent events would stall the connection until the
// stream ended, which for a debugging tool means forever.
func IsStreaming(mediaType string) bool {
	return mediaType == "text/event-stream" || mediaType == "multipart/x-mixed-replace"
}
