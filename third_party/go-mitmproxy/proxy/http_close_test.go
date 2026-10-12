package proxy

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

type httpCloseAddon struct {
	BaseAddon
	heldPath         string
	entered, release chan struct{}
	releaseOnce      sync.Once
	servers          chan *wrapServerConn
	disconnected     chan struct{}
	clientDone       chan struct{}
	mu               sync.Mutex
	order            []string
	reenter          bool
}

func newHTTPCloseAddon(t *testing.T, path string) *httpCloseAddon {
	t.Helper()
	a := &httpCloseAddon{heldPath: path, entered: make(chan struct{}), release: make(chan struct{}),
		servers: make(chan *wrapServerConn, 4), disconnected: make(chan struct{}, 4), clientDone: make(chan struct{}, 4)}
	t.Cleanup(a.unblock)
	return a
}
func (a *httpCloseAddon) unblock() { a.releaseOnce.Do(func() { close(a.release) }) }
func (a *httpCloseAddon) Response(f *Flow) {
	if a.heldPath != "" && f.Request.URL.Path == a.heldPath {
		close(a.entered)
		<-a.release
	}
}
func (a *httpCloseAddon) ServerConnected(c *ConnContext) {
	a.servers <- c.ServerConn.Conn.(*wrapServerConn)
}
func (a *httpCloseAddon) record(event string) {
	a.mu.Lock()
	a.order = append(a.order, event)
	a.mu.Unlock()
}
func (a *httpCloseAddon) ServerDisconnected(c *ConnContext) {
	a.record("server")
	if a.reenter {
		c.ServerConn.Conn.Close()
	}
	a.disconnected <- struct{}{}
}
func (a *httpCloseAddon) ClientDisconnected(c *ClientConn) {
	a.record("client")
	if a.reenter {
		c.Conn.Close()
	}
	a.clientDone <- struct{}{}
}
func (a *httpCloseAddon) assertOrder(t *testing.T, want ...string) {
	t.Helper()
	a.mu.Lock()
	defer a.mu.Unlock()
	if fmt.Sprint(a.order) != fmt.Sprint(want) {
		t.Fatalf("disconnect order=%v want=%v", a.order, want)
	}
}

func serveHTTPClose(t *testing.T, p *Proxy) (net.Conn, *bufio.Reader, <-chan error, <-chan struct{}) {
	t.Helper()
	idle := make(chan struct{}, 8)
	state := p.entry.server.ConnState
	p.entry.server.ConnState = func(c net.Conn, s http.ConnState) {
		if state != nil {
			state(c, s)
		}
		if s == http.StateIdle {
			idle <- struct{}{}
		}
	}
	ln := loopback(t)
	done := reservedServe(t, p, ln)
	c, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	c.SetDeadline(time.Now().Add(5 * time.Second))
	return c, bufio.NewReader(c), done, idle
}
func sendHTTPClose(t *testing.T, c net.Conn, url string) {
	t.Helper()
	if _, err := fmt.Fprintf(c, "GET %s HTTP/1.1\r\nHost: proxy-test\r\n\r\n", url); err != nil {
		t.Fatal(err)
	}
}

type httpCloseResult struct {
	body []byte
	err  error
}

func readHTTPClose(br *bufio.Reader) httpCloseResult {
	r, err := http.ReadResponse(br, &http.Request{Method: "GET"})
	if err != nil {
		return httpCloseResult{err: err}
	}
	defer r.Body.Close()
	b, err := io.ReadAll(r.Body)
	return httpCloseResult{b, err}
}
func expectHTTPCloseBody(t *testing.T, r httpCloseResult, want []byte) {
	t.Helper()
	if r.err != nil || !bytes.Equal(r.body, want) {
		t.Fatalf("response error=%v bytes=%d want=%d", r.err, len(r.body), len(want))
	}
}
func receiveHTTPCloseServer(t *testing.T, a *httpCloseAddon) *wrapServerConn {
	t.Helper()
	select {
	case c := <-a.servers:
		return c
	case <-time.After(5 * time.Second):
		t.Fatal("upstream not connected")
		return nil
	}
}

// An unconditional upstream-driven client.Close loses the buffered response.
func TestHTTPUpstreamCloseRetainsBlockedLargeResponse(t *testing.T) {
	payload := bytes.Repeat([]byte("legal-response\n"), 163840)
	up := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(payload) }))
	up.Config.SetKeepAlivesEnabled(false)
	up.Start()
	t.Cleanup(up.Close)
	p := lifecycleProxy(t)
	a := newHTTPCloseAddon(t, "/")
	p.AddAddon(a)
	c, br, served, _ := serveHTTPClose(t, p)
	sendHTTPClose(t, c, up.URL+"/")
	result := make(chan httpCloseResult, 1)
	go func() { result <- readHTTPClose(br) }()
	awaitLifecycle(t, a.entered)
	awaitLifecycle(t, a.disconnected)
	select {
	case r := <-result:
		t.Fatalf("upstream close ended blocked response: %v bytes=%d", r.err, len(r.body))
	case <-time.After(30 * time.Millisecond):
	}
	a.unblock()
	expectHTTPCloseBody(t, <-result, payload)
	awaitLifecycle(t, a.clientDone)
	a.assertOrder(t, "server", "client")
	p.Close()
	awaitServe(t, served)
}

// CloseRead alone leaves the idle client and its disconnect callback alive.
func TestHTTPUpstreamCloseAlreadyIdleAndReentrant(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "first") }))
	t.Cleanup(up.Close)
	p := lifecycleProxy(t)
	a := newHTTPCloseAddon(t, "")
	a.reenter = true
	p.AddAddon(a)
	c, br, served, idle := serveHTTPClose(t, p)
	sendHTTPClose(t, c, up.URL)
	expectHTTPCloseBody(t, readHTTPClose(br), []byte("first"))
	awaitLifecycle(t, idle)
	sc := receiveHTTPCloseServer(t, a)
	closed := make(chan struct{})
	go func() { sc.Close(); sc.Close(); close(closed) }()
	awaitLifecycle(t, closed)
	awaitLifecycle(t, a.clientDone)
	if _, err := br.ReadByte(); !errors.Is(err, io.EOF) {
		t.Fatalf("idle client not closed: %v", err)
	}
	sc.Close()
	a.assertOrder(t, "server", "client")
	p.Close()
	awaitServe(t, served)
}

// This addon deliberately leaves the first owned socket alive while dialing
// the second; it exercises multiple real owners without changing pooling.
type secondHTTPUpstreamAddon struct{ BaseAddon }

func (*secondHTTPUpstreamAddon) Requestheaders(f *Flow) {
	if f.Request.URL.Path == "/second" {
		f.ConnContext.ServerConn = nil
	}
}

func TestHTTPUpstreamClosePreservesSecondKeepAliveResponse(t *testing.T) {
	payload := bytes.Repeat([]byte("second-response\n"), 8192)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/second" {
			w.Write(payload)
		} else {
			io.WriteString(w, "first")
		}
	}))
	t.Cleanup(up.Close)
	p := lifecycleProxy(t)
	a := newHTTPCloseAddon(t, "/second")
	p.AddAddon(&secondHTTPUpstreamAddon{})
	p.AddAddon(a)
	c, br, served, idle := serveHTTPClose(t, p)
	sendHTTPClose(t, c, up.URL+"/first")
	expectHTTPCloseBody(t, readHTTPClose(br), []byte("first"))
	awaitLifecycle(t, idle)
	first := receiveHTTPCloseServer(t, a)
	sendHTTPClose(t, c, up.URL+"/second")
	awaitLifecycle(t, a.entered)
	second := receiveHTTPCloseServer(t, a)
	result := make(chan httpCloseResult, 1)
	go func() { result <- readHTTPClose(br) }()
	closed := make(chan struct{})
	go func() { first.Close(); first.Close(); second.Close(); second.Close(); close(closed) }()
	awaitLifecycle(t, closed)
	awaitLifecycle(t, a.disconnected)
	awaitLifecycle(t, a.disconnected)
	select {
	case r := <-result:
		t.Fatalf("older upstream close truncated second response: %v bytes=%d", r.err, len(r.body))
	case <-time.After(30 * time.Millisecond):
	}
	a.unblock()
	expectHTTPCloseBody(t, <-result, payload)
	awaitLifecycle(t, a.clientDone)
	if _, err := br.ReadByte(); !errors.Is(err, io.EOF) {
		t.Fatalf("pending-close client stayed alive: %v", err)
	}
	a.assertOrder(t, "server", "server", "client")
	p.Close()
	awaitServe(t, served)
}

func TestHTTPUpstreamCloseForcedShutdownJoinsBlockedResponse(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "never delivered") }))
	t.Cleanup(up.Close)
	p := lifecycleProxy(t)
	a := newHTTPCloseAddon(t, "/")
	p.AddAddon(a)
	c, br, served, _ := serveHTTPClose(t, p)
	sendHTTPClose(t, c, up.URL+"/")
	awaitLifecycle(t, a.entered)
	sc := receiveHTTPCloseServer(t, a)
	sc.Close()
	awaitLifecycle(t, a.disconnected)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	shutdown := make(chan error, 1)
	go func() { shutdown <- p.Shutdown(ctx) }()
	if _, err := br.ReadByte(); !errors.Is(err, io.EOF) {
		t.Fatalf("force shutdown did not interrupt socket: %v", err)
	}
	select {
	case err := <-shutdown:
		t.Fatalf("Shutdown returned before capture release: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	a.unblock()
	select {
	case err := <-shutdown:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Shutdown error=%v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Shutdown waited for missing Idle transition")
	}
	awaitServe(t, served)
}

func TestHTTPUpstreamCloseConcurrentIdleTransition(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "flushed-before-idle") }))
	t.Cleanup(up.Close)
	p := lifecycleProxy(t)
	a := newHTTPCloseAddon(t, "")
	p.AddAddon(a)
	enterIdle, releaseIdle := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	unblockIdle := func() { releaseOnce.Do(func() { close(releaseIdle) }) }
	t.Cleanup(unblockIdle)
	state := p.entry.server.ConnState
	p.entry.server.ConnState = func(c net.Conn, s http.ConnState) {
		if s == http.StateIdle {
			close(enterIdle)
			<-releaseIdle
		}
		if state != nil {
			state(c, s)
		}
	}
	c, br, served, _ := serveHTTPClose(t, p)
	sendHTTPClose(t, c, up.URL)
	expectHTTPCloseBody(t, readHTTPClose(br), []byte("flushed-before-idle"))
	awaitLifecycle(t, enterIdle)
	sc := receiveHTTPCloseServer(t, a)
	start, closed := make(chan struct{}), make(chan struct{})
	var closers sync.WaitGroup
	for i := 0; i < 8; i++ {
		closers.Add(1)
		go func() { defer closers.Done(); <-start; sc.Close() }()
	}
	go func() { closers.Wait(); close(closed) }()
	close(start)
	unblockIdle()
	awaitLifecycle(t, closed)
	awaitLifecycle(t, a.clientDone)
	if _, err := br.ReadByte(); !errors.Is(err, io.EOF) {
		t.Fatalf("concurrent idle close left client open: %v", err)
	}
	a.assertOrder(t, "server", "client")
	p.Close()
	awaitServe(t, served)
}
