package dashboard

import (
	"bufio"
	"bytes"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/flokiorg/lokihub/lspd/lsps"
	"github.com/flokiorg/lokihub/lspd/transport/auth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// countingReader counts the bytes the server pulls from the request body.
type countingReader struct {
	r io.Reader
	n int
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += n
	return n, err
}

// Anyone can make a Nostr key: a request signed by a key that is not the
// admin's is refused without the server reading its body.
func TestServeHTTP_NonAdminLargeBodyRefusedUnread(t *testing.T) {
	sk, _ := newTestKeypair(t)
	_, adminPK := newTestKeypair(t)
	srv := NewServer(adminPK, &stubRouter{}, &stubNodesService{})

	body := bytes.Repeat([]byte("a"), 8<<20)
	url := "http://example.com/dashboard"
	cr := &countingReader{r: bytes.NewReader(body)}
	r := httptest.NewRequest(http.MethodPost, url, cr)
	r.Header.Set("Authorization", buildAuthHeader(t, sk, http.MethodPost, url, auth.ComputePayloadHash(body)))
	w := httptest.NewRecorder()

	srv.ServeHTTP(w, r)

	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.Zero(t, cr.n, "bytes read from a non-admin request body")
}

func TestServeHTTP_AdminBodyOverCapRefused(t *testing.T) {
	sk, pk := newTestKeypair(t)
	srv := NewServer(pk, &stubRouter{}, &stubNodesService{})

	body := bytes.Repeat([]byte("a"), int(MaxRequestBodyBytes)+1)
	url := "http://example.com/dashboard"
	cr := &countingReader{r: bytes.NewReader(body)}
	r := httptest.NewRequest(http.MethodPost, url, cr)
	r.Header.Set("Authorization", buildAuthHeader(t, sk, http.MethodPost, url, auth.ComputePayloadHash(body)))
	w := httptest.NewRecorder()

	srv.ServeHTTP(w, r)

	assert.Equal(t, http.StatusRequestEntityTooLarge, w.Code)
	assert.LessOrEqual(t, cr.n, int(MaxRequestBodyBytes)+1)
}

func TestServeHTTP_AdminBodyAtCapIsRouted(t *testing.T) {
	sk, pk := newTestKeypair(t)
	router := &stubRouter{resp: lsps.NewJsonRpcResponse(nil, "ok")}
	srv := NewServer(pk, router, &stubNodesService{})

	// A valid JSON-RPC request padded with whitespace to exactly the cap.
	rpc := `{"jsonrpc":"2.0","method":"get_summary","id":1}`
	body := []byte(rpc + strings.Repeat(" ", int(MaxRequestBodyBytes)-len(rpc)))
	url := "http://example.com/dashboard"
	r := httptest.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	r.Header.Set("Authorization", buildAuthHeader(t, sk, http.MethodPost, url, auth.ComputePayloadHash(body)))
	w := httptest.NewRecorder()

	srv.ServeHTTP(w, r)

	assert.Equal(t, http.StatusOK, w.Code)
}

// The query-parameter token is for WebSocket upgrades only.
func TestServeHTTP_QueryTokenRefusedOutsideWebSocket(t *testing.T) {
	sk, pk := newTestKeypair(t)
	srv := NewServer(pk, &stubRouter{}, &stubNodesService{})

	u := "http://example.com/dashboard"
	token := strings.TrimPrefix(buildAuthHeader(t, sk, http.MethodPost, u, ""), "Nostr ")
	r := httptest.NewRequest(http.MethodPost, u+"?auth="+url.QueryEscape(token), nil)
	w := httptest.NewRecorder()

	srv.ServeHTTP(w, r)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestNewHTTPServer_BoundsEveryPhase(t *testing.T) {
	s := NewHTTPServer(":0", http.NotFoundHandler())
	assert.Equal(t, ReadHeaderTimeout, s.ReadHeaderTimeout)
	assert.Equal(t, ReadTimeout, s.ReadTimeout)
	assert.Equal(t, WriteTimeout, s.WriteTimeout)
	assert.Equal(t, IdleTimeout, s.IdleTimeout)
	assert.Positive(t, s.ReadHeaderTimeout)
	assert.Positive(t, s.ReadTimeout)
	assert.Positive(t, s.WriteTimeout)
	assert.Positive(t, s.IdleTimeout)
	assert.LessOrEqual(t, s.ReadHeaderTimeout, 10*time.Second)
}

// startServer serves NewHTTPServer's settings on a loopback port, with the
// header timeout shortened so the test runs fast.
func startServer(t *testing.T, headerTimeout time.Duration) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	_, pk := newTestKeypair(t)
	s := NewHTTPServer("", NewServer(pk, &stubRouter{}, &stubNodesService{}))
	s.ReadHeaderTimeout = headerTimeout
	go func() { _ = s.Serve(ln) }()
	t.Cleanup(func() { _ = s.Close() })
	return ln.Addr().String()
}

// A client that sends its headers a byte at a time is cut off.
func TestNewHTTPServer_SlowHeadersAreCutOff(t *testing.T) {
	addr := startServer(t, 200*time.Millisecond)
	conn, err := net.Dial("tcp", addr)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()

	_, err = conn.Write([]byte("GET /healthz HTTP/1.1\r\nHost: x\r\n"))
	require.NoError(t, err)
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(5*time.Second)))
	start := time.Now()
	_, err = io.ReadAll(conn)
	require.NoError(t, err, "server closes the connection rather than the client deadline firing")
	assert.Less(t, time.Since(start), 3*time.Second)
}

// A client that completes its headers in time is served.
func TestNewHTTPServer_PromptHeadersAreServed(t *testing.T) {
	addr := startServer(t, 200*time.Millisecond)
	conn, err := net.Dial("tcp", addr)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()

	_, err = conn.Write([]byte("GET /healthz HTTP/1.1\r\nHost: x\r\nConnection: close\r\n\r\n"))
	require.NoError(t, err)
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(5*time.Second)))
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

// countingRouter records whether any request reached the dashboard methods.
type countingRouter struct{ calls int }

func (c *countingRouter) Route(*lsps.JsonRpcRequest) *lsps.JsonRpcResponse {
	c.calls++
	return lsps.NewJsonRpcResponse(nil, "ok")
}

// A non-admin selecting a node is refused like any other non-admin call:
// before the body is read and before any method runs.
func TestServeHTTP_NonAdminNodeActionRefusedUnread(t *testing.T) {
	sk, _ := newTestKeypair(t)
	_, adminPK := newTestKeypair(t)
	router := &countingRouter{}
	srv := NewServer(adminPK, router, &stubNodesService{})

	body := []byte(`{"jsonrpc":"2.0","id":1,"method":"set_announce_state","params":{"action":"unlist","node":"0302"}}`)
	url := "http://example.com/dashboard"
	cr := &countingReader{r: bytes.NewReader(body)}
	r := httptest.NewRequest(http.MethodPost, url, cr)
	r.Header.Set("Authorization", buildAuthHeader(t, sk, http.MethodPost, url, auth.ComputePayloadHash(body)))
	w := httptest.NewRecorder()

	srv.ServeHTTP(w, r)

	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.Zero(t, cr.n)
	assert.Zero(t, router.calls)
}
