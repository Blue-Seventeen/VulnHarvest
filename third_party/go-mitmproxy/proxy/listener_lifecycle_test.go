package proxy

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	log "github.com/sirupsen/logrus"
)

type lifecycleAddon struct {
	BaseAddon
	responses atomic.Int32
	starts    chan struct{}
	ends      chan struct{}
}

func (a *lifecycleAddon) Response(f *Flow)       { a.responses.Add(1) }
func (a *lifecycleAddon) WebSocketStart(f *Flow) { a.starts <- struct{}{} }
func (a *lifecycleAddon) WebSocketEnd(f *Flow)   { a.ends <- struct{}{} }

func lifecycleProxy(t *testing.T) *Proxy {
	t.Helper()
	p, err := NewProxy(&Options{Addr: "127.0.0.1:0", SslInsecure: true, CaRootPath: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	p.SetUpstreamProxy(func(*http.Request) (*url.URL, error) { return nil, nil })
	t.Cleanup(func() { p.Close() })
	return p
}
func reservedServe(t *testing.T, p *Proxy, ln net.Listener) <-chan error {
	t.Helper()
	s, ok := any(p).(interface{ Serve(net.Listener) error })
	if !ok {
		t.Fatal("Proxy lacks reserved listener Serve")
	}
	done := make(chan error, 1)
	go func() { done <- s.Serve(ln) }()
	return done
}
func loopback(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	return ln
}
func awaitLifecycle(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal("owned work did not finish")
	}
}
func awaitServe(t *testing.T, ch <-chan error) {
	t.Helper()
	select {
	case err := <-ch:
		if err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, net.ErrClosed) {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not exit")
	}
}

func TestReservedListenerCapturesHTTPHTTPSAndH2(t *testing.T) {
	for _, proto := range []string{"http", "https", "h2"} {
		t.Run(proto, func(t *testing.T) {
			target := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprintf(w, "captured:%s", r.Proto) }))
			if proto == "http" {
				target.Start()
			} else {
				target.EnableHTTP2 = proto == "h2"
				target.StartTLS()
			}
			defer target.Close()
			p := lifecycleProxy(t)
			addon := &lifecycleAddon{}
			p.AddAddon(addon)
			ln := loopback(t)
			done := reservedServe(t, p, ln)
			proxyURL, _ := url.Parse("http://" + ln.Addr().String())
			pool := x509.NewCertPool()
			ca := p.GetCertificate()
			pool.AddCert(&ca)
			tr := &http.Transport{Proxy: http.ProxyURL(proxyURL), TLSClientConfig: &tls.Config{RootCAs: pool}, ForceAttemptHTTP2: proto == "h2"}
			defer tr.CloseIdleConnections()
			client := &http.Client{Transport: tr, Timeout: 5 * time.Second}
			endpoint := strings.Replace(target.URL, "127.0.0.1", "localhost", 1)
			res, err := client.Get(endpoint + "/capture")
			if err != nil {
				t.Fatal(err)
			}
			body, err := io.ReadAll(res.Body)
			res.Body.Close()
			if err != nil {
				t.Fatal(err)
			}
			want := "captured:HTTP/1.1"
			if proto == "h2" {
				want = "captured:HTTP/2.0"
			}
			if string(body) != want {
				t.Fatalf("body=%q want %q", body, want)
			}
			if addon.responses.Load() != 1 {
				t.Fatalf("captured %d responses", addon.responses.Load())
			}
			if err := p.Close(); err != nil {
				t.Fatal(err)
			}
			awaitServe(t, done)
			if _, err := client.Get(endpoint); err == nil {
				t.Fatal("request succeeded after Close")
			}
			if addon.responses.Load() != 1 {
				t.Fatal("post-Close capture")
			}
		})
	}
}

func TestCloseOwnsHijackedCONNECTAndTLSHandshake(t *testing.T) {
	for _, tlsStart := range []bool{false, true} {
		t.Run(fmt.Sprint(tlsStart), func(t *testing.T) {
			p := lifecycleProxy(t)
			p.AddAddon(NewUpstreamCertAddon(false))
			ln := loopback(t)
			done := reservedServe(t, p, ln)
			c, err := net.Dial("tcp", ln.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			fmt.Fprint(c, "CONNECT localhost:1 HTTP/1.1\r\nHost: localhost:1\r\n\r\n")
			// Lazy interception allows testing a stalled client without dialing a target.
			_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
			r, err := http.ReadResponse(bufio.NewReader(c), &http.Request{Method: "CONNECT"})
			if err != nil || r.StatusCode != 200 {
				t.Fatalf("CONNECT: %v %v", r, err)
			}
			if tlsStart {
				c.Write([]byte{0x16, 0x03, 0x01, 0x00, 0x30})
			}
			closed := make(chan struct{})
			go func() { p.Close(); close(closed) }()
			awaitLifecycle(t, closed)
			awaitServe(t, done)
			if _, err := c.Read(make([]byte, 1)); err == nil {
				t.Fatal("hijacked socket remains open")
			}
		})
	}
}

func TestCloseWebSocketJoinsBothDirections(t *testing.T) {
	out := captureLifecycleLogs(t)
	u := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := u.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		for {
			k, b, e := c.ReadMessage()
			if e != nil {
				return
			}
			if c.WriteMessage(k, b) != nil {
				return
			}
		}
	}))
	defer target.Close()
	p := lifecycleProxy(t)
	a := &lifecycleAddon{starts: make(chan struct{}, 1), ends: make(chan struct{}, 1)}
	p.AddAddon(a)
	ln := loopback(t)
	done := reservedServe(t, p, ln)
	pu, _ := url.Parse("http://" + ln.Addr().String())
	pool := x509.NewCertPool()
	ca := p.GetCertificate()
	pool.AddCert(&ca)
	d := websocket.Dialer{Proxy: http.ProxyURL(pu), TLSClientConfig: &tls.Config{RootCAs: pool}, HandshakeTimeout: 5 * time.Second}
	c, _, err := d.Dial(strings.Replace("wss"+target.URL[5:], "127.0.0.1", "localhost", 1)+"/echo", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	awaitLifecycle(t, a.starts)
	if err := c.WriteMessage(websocket.TextMessage, []byte("echo")); err != nil {
		t.Fatal(err)
	}
	_, b, err := c.ReadMessage()
	if err != nil || string(b) != "echo" {
		t.Fatalf("echo %q: %v", b, err)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	awaitServe(t, done)
	select {
	case <-a.ends:
	default:
		t.Fatal("Close returned before WebSocketEnd")
	}
	if strings.Contains(out.String(), `"level":"error"`) {
		t.Fatalf("expected WSS stop produced error:\n%s", out)
	}
}

func TestListenerOwnershipRejectedAndRepeatedServe(t *testing.T) {
	p := lifecycleProxy(t)
	p.Close()
	ln := loopback(t)
	s, ok := any(p).(interface{ Serve(net.Listener) error })
	if !ok {
		t.Fatal("missing Serve")
	}
	if s.Serve(ln) == nil {
		t.Fatal("Serve accepted closed proxy")
	}
	if _, err := ln.Accept(); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("rejected listener not closed: %v", err)
	}
	if s.Serve(nil) == nil {
		t.Fatal("nil listener accepted")
	}
	p2 := lifecycleProxy(t)
	first := loopback(t)
	done := reservedServe(t, p2, first)
	// A real request establishes that first Serve acquired ownership.
	c, err := net.Dial("tcp", first.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprint(c, "GET / HTTP/1.1\r\nHost: proxy\r\nConnection: close\r\n\r\n")
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := http.ReadResponse(bufio.NewReader(c), nil); err != nil {
		t.Fatal(err)
	}
	c.Close()
	second := loopback(t)
	s2 := any(p2).(interface{ Serve(net.Listener) error })
	if s2.Serve(second) == nil {
		t.Fatal("second Serve accepted")
	}
	if _, err := second.Accept(); !errors.Is(err, net.ErrClosed) {
		t.Fatal("second listener leaked")
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Go(func() {
			if err := p2.Close(); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	awaitServe(t, done)
}

func TestAttackerListenerCloseWakesAcceptAndPendingSend(t *testing.T) {
	p := lifecycleProxy(t)
	l := p.attacker.listener
	accepted := make(chan struct{})
	go func() { l.Accept(); close(accepted) }()
	l.Close()
	awaitLifecycle(t, accepted)
	x, y := net.Pipe()
	defer y.Close()
	sent := make(chan struct{})
	go func() { l.accept(x); close(sent) }()
	awaitLifecycle(t, sent)
	pending := lifecycleProxy(t).attacker.listener
	x2, y2 := net.Pipe()
	defer y2.Close()
	pendingDone := make(chan struct{})
	entered := make(chan struct{})
	go func() { close(entered); pending.accept(x2); close(pendingDone) }()
	awaitLifecycle(t, entered)
	pending.Close()
	awaitLifecycle(t, pendingDone)
}

func TestShutdownDeadlineStillJoinsSink(t *testing.T) {
	p := lifecycleProxy(t)
	ln := loopback(t)
	done := reservedServe(t, p, ln)
	c, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprint(c, "GET / HTTP/1.1\r\nHost: proxy\r\nConnection: close\r\n\r\n")
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := http.ReadResponse(bufio.NewReader(c), nil); err != nil {
		t.Fatal(err)
	}
	c.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := p.Shutdown(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Shutdown error=%v", err)
	}
	awaitServe(t, done)
}

type blockedResponseAddon struct {
	BaseAddon
	entered, release chan struct{}
	writes           atomic.Int32
}

func (a *blockedResponseAddon) Response(*Flow) { close(a.entered); <-a.release; a.writes.Add(1) }

func TestCloseJoinsInFlightHTTPHTTPSAndH2Capture(t *testing.T) {
	for _, proto := range []string{"http", "https", "h2"} {
		t.Run(proto, func(t *testing.T) {
			target := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "in-flight capture") }))
			if proto == "http" {
				target.Start()
			} else {
				target.EnableHTTP2 = proto == "h2"
				target.StartTLS()
			}
			defer target.Close()
			p := lifecycleProxy(t)
			a := &blockedResponseAddon{entered: make(chan struct{}), release: make(chan struct{})}
			p.AddAddon(a)
			ln := loopback(t)
			done := reservedServe(t, p, ln)
			pu, _ := url.Parse("http://" + ln.Addr().String())
			pool := x509.NewCertPool()
			ca := p.GetCertificate()
			pool.AddCert(&ca)
			tr := &http.Transport{Proxy: http.ProxyURL(pu), TLSClientConfig: &tls.Config{RootCAs: pool}, ForceAttemptHTTP2: proto == "h2"}
			defer tr.CloseIdleConnections()
			requestDone := make(chan struct{})
			go func() {
				defer close(requestDone)
				c := &http.Client{Transport: tr, Timeout: 5 * time.Second}
				r, e := c.Get(strings.Replace(target.URL, "127.0.0.1", "localhost", 1))
				if e == nil {
					r.Body.Close()
				}
			}()
			awaitLifecycle(t, a.entered)
			closed := make(chan struct{})
			go func() { p.Close(); close(closed) }()
			select {
			case <-closed:
				t.Fatal("Close returned before capture callback")
			case <-time.After(30 * time.Millisecond):
			}
			close(a.release)
			awaitLifecycle(t, closed)
			awaitLifecycle(t, requestDone)
			awaitServe(t, done)
			if a.writes.Load() != 1 {
				t.Fatal("capture callback did not complete before Close")
			}
		})
	}
}

func TestTransparentCONNECTTransfersHTTPAndCloses(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "through CONNECT transfer") }))
	defer target.Close()
	p := lifecycleProxy(t)
	p.SetShouldInterceptRule(func(*http.Request) bool { return false })
	ln := loopback(t)
	done := reservedServe(t, p, ln)
	c, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(5 * time.Second))
	host := target.Listener.Addr().String()
	fmt.Fprintf(c, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", host, host)
	br := bufio.NewReader(c)
	r, err := http.ReadResponse(br, &http.Request{Method: "CONNECT"})
	if err != nil || r.StatusCode != 200 {
		t.Fatalf("CONNECT %v %v", r, err)
	}
	fmt.Fprintf(c, "GET /transfer HTTP/1.1\r\nHost: %s\r\n\r\n", host)
	r, err = http.ReadResponse(br, &http.Request{Method: "GET"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(r.Body)
	r.Body.Close()
	if err != nil || string(b) != "through CONNECT transfer" {
		t.Fatalf("body=%q err=%v", b, err)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	awaitServe(t, done)
	if _, err := c.Read(make([]byte, 1)); err == nil {
		t.Fatal("CONNECT still open")
	}
}

func TestServeFailureAndStartBindFailureReleaseOwners(t *testing.T) {
	p := lifecycleProxy(t)
	ln := loopback(t)
	ln.Close()
	s, ok := any(p).(interface{ Serve(net.Listener) error })
	if !ok {
		t.Fatal("missing Serve")
	}
	if err := s.Serve(ln); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("closed listener: %v", err)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	p2 := lifecycleProxy(t)
	busy := loopback(t)
	p2.Opts.Addr = busy.Addr().String()
	if p2.Start() == nil {
		t.Fatal("occupied bind accepted")
	}
	p2.Close()
}

type disconnectBlockAddon struct {
	BaseAddon
	entered, release chan struct{}
}

func (a *disconnectBlockAddon) ServerDisconnected(*ConnContext) { close(a.entered); <-a.release }
func TestCloseJoinsUpstreamDisconnectCallback(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "disconnect capture") }))
	defer target.Close()
	p := lifecycleProxy(t)
	a := &disconnectBlockAddon{entered: make(chan struct{}), release: make(chan struct{})}
	p.AddAddon(a)
	ln := loopback(t)
	done := reservedServe(t, p, ln)
	pu, _ := url.Parse("http://" + ln.Addr().String())
	tr := &http.Transport{Proxy: http.ProxyURL(pu)}
	defer tr.CloseIdleConnections()
	c := &http.Client{Transport: tr, Timeout: 5 * time.Second}
	r, err := c.Get(target.URL)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, r.Body)
	r.Body.Close()
	target.CloseClientConnections()
	awaitLifecycle(t, a.entered)
	closed := make(chan struct{})
	go func() { p.Close(); close(closed) }()
	select {
	case <-closed:
		close(a.release)
		t.Fatal("Close returned before upstream callback completed")
	case <-time.After(30 * time.Millisecond):
	}
	close(a.release)
	awaitLifecycle(t, closed)
	awaitServe(t, done)
}

func TestClientDisconnectClosesOwnedUpstream(t *testing.T) {
	upstreamClosed := make(chan struct{}, 1)
	target := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "owned upstream") }))
	target.Config.ConnState = func(c net.Conn, s http.ConnState) {
		if s == http.StateClosed {
			upstreamClosed <- struct{}{}
		}
	}
	target.Start()
	defer target.Close()
	p := lifecycleProxy(t)
	ln := loopback(t)
	done := reservedServe(t, p, ln)
	pu, _ := url.Parse("http://" + ln.Addr().String())
	tr := &http.Transport{Proxy: http.ProxyURL(pu)}
	defer tr.CloseIdleConnections()
	c := &http.Client{Transport: tr, Timeout: 5 * time.Second}
	r, err := c.Get(target.URL)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, r.Body)
	r.Body.Close()
	tr.CloseIdleConnections()
	select {
	case <-upstreamClosed:
	case <-time.After(300 * time.Millisecond):
		t.Fatal("client disconnect leaked upstream keep-alive")
	}
	p.Close()
	awaitServe(t, done)
}

func TestCloseInterruptsUpstreamTLSHandshake(t *testing.T) {
	target := loopback(t)
	handshakeEntered := make(chan struct{})
	targetDone := make(chan struct{})
	go func() {
		defer close(targetDone)
		c, e := target.Accept()
		if e != nil {
			return
		}
		defer c.Close()
		if _, e = io.ReadFull(c, make([]byte, 1)); e != nil {
			return
		}
		close(handshakeEntered)
		io.Copy(io.Discard, c)
	}()
	p := lifecycleProxy(t)
	ln := loopback(t)
	done := reservedServe(t, p, ln)
	c, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(5 * time.Second))
	fmt.Fprintf(c, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", target.Addr(), target.Addr())
	r, err := http.ReadResponse(bufio.NewReader(c), &http.Request{Method: "CONNECT"})
	if err != nil || r.StatusCode != 200 {
		t.Fatalf("CONNECT %v %v", r, err)
	}
	tc := tls.Client(c, &tls.Config{InsecureSkipVerify: true, ServerName: "localhost"})
	clientDone := make(chan error, 1)
	go func() { clientDone <- tc.Handshake() }()
	awaitLifecycle(t, handshakeEntered)
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	awaitServe(t, done)
	awaitLifecycle(t, targetDone)
	select {
	case err := <-clientDone:
		if err == nil {
			t.Fatal("stalled TLS unexpectedly succeeded")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("TLS worker remains")
	}
}

type delayedReadConn struct {
	net.Conn
	readDone, release chan struct{}
	once              sync.Once
}

func (c *delayedReadConn) Read(b []byte) (int, error) {
	n, e := c.Conn.Read(b)
	if e != nil {
		c.once.Do(func() { close(c.readDone); <-c.release })
	}
	return n, e
}
func TestTransferWaitsBothCopyWorkersOnError(t *testing.T) {
	s, remote := net.Pipe()
	client, sender := net.Pipe()
	defer sender.Close()
	defer client.Close()
	defer s.Close()
	server := &delayedReadConn{Conn: s, readDone: make(chan struct{}), release: make(chan struct{})}
	remote.Close()
	done := make(chan struct{})
	go func() { transfer(log.WithField("test", "join"), server, client); close(done) }()
	go sender.Write([]byte("force write error"))
	awaitLifecycle(t, server.readDone)
	select {
	case <-done:
		close(server.release)
		t.Fatal("transfer returned while second reader still running")
	case <-time.After(30 * time.Millisecond):
	}
	close(server.release)
	awaitLifecycle(t, done)
}

func TestConcurrentServeTransfersBothListenerOwnership(t *testing.T) {
	p := lifecycleProxy(t)
	listeners := []net.Listener{loopback(t), loopback(t)}
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, ln := range listeners {
		go func() { <-start; results <- p.Serve(ln) }()
	}
	close(start)
	served := 0
	for _, ln := range listeners {
		c, e := net.DialTimeout("tcp", ln.Addr().String(), time.Second)
		if e != nil {
			continue
		}
		c.SetDeadline(time.Now().Add(5 * time.Second))
		fmt.Fprint(c, "GET / HTTP/1.1\r\nHost: proxy\r\nConnection: close\r\n\r\n")
		_, e = http.ReadResponse(bufio.NewReader(c), nil)
		c.Close()
		if e == nil {
			served++
		}
	}
	if served != 1 {
		t.Fatalf("concurrent Serve admitted %d listeners", served)
	}
	p.Close()
	for range listeners {
		select {
		case <-results:
		case <-time.After(5 * time.Second):
			t.Fatal("Serve result not joined")
		}
	}
	for _, ln := range listeners {
		if _, e := ln.Accept(); !errors.Is(e, net.ErrClosed) {
			t.Fatalf("listener not owned/closed: %v", e)
		}
	}
}

func TestColdStartConcurrentH2Capture(t *testing.T) {
	target := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, r.Proto) }))
	target.EnableHTTP2 = true
	target.StartTLS()
	defer target.Close()
	p := lifecycleProxy(t)
	a := &lifecycleAddon{}
	p.AddAddon(a)
	ln := loopback(t)
	done := reservedServe(t, p, ln)
	pu, _ := url.Parse("http://" + ln.Addr().String())
	pool := x509.NewCertPool()
	ca := p.GetCertificate()
	pool.AddCert(&ca)
	tr := &http.Transport{Proxy: http.ProxyURL(pu), TLSClientConfig: &tls.Config{RootCAs: pool}, ForceAttemptHTTP2: true}
	defer tr.CloseIdleConnections()
	c := &http.Client{Transport: tr, Timeout: 5 * time.Second}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Go(func() {
			r, e := c.Get(strings.Replace(target.URL, "127.0.0.1", "localhost", 1))
			if e != nil {
				t.Error(e)
				return
			}
			b, e := io.ReadAll(r.Body)
			r.Body.Close()
			if e != nil || string(b) != "HTTP/2.0" {
				t.Errorf("h2 capture %q: %v", b, e)
			}
		})
	}
	wg.Wait()
	if a.responses.Load() != 8 {
		t.Fatalf("captures=%d", a.responses.Load())
	}
	p.Close()
	awaitServe(t, done)
}

func captureLifecycleLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	l := log.StandardLogger()
	oldOut, oldLevel, oldFormatter := l.Out, l.GetLevel(), l.Formatter
	out := &bytes.Buffer{}
	l.SetOutput(out)
	l.SetLevel(log.DebugLevel)
	l.SetFormatter(&log.JSONFormatter{})
	t.Cleanup(func() { l.SetOutput(oldOut); l.SetLevel(oldLevel); l.SetFormatter(oldFormatter) })
	return out
}

type lifecycleErrorSignal struct {
	done chan struct{}
	once sync.Once
}

func (h *lifecycleErrorSignal) Levels() []log.Level { return []log.Level{log.ErrorLevel} }
func (h *lifecycleErrorSignal) Fire(*log.Entry) error {
	h.once.Do(func() { close(h.done) })
	return nil
}
func waitErrorLog(t *testing.T) <-chan struct{} {
	t.Helper()
	logger := log.StandardLogger()
	old := logger.ReplaceHooks(make(log.LevelHooks))
	h := &lifecycleErrorSignal{done: make(chan struct{})}
	logger.AddHook(h)
	t.Cleanup(func() { logger.ReplaceHooks(old) })
	return h.done
}

func TestClosingCONNECTLogsExpectedDisconnectBelowError(t *testing.T) {
	out := captureLifecycleLogs(t)
	p := lifecycleProxy(t)
	p.AddAddon(NewUpstreamCertAddon(false))
	ln := loopback(t)
	done := reservedServe(t, p, ln)
	c, e := net.Dial("tcp", ln.Addr().String())
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(5 * time.Second))
	fmt.Fprint(c, "CONNECT localhost:1 HTTP/1.1\r\nHost: localhost:1\r\n\r\n")
	r, e := http.ReadResponse(bufio.NewReader(c), &http.Request{Method: "CONNECT"})
	if e != nil || r.StatusCode != 200 {
		t.Fatalf("CONNECT %v %v", r, e)
	}
	p.Close()
	awaitServe(t, done)
	if strings.Contains(out.String(), `"level":"error"`) {
		t.Fatalf("expected shutdown produced error logs:\n%s", out)
	}
	if !strings.Contains(out.String(), `"level":"debug"`) {
		t.Fatal("expected disconnect classification was not logged")
	}
}

func TestRunningTLSFailureKeepsErrorLog(t *testing.T) {
	out := captureLifecycleLogs(t)
	errorLogged := waitErrorLog(t)
	p := lifecycleProxy(t)
	p.AddAddon(NewUpstreamCertAddon(false))
	ln := loopback(t)
	done := reservedServe(t, p, ln)
	c, e := net.Dial("tcp", ln.Addr().String())
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(5 * time.Second))
	fmt.Fprint(c, "CONNECT localhost:1 HTTP/1.1\r\nHost: localhost:1\r\n\r\n")
	if _, e := http.ReadResponse(bufio.NewReader(c), &http.Request{Method: "CONNECT"}); e != nil {
		t.Fatal(e)
	}
	c.Write([]byte{0x16, 0x03, 0x01, 0, 4, 0xff, 0, 0, 0})
	c.Read(make([]byte, 16))
	awaitLifecycle(t, errorLogged)
	p.Close()
	awaitServe(t, done)
	if !strings.Contains(out.String(), `"level":"error"`) {
		t.Fatalf("real TLS failure was suppressed:\n%s", out)
	}
}

func TestRunningWebSocketFailureKeepsErrorLog(t *testing.T) {
	out := captureLifecycleLogs(t)
	errorLogged := waitErrorLog(t)
	p := lifecycleProxy(t)
	ln := loopback(t)
	done := reservedServe(t, p, ln)
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, e := upgrader.Upgrade(w, r, nil)
		if e != nil {
			return
		}
		c.Close()
	}))
	defer target.Close()
	pu, _ := url.Parse("http://" + ln.Addr().String())
	pool := x509.NewCertPool()
	ca := p.GetCertificate()
	pool.AddCert(&ca)
	dialer := websocket.Dialer{Proxy: http.ProxyURL(pu), TLSClientConfig: &tls.Config{RootCAs: pool}, HandshakeTimeout: 5 * time.Second}
	c, _, e := dialer.Dial(strings.Replace("wss"+target.URL[5:], "127.0.0.1", "localhost", 1), nil)
	if e == nil {
		c.SetReadDeadline(time.Now().Add(5 * time.Second))
		c.ReadMessage()
		c.Close()
	}
	awaitLifecycle(t, errorLogged)
	p.Close()
	awaitServe(t, done)
	if !strings.Contains(out.String(), `"level":"error"`) {
		t.Fatalf("real WSS failure suppressed:\n%s", out)
	}
}

func TestLifecycleErrorLogsRequireClosingAndExpectedCause(t *testing.T) {
	for _, closing := range []bool{false, true} {
		for _, cause := range []struct {
			name     string
			err      error
			expected bool
		}{
			{"cancel", context.Canceled, true}, {"closed", net.ErrClosed, true}, {"websocket1006", &websocket.CloseError{Code: websocket.CloseAbnormalClosure, Text: "unexpected EOF"}, true}, {"tls", errors.New("tls: bad certificate"), false}, {"capture", errors.New("capture persistence failed"), false},
		} {
			t.Run(fmt.Sprintf("closing=%v/%s", closing, cause.name), func(t *testing.T) {
				out := captureLifecycleLogs(t)
				p := lifecycleProxy(t)
				if closing {
					p.Close()
				}
				p.logError(log.WithField("test", "classification"), cause.err, "capture failure")
				want := "error"
				if closing && cause.expected {
					want = "debug"
				}
				if !strings.Contains(out.String(), `"level":"`+want+`"`) {
					t.Fatalf("classification wanted %s:\n%s", want, out)
				}
			})
		}
	}
}
