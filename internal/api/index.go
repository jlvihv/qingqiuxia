package api

import "net/http"

// The index is written for a program that has just been pointed at this API,
// not for a person reading a web page: an agent with a shell and curl has to
// learn what it can do, and every token it spends learning is one it cannot
// spend answering.
//
// So it is plain text, not HTML or a schema dump. It is short enough to read in
// one go, it leads with the three commands that answer most questions, and it
// says what each costs: the difference between a 200-row list and a filtered
// ten-row table is a factor of sixty in context, and an agent that does not know
// that will ask for everything and then drown.

const indexText = `请求侠 (qingqiuxia) control API

A debugging proxy. It records HTTP and HTTPS traffic, and can answer requests
itself (mock), change them (rewrite), or hold them for a decision (breakpoint).

Read this page, then use it: every path below is JSON unless it says otherwise.

== The three calls that answer most questions ==

  # what has been captured, most recent first
  curl -s 'API/api/sessions?limit=20&shape=table'

  # one exchange in full (drop part= for the bodies)
  curl -s 'API/api/sessions/ID?part=summary'

  # is it running, is it capturing, where is the proxy
  curl -s 'API/api/status'

== Asking for less on purpose ==

A row is about 300 bytes as JSON. Two hundred of them is most of an agent's
context, so the list is filtered and shaped on the server:

  limit=20         rows at most (default 20, max 1000)
  since=30s|5m|1h  only the last window
  host=api.foo.com or *.foo.com
  status=5xx       or 404, or 2xx, or a single code
  method=POST
  url=*/login*     * matches anything, ? one character
  shape=table      arrays instead of objects: about 60% fewer tokens
  fields=id,status,url   only these columns

One exchange has three parts, because a body is usually not what the question is
about:

  part=summary     url, status, timings, sizes     ~300 bytes
  part=headers     the request and response headers
  part=body        the bodies, decoded for reading
  max=2000         bytes of each body at most

== Changing traffic ==

Rules are matched by URL pattern and run in order. A rule is one of three kinds:

  mock     answers instead of the origin
  rewrite  changes the request or the response and forwards it
  abort    refuses it

  # a mock: every request whose path ends in /login answers 200 with this body
  curl -s -X POST API/api/rules -H 'Content-Type: application/json' -d '{
    "name":"fake login","kind":"mock","phase":"request","pattern":"*/login",
    "mock":{"status":200,"body":"{\\"token\\":\\"fake\\"}","contentType":"application/json"}}'

  # a rewrite: change every "secret" in responses to "***"
  curl -s -X POST API/api/rules -H 'Content-Type: application/json' -d '{
    "name":"scrub","kind":"rewrite","phase":"response","pattern":"*",
    "actions":[{"type":"replace-body","find":"secret","replace":"***"}]}'

  GET    API/api/rules            list them
  PATCH  API/api/rules/ID         {"enabled":false} to switch one off
  DELETE API/api/rules/ID         remove it

Rewrite actions: set-url, set-query, set-header, remove-header, set-body,
replace-body, set-status, redirect.

== Pausing traffic ==

A breakpoint rule stops matching traffic until someone decides:

  curl -s -X PUT API/api/breakpoints -H 'Content-Type: application/json' \
    -d '{"breakpoints":[{"name":"login","phase":"request","pattern":"*/login"}]}'

  curl -s API/api/pauses                       what is waiting
  curl -s -X PUT API/api/pauses/ID \
    -H 'Content-Type: application/json' \
    -d '{"headers":{"Authorization":["Bearer test"]}}'
  curl -s -X POST API/api/pauses/ID/resume     let it go
  curl -s -X POST API/api/pauses/ID/abort      refuse it

A paused request holds the client open, so release it: a pause nobody answers
times out after five minutes.

== The proxy itself ==

  curl -s API/api/proxy                        is it listening, and where
  curl -s -X POST API/api/proxy/start \
    -H 'Content-Type: application/json' -d '{"addr":"0.0.0.0:8899"}'
  curl -s -X POST API/api/proxy/stop

  The proxy and this API are two different listeners. The proxy is what a client
  points at; this API is what a tool talks to. The proxy can be stopped while
  this API keeps answering.

== Watching, instead of asking again ==

  curl -sN API/api/events

Server-sent events: one JSON object per line, named session, dropped, paused or
resumed. It keeps running until the connection closes.

== Errors ==

{"error":"..."} with a status: 400 bad input, 404 no such thing, 409 conflict
(a port that is taken), 501 this core does not have that. The message says what
is wrong; read it rather than retrying unchanged.
`

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	body := indexText
	if base := s.baseURL(r); base != "" {
		body = replaceAll(body, "API", base)
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Qingqiuxia-Api-Version", itoa(APIVersion))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(body))
}

// baseURL is how a caller reaches this API, so that the examples can be
// pasted as they are: an agent that has to rewrite every URL will get one of
// them wrong.
func (s *Server) baseURL(r *http.Request) string {
	if s.base != "" {
		return s.base
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if host := r.Host; host != "" {
		return scheme + "://" + host
	}
	return ""
}

// replaceAll is strings.ReplaceAll without the import in this file.
func replaceAll(s, old, new string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); {
		if i+len(old) <= len(s) && s[i:i+len(old)] == old {
			out = append(out, new...)
			i += len(old)
			continue
		}
		out = append(out, s[i])
		i++
	}
	return string(out)
}

// itoa is strconv.Itoa without the import.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
