package proxy

import (
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
)

type responseCloseAddon struct {
	BaseAddon
	separate      bool
	contexts      chan *ConnContext
	headers       chan struct{}
	serverEntered chan struct{}
	reentered     chan struct{}
	serverRelease chan struct{}
	clientEntered chan struct{}
	clientRelease chan struct{}
	servers       atomic.Int32
	disconnects   atomic.Int32
	mu            sync.Mutex
	order         []string
	headersOnce   sync.Once
	serverOnce    sync.Once
	clientOnce    sync.Once
}

func newResponseCloseAddon() *responseCloseAddon {
	return &responseCloseAddon{contexts: make(chan *ConnContext, 1), headers: make(chan struct{}),
		serverEntered: make(chan struct{}), reentered: make(chan struct{}), serverRelease: make(chan struct{}),
		clientEntered: make(chan struct{}), clientRelease: make(chan struct{})}
}

func (a *responseCloseAddon) Requestheaders(f *Flow) {
	if f.Request.Method != "CONNECT" {
		f.UseSeparateClient = a.separate
	}
}

func (a *responseCloseAddon) Responseheaders(f *Flow) {
	if f.Request.Method != "CONNECT" {
		a.contexts <- f.ConnContext
		<-a.headers
	}
}

func (a *responseCloseAddon) ServerConnected(*ConnContext) { a.servers.Add(1) }
func (a *responseCloseAddon) record(event string) {
	a.mu.Lock()
	a.order = append(a.order, event)
	a.mu.Unlock()
}

func (a *responseCloseAddon) ServerDisconnected(c *ConnContext) {
	a.disconnects.Add(1)
	close(a.serverEntered)
	c.ClientConn.Conn.Close()
	close(a.reentered)
	<-a.serverRelease
	a.record("server returned")
}

func (a *responseCloseAddon) ClientDisconnected(*ClientConn) {
	a.record("client entered")
	close(a.clientEntered)
	<-a.clientRelease
}

func (a *responseCloseAddon) unblock() {
	a.releaseHeaders()
	a.releaseServer()
	a.releaseClient()
}

func (a *responseCloseAddon) releaseHeaders() { a.headersOnce.Do(func() { close(a.headers) }) }
func (a *responseCloseAddon) releaseServer()  { a.serverOnce.Do(func() { close(a.serverRelease) }) }
func (a *responseCloseAddon) releaseClient()  { a.clientOnce.Do(func() { close(a.clientRelease) }) }

// A failed replay against the old implementation must release its circular
// wait before the fixture joins Proxy.Close. Fixed implementations lack it.
func releaseLegacyResponseWait(c *ConnContext) {
	if legacy, ok := any(c).(interface{ signalServerCloseDone() }); ok {
		legacy.signalServerCloseDone()
	}
}

func responseCloseClient(t *testing.T, p *Proxy, proxyURL string) *http.Client {
	t.Helper()
	pu, err := url.Parse(proxyURL)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	ca := p.GetCertificate()
	pool.AddCert(&ca)
	tr := &http.Transport{Proxy: http.ProxyURL(pu), TLSClientConfig: &tls.Config{RootCAs: pool}}
	t.Cleanup(tr.CloseIdleConnections)
	return &http.Client{Transport: tr, Timeout: 5 * time.Second}
}

func assertResponseCloseBlocked(t *testing.T, ch <-chan struct{}, message string) {
	t.Helper()
	select {
	case <-ch:
		t.Error(message)
	case <-time.After(30 * time.Millisecond):
	}
}

func TestResponseCloseServerCallbackCanCloseClient(t *testing.T) {
	for _, protocol := range []string{"http", "https"} {
		t.Run(protocol, func(t *testing.T) {
			bodyRelease := make(chan struct{})
			var bodyOnce sync.Once
			unblockBody := func() { bodyOnce.Do(func() { close(bodyRelease) }) }
			up := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Connection", "close")
				w.Header().Set("Content-Length", "7")
				w.WriteHeader(http.StatusOK)
				w.(http.Flusher).Flush()
				<-bodyRelease
				io.WriteString(w, "payload")
			}))
			if protocol == "https" {
				up.StartTLS()
			} else {
				up.Start()
			}
			t.Cleanup(up.Close)
			p := lifecycleProxy(t)
			a := newResponseCloseAddon()
			p.AddAddon(a)
			ln := loopback(t)
			served := reservedServe(t, p, ln)
			client := responseCloseClient(t, p, "http://"+ln.Addr().String())
			requested := make(chan struct{})
			go func() {
				defer close(requested)
				r, err := client.Get(strings.Replace(up.URL, "127.0.0.1", "localhost", 1))
				if err == nil {
					io.Copy(io.Discard, r.Body)
					r.Body.Close()
				}
			}()
			var c *ConnContext
			var serverClosed chan struct{}
			serveJoined := false
			t.Cleanup(func() {
				if c != nil {
					releaseLegacyResponseWait(c)
				}
				unblockBody()
				a.unblock()
				p.Close()
				awaitLifecycle(t, requested)
				if serverClosed != nil {
					awaitLifecycle(t, serverClosed)
				}
				if !serveJoined {
					awaitServe(t, served)
				}
			})
			select {
			case c = <-a.contexts:
			case <-time.After(5 * time.Second):
				t.Fatal("response headers were not captured")
			}
			if !c.closeAfterResponse.Load() {
				t.Fatal("fixture did not reach the response-close branch")
			}
			serverClosed = make(chan struct{})
			go func() { c.ServerConn.Conn.Close(); close(serverClosed) }()
			awaitLifecycle(t, a.serverEntered)
			select {
			case <-a.reentered:
			case <-time.After(500 * time.Millisecond):
				t.Error("ServerDisconnected synchronously closing the client waited for its own callback")
				releaseLegacyResponseWait(c)
				a.unblock()
				unblockBody()
				awaitLifecycle(t, a.reentered)
				awaitLifecycle(t, serverClosed)
				p.Close()
				awaitLifecycle(t, requested)
				awaitServe(t, served)
				serveJoined = true
				return
			}
			assertResponseCloseBlocked(t, a.clientEntered, "ClientDisconnected ran before ServerDisconnected returned")
			closed := make(chan struct{})
			go func() { p.Close(); close(closed) }()
			assertResponseCloseBlocked(t, closed, "Proxy.Close returned while disconnect callbacks remained active")
			unblockBody()
			a.releaseHeaders()
			a.releaseServer()
			awaitLifecycle(t, a.clientEntered)
			assertResponseCloseBlocked(t, closed, "Proxy.Close returned before ClientDisconnected completed")
			a.releaseClient()
			awaitLifecycle(t, closed)
			awaitLifecycle(t, serverClosed)
			awaitLifecycle(t, requested)
			awaitServe(t, served)
			serveJoined = true
			a.mu.Lock()
			defer a.mu.Unlock()
			if fmt.Sprint(a.order) != "[server returned client entered]" {
				t.Errorf("response disconnect callback order=%v", a.order)
			}
		})
	}
}

func TestSeparateClientResponseCloseJoinsClientCallback(t *testing.T) {
	up := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "separate payload") }))
	up.Config.SetKeepAlivesEnabled(false)
	up.Start()
	t.Cleanup(up.Close)
	p := lifecycleProxy(t)
	a := newResponseCloseAddon()
	a.separate = true
	p.AddAddon(a)
	c, br, served, _ := serveHTTPClose(t, p)
	var connCtx *ConnContext
	serveJoined := false
	t.Cleanup(func() {
		if connCtx != nil {
			releaseLegacyResponseWait(connCtx)
		}
		a.unblock()
		p.Close()
		if !serveJoined {
			awaitServe(t, served)
		}
	})
	sendHTTPClose(t, c, up.URL)
	select {
	case connCtx = <-a.contexts:
	case <-time.After(5 * time.Second):
		t.Fatal("separate response headers were not captured")
	}
	if !connCtx.closeAfterResponse.Load() || connCtx.ServerConn != nil {
		t.Fatal("fixture did not reach a closing response without a server callback owner")
	}
	a.releaseHeaders()
	expectHTTPCloseBody(t, readHTTPClose(br), []byte("separate payload"))
	closed := make(chan struct{})
	go func() { p.Close(); close(closed) }()
	select {
	case <-a.clientEntered:
	case <-time.After(500 * time.Millisecond):
		t.Error("separate-client response delivered but ClientDisconnected waited for a nonexistent server owner")
		releaseLegacyResponseWait(connCtx)
		awaitLifecycle(t, a.clientEntered)
	}
	assertResponseCloseBlocked(t, closed, "Proxy.Close returned before ClientDisconnected completed")
	a.releaseClient()
	awaitLifecycle(t, closed)
	awaitServe(t, served)
	serveJoined = true
	if a.servers.Load() != 0 || a.disconnects.Load() != 0 {
		t.Errorf("separate transport unexpectedly emitted server callbacks: connected=%d disconnected=%d", a.servers.Load(), a.disconnects.Load())
	}
}

type delayedClosedListener struct {
	net.Listener
	acceptEntered, acceptRelease, closedAgain chan struct{}
	acceptOnce, releaseOnce, closeOnce        sync.Once
}

func (l *delayedClosedListener) Accept() (net.Conn, error) {
	l.acceptOnce.Do(func() { close(l.acceptEntered) })
	c, err := l.Listener.Accept()
	if errors.Is(err, net.ErrClosed) {
		<-l.acceptRelease
	}
	return c, err
}

func (l *delayedClosedListener) Close() error {
	err := l.Listener.Close()
	if errors.Is(err, net.ErrClosed) {
		l.closeOnce.Do(func() { close(l.closedAgain) })
	}
	return err
}

func (l *delayedClosedListener) release() { l.releaseOnce.Do(func() { close(l.acceptRelease) }) }

func TestCloseIgnoresAlreadyClosedOwnedListener(t *testing.T) {
	p := lifecycleProxy(t)
	ln := &delayedClosedListener{Listener: loopback(t), acceptEntered: make(chan struct{}),
		acceptRelease: make(chan struct{}), closedAgain: make(chan struct{})}
	served := reservedServe(t, p, ln)
	serveJoined := false
	t.Cleanup(func() {
		ln.release()
		ln.Close()
		p.Close()
		if !serveJoined {
			awaitServe(t, served)
		}
	})
	awaitLifecycle(t, ln.acceptEntered)
	closed := make(chan error, 1)
	go func() { closed <- p.Close() }()
	// Keep the real listener in http.Server's registry through its second close.
	awaitLifecycle(t, ln.closedAgain)
	ln.release()
	select {
	case err := <-closed:
		if err != nil {
			t.Errorf("Close returned an expected duplicate-listener-close error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not join its owned work")
	}
	awaitServe(t, served)
	serveJoined = true
}
