package mcpconn

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Transport is an mcp.Transport for one server that also remembers what
// explains a failure (a stdio server's exit status and stderr, an HTTP
// server's 401) and the property order of the forms servers send (see
// OrderedSchema).
type Transport struct {
	inner   mcp.Transport
	proc    *process        // stdio
	rec     *statusRecorder // HTTP
	schemas schemaOrder
}

// Wrap adds OrderedSchema to any transport (an in-memory one, say).
func Wrap(t mcp.Transport) *Transport { return &Transport{inner: t} }

// HTTP is a streamable HTTP transport for url that sends headers with
// every request. client may be nil (http.DefaultClient's transport).
func HTTP(url string, headers http.Header, client *http.Client) *Transport {
	rec := &statusRecorder{headers: headers}
	hc := &http.Client{}
	if client != nil {
		*hc = *client
	}
	rec.next = hc.Transport
	if rec.next == nil {
		rec.next = http.DefaultTransport
	}
	hc.Transport = rec
	return &Transport{inner: &mcp.StreamableClientTransport{Endpoint: url, HTTPClient: hc}, rec: rec}
}

// Connect implements mcp.Transport.
func (t *Transport) Connect(ctx context.Context) (mcp.Connection, error) {
	c, err := t.inner.Connect(ctx)
	if err != nil {
		return nil, t.Explain(err)
	}
	return &orderConn{Connection: c, order: &t.schemas}, nil
}

// Stderr is the end of what a stdio server wrote to stderr ("" for HTTP).
func (t *Transport) Stderr() string {
	if t.proc == nil {
		return ""
	}
	return t.proc.tail.String()
}

// Explain turns an error from connecting or from a dropped session into
// the reason the server gave: for stdio "server exited with exit status
// 1: <its last stderr line>", for HTTP an *AuthError after a 401 or 403.
// Timeouts and cancellations are returned as they are.
func (t *Transport) Explain(err error) error {
	if err == nil || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return err
	}
	if t.rec != nil {
		if ae := t.rec.authError(); ae != nil {
			return ae
		}
	}
	if t.proc != nil {
		if exit := t.proc.exitError(2 * time.Second); exit != nil {
			return exit
		}
	}
	return err
}

// Ended says why a session ended, given what (*mcp.ClientSession).Wait
// returned: nil when the server just closed its side, which for a stdio
// server usually means it exited (with a status and a last stderr line).
func (t *Transport) Ended(err error) error {
	if err == nil {
		err = errors.New("the server closed the connection")
	}
	return t.Explain(err)
}

// AuthError is a 401 or 403 from an HTTP server: it needs (other)
// credentials. Codex shows such a server as "not logged in".
type AuthError struct {
	StatusCode      int
	WWWAuthenticate string
}

func (e *AuthError) Error() string {
	if e.StatusCode == http.StatusForbidden {
		return "access denied (HTTP 403)"
	}
	return "authentication required (HTTP 401)"
}

// IsAuthError reports whether err is (or wraps) an *AuthError.
func IsAuthError(err error) bool {
	var ae *AuthError
	return errors.As(err, &ae)
}

// statusRecorder adds the configured headers and notes the last 401/403:
// the SDK reports those only as text.
type statusRecorder struct {
	next    http.RoundTripper
	headers http.Header

	mu   sync.Mutex
	auth *AuthError
}

func (r *statusRecorder) RoundTrip(req *http.Request) (*http.Response, error) {
	if len(r.headers) > 0 {
		req = req.Clone(req.Context())
		for k, v := range r.headers {
			req.Header[k] = v
		}
	}
	resp, err := r.next.RoundTrip(req)
	if err == nil && req.Method == http.MethodPost {
		r.mu.Lock()
		switch resp.StatusCode {
		case http.StatusUnauthorized, http.StatusForbidden:
			r.auth = &AuthError{StatusCode: resp.StatusCode, WWWAuthenticate: resp.Header.Get("WWW-Authenticate")}
		default:
			r.auth = nil
		}
		r.mu.Unlock()
	}
	return resp, err
}

func (r *statusRecorder) authError() *AuthError {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.auth
}
