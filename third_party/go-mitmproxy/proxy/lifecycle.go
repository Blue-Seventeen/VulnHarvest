package proxy

import (
	"context"
	"errors"
	"net"
	"net/http"
	"sync"
)

// All admissions and WaitGroup.Add calls use the same closing gate. The SDK
// owns accepted sockets, outgoing sockets and internal work, not the caller's
// goroutine running Serve. Addons must return; Close waits for their callbacks.
type lifecycle struct {
	mu               sync.Mutex
	started, closing bool
	listener         net.Listener
	conns            map[net.Conn]struct{}
	work             sync.WaitGroup
	ctx              context.Context
	cancel           context.CancelFunc
	closeOnce        sync.Once
	closeErr         error
}

func newLifecycle() *lifecycle {
	ctx, cancel := context.WithCancel(context.Background())
	return &lifecycle{ctx: ctx, cancel: cancel, conns: make(map[net.Conn]struct{})}
}
func (p *Proxy) beginWork() bool {
	p.life.mu.Lock()
	defer p.life.mu.Unlock()
	if p.life.closing {
		return false
	}
	p.life.work.Add(1)
	return true
}
func (p *Proxy) track(c net.Conn) bool {
	p.life.mu.Lock()
	defer p.life.mu.Unlock()
	if p.life.closing {
		return false
	}
	if _, exists := p.life.conns[c]; exists {
		return true
	}
	p.life.conns[c] = struct{}{}
	if callbackConn(c) {
		p.life.work.Add(1)
	}
	return true
}
func (p *Proxy) untrack(c net.Conn) {
	p.life.mu.Lock()
	if _, exists := p.life.conns[c]; exists {
		delete(p.life.conns, c)
		if callbackConn(c) {
			p.life.work.Done()
		}
	}
	p.life.mu.Unlock()
}

func callbackConn(c net.Conn) bool {
	switch c.(type) {
	case *wrapClientConn, *wrapServerConn:
		return true
	}
	return false
}

func (p *Proxy) trackServer(c *wrapServerConn) bool {
	if !p.track(c) {
		c.Close()
		return false
	}
	if !c.connCtx.ClientConn.Conn.(*wrapClientConn).attachUpstream(c) {
		c.Close()
		return false
	}
	return true
}
func (p *Proxy) isClosing() bool { p.life.mu.Lock(); defer p.life.mu.Unlock(); return p.life.closing }

type ownedConn struct {
	net.Conn
	p    *Proxy
	once sync.Once
	err  error
}

func (c *ownedConn) Close() error {
	c.once.Do(func() { c.err = c.Conn.Close(); c.p.untrack(c) })
	return c.err
}

// Serve takes ownership of every non-nil listener, including rejected calls.
// Exactly one Start or Serve is permitted. The raw listener is still wrapped
// by wrapListener, preserving ConnContext and CONNECT/TLS interception.
func (p *Proxy) Serve(ln net.Listener) error {
	if ln == nil {
		return errors.New("proxy: nil listener")
	}
	p.life.mu.Lock()
	if p.life.started || p.life.closing {
		p.life.mu.Unlock()
		ln.Close()
		return errors.New("proxy: already started or closed")
	}
	p.life.started = true
	p.life.listener = ln
	p.life.work.Add(1)
	go func() { defer p.life.work.Done(); p.attacker.start() }()
	p.life.mu.Unlock()
	err := p.entry.serve(ln)
	// Closing the retained raw listener may reach Accept before http.Server's
	// inShutdown flag. Preserve the legacy Start/Serve shutdown sentinel.
	if p.isClosing() && errors.Is(err, net.ErrClosed) {
		err = http.ErrServerClosed
	}
	// A failed accept/Serve must not leave the internal attacker running.
	closeErr := p.Close()
	if closeErr == nil {
		return err
	}
	return errors.Join(err, closeErr)
}

func (p *Proxy) stop(ctx context.Context) error {
	p.life.closeOnce.Do(func() {
		p.life.mu.Lock()
		p.life.closing = true
		ln := p.life.listener
		conns := make([]net.Conn, 0, len(p.life.conns))
		for c := range p.life.conns {
			conns = append(conns, c)
		}
		p.life.mu.Unlock()
		p.life.cancel()
		if ln != nil {
			ln.Close()
		}
		p.attacker.listener.Close()
		// Close raw client sockets first: unblock reads without invoking addons
		// while the registry lock is held. wrapClientConn.Close is idempotent.
		for _, c := range conns {
			if wc, ok := c.(*wrapClientConn); ok {
				wc.Conn.Close()
			} else if sc, ok := c.(*wrapServerConn); ok {
				sc.Conn.Close()
			} else {
				c.Close()
			}
		}
		entryErr := p.entry.server.Close()
		attackerErr := p.attacker.server.Close()
		for _, c := range conns {
			if callbackConn(c) {
				c.Close()
			}
		}
		p.life.work.Wait()
		p.attacker.client.CloseIdleConnections()
		p.life.closeErr = errors.Join(entryErr, attackerErr)
	})
	// Even an expired Shutdown context force-stops AND joins. Thus return never
	// means "cancellation requested, writers still running".
	return errors.Join(ctx.Err(), p.life.closeErr)
}

// Close force-stops and joins all owned work. Concurrent calls wait for the
// same completion. Calling it from an addon callback would deadlock.
func (p *Proxy) Close() error { return p.stop(context.Background()) }

// Shutdown stops and joins the same owners as Close. It does not drain an
// indefinitely live CONNECT/WS stream. A canceled deadline is reported only
// after force-close and joining the actual owners.
func (p *Proxy) Shutdown(ctx context.Context) error { return p.stop(ctx) }
