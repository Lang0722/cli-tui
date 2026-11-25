package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
	"sync"
	"time"
)

// The streamable HTTP transport (spec 2025-03-26 and later): every client
// message is a POST to the server's endpoint. A request is answered with
// either one JSON response or an SSE stream that carries the response and
// anything the server sends meanwhile (an elicitation during a tool call);
// notifications and responses get 202 Accepted. The server may assign a
// session (Mcp-Session-Id), which every later message carries, and may
// offer a GET SSE stream for messages that belong to no request.

// AuthError is a 401 or 403 from an HTTP server: the server needs (other)
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

// ErrSessionExpired: the server forgot the session (HTTP 404); the client
// has to connect again.
var ErrSessionExpired = errors.New("mcp: session expired")

// HTTPOptions configures an HTTPTransport.
type HTTPOptions struct {
	Client *http.Client // default: http.DefaultClient
	// Headers are added to every request (e.g. an API key header).
	Headers map[string]string
	// BearerToken, if set, is sent as Authorization: Bearer.
	BearerToken string
}

// HTTPTransport is a client connection to a streamable HTTP server.
type HTTPTransport struct {
	url  string
	opts HTTPOptions

	mu        sync.Mutex
	sessionID string
	version   string
	listening bool

	in     chan Incoming
	ctx    context.Context
	cancel context.CancelFunc
	once   sync.Once
	fatal  error
	wg     sync.WaitGroup
}

// NewHTTPTransport connects to the MCP endpoint url (nothing is sent until
// the first message).
func NewHTTPTransport(url string, opts HTTPOptions) *HTTPTransport {
	if opts.Client == nil {
		opts.Client = http.DefaultClient
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &HTTPTransport{url: url, opts: opts, in: make(chan Incoming, 64), ctx: ctx, cancel: cancel}
}

// SessionID is the session the server assigned ("" if none).
func (t *HTTPTransport) SessionID() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.sessionID
}

// setProtocolVersion is called by Connect once initialize settled the
// revision; every later request carries it (MCP-Protocol-Version).
func (t *HTTPTransport) setProtocolVersion(v string) {
	t.mu.Lock()
	t.version = v
	t.mu.Unlock()
}

func (t *HTTPTransport) newRequest(ctx context.Context, method string, body []byte) (*http.Request, error) {
	var r io.Reader
	if body != nil {
		r = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, t.url, r)
	if err != nil {
		return nil, err
	}
	for k, v := range t.opts.Headers {
		req.Header.Set(k, v)
	}
	if t.opts.BearerToken != "" {
		req.Header.Set("Authorization", "Bearer "+t.opts.BearerToken)
	}
	t.mu.Lock()
	if t.sessionID != "" {
		req.Header.Set("Mcp-Session-Id", t.sessionID)
	}
	if t.version != "" {
		req.Header.Set("MCP-Protocol-Version", t.version)
	}
	t.mu.Unlock()
	return req, nil
}

// Send POSTs one message. For a request it returns once the server
// answered with headers; the response (and whatever comes with it) is
// read in the background and delivered by Recv.
func (t *HTTPTransport) Send(ctx context.Context, data []byte) error {
	if err := t.ctx.Err(); err != nil {
		return t.closedErr()
	}
	var head struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
	}
	_ = json.Unmarshal(data, &head)
	isRequest := head.Method != "" && hasID(head.ID)

	// The body outlives ctx's caller only as long as the transport: it is
	// cancelled when ctx is (the request was abandoned) or on Close.
	rctx, rcancel := context.WithCancel(t.ctx)
	stop := context.AfterFunc(ctx, rcancel)
	req, err := t.newRequest(rctx, http.MethodPost, data)
	if err != nil {
		stop()
		rcancel()
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := t.opts.Client.Do(req)
	if err != nil {
		stop()
		rcancel()
		if t.ctx.Err() != nil {
			return t.closedErr()
		}
		return fmt.Errorf("mcp: POST %s: %w", t.url, err)
	}
	finish := func() {
		resp.Body.Close()
		stop()
		rcancel()
	}
	if err := t.checkStatus(resp); err != nil {
		finish()
		return err
	}
	if head.Method == "initialize" {
		if sid := resp.Header.Get("Mcp-Session-Id"); sid != "" {
			t.mu.Lock()
			t.sessionID = sid
			t.mu.Unlock()
		}
	}
	if head.Method == "notifications/initialized" {
		t.startListening()
	}
	if !isRequest || resp.StatusCode == http.StatusAccepted {
		finish()
		return nil
	}
	related := idKey(head.ID)
	ct, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	switch ct {
	case "text/event-stream":
		t.wg.Add(1)
		go func() {
			defer t.wg.Done()
			defer finish()
			t.readSSE(resp.Body, related)
		}()
		return nil
	case "application/json", "":
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxMessage))
		finish()
		if err != nil {
			return fmt.Errorf("mcp: read response: %w", err)
		}
		t.deliver(Incoming{Data: body, InReplyTo: related})
		return nil
	default:
		finish()
		return fmt.Errorf("mcp: unexpected response type %q", ct)
	}
}

func (t *HTTPTransport) checkStatus(resp *http.Response) error {
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return &AuthError{StatusCode: resp.StatusCode, WWWAuthenticate: resp.Header.Get("WWW-Authenticate")}
	case resp.StatusCode == http.StatusNotFound && t.SessionID() != "":
		t.fail(ErrSessionExpired)
		return ErrSessionExpired
	case resp.StatusCode < 200 || resp.StatusCode > 299:
		excerpt, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		msg := strings.TrimSpace(string(excerpt))
		if msg == "" {
			msg = http.StatusText(resp.StatusCode)
		}
		return fmt.Errorf("mcp: HTTP %d: %s", resp.StatusCode, msg)
	}
	return nil
}

func (t *HTTPTransport) deliver(in Incoming) {
	select {
	case t.in <- in:
	case <-t.ctx.Done():
	}
}

// readSSE delivers each "message" event of an SSE stream.
func (t *HTTPTransport) readSSE(body io.Reader, related string) {
	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 64<<10), maxMessage)
	var data []string
	event := ""
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			if len(data) > 0 && (event == "" || event == "message") {
				t.deliver(Incoming{Data: []byte(strings.Join(data, "\n")), InReplyTo: related})
			}
			data, event = nil, ""
		case strings.HasPrefix(line, ":"):
			// comment / keep-alive
		default:
			field, value, _ := strings.Cut(line, ":")
			value = strings.TrimPrefix(value, " ")
			switch field {
			case "data":
				data = append(data, value)
			case "event":
				event = value
			}
		}
	}
	if len(data) > 0 && (event == "" || event == "message") {
		t.deliver(Incoming{Data: []byte(strings.Join(data, "\n")), InReplyTo: related})
	}
}

// startListening opens the GET stream for server messages that belong to
// no request, reconnecting while the transport is open. A server without
// one answers 405, and then there is none.
func (t *HTTPTransport) startListening() {
	t.mu.Lock()
	if t.listening {
		t.mu.Unlock()
		return
	}
	t.listening = true
	t.mu.Unlock()
	t.wg.Add(1)
	go func() {
		defer t.wg.Done()
		backoff := 500 * time.Millisecond
		for t.ctx.Err() == nil {
			req, err := t.newRequest(t.ctx, http.MethodGet, nil)
			if err != nil {
				return
			}
			req.Header.Set("Accept", "text/event-stream")
			resp, err := t.opts.Client.Do(req)
			if err == nil {
				if resp.StatusCode == http.StatusMethodNotAllowed {
					resp.Body.Close()
					return
				}
				if t.checkStatus(resp) == nil {
					backoff = 500 * time.Millisecond
					t.readSSE(resp.Body, "")
				}
				resp.Body.Close()
			}
			select {
			case <-t.ctx.Done():
				return
			case <-time.After(backoff):
			}
			backoff = min(backoff*2, 30*time.Second)
		}
	}()
}

func (t *HTTPTransport) Recv() (Incoming, error) {
	select {
	case in := <-t.in:
		return in, nil
	case <-t.ctx.Done():
		return Incoming{}, t.closedErr()
	}
}

func (t *HTTPTransport) fail(err error) {
	t.once.Do(func() {
		t.fatal = err
		t.cancel()
	})
}

func (t *HTTPTransport) closedErr() error {
	if t.fatal != nil && t.fatal != ErrClosed {
		return t.fatal
	}
	return ErrClosed
}

// Close ends the session (DELETE, as the spec asks) and the streams.
func (t *HTTPTransport) Close() error {
	if sid := t.SessionID(); sid != "" && t.ctx.Err() == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		if req, err := t.newRequest(ctx, http.MethodDelete, nil); err == nil {
			if resp, err := t.opts.Client.Do(req); err == nil {
				resp.Body.Close()
			}
		}
		cancel()
	}
	t.fail(ErrClosed)
	t.wg.Wait()
	return nil
}
