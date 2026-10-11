package traffic

import (
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	mproxy "github.com/lqqyt2423/go-mitmproxy/proxy"
)

type blockingCapture struct {
	mproxy.BaseAddon
	entered, release chan struct{}
}

func (a *blockingCapture) Response(*mproxy.Flow) { close(a.entered); <-a.release }

// Removing proxy stop+join from Traffic.Close would return while the blocking
// addon still owns a real capture, then attempt to record into a closed index.
func TestTrafficReservedListenerCloseWaitsCaptureBeforeIndex(t *testing.T) {
	tr, err := Open(t.TempDir(), "127.0.0.1:1")
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()
	a := &blockingCapture{entered: make(chan struct{}), release: make(chan struct{})}
	tr.proxy.Addons = append([]mproxy.Addon{a}, tr.proxy.Addons...)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	s, ok := any(tr).(interface{ Serve(net.Listener) error })
	if !ok {
		t.Fatal("Traffic lacks reserved listener Serve")
	}
	done := make(chan error, 1)
	go func() { done <- s.Serve(ln) }()
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, strings.Repeat("real captured response", 18000))
	}))
	defer target.Close()
	pu, _ := url.Parse("http://" + ln.Addr().String())
	transport := &http.Transport{Proxy: http.ProxyURL(pu)}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	requestDone := make(chan struct{})
	go func() {
		defer close(requestDone)
		r, e := client.Get(target.URL)
		if e == nil {
			io.Copy(io.Discard, r.Body)
			r.Body.Close()
		}
	}()
	select {
	case <-a.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("capture not entered")
	}
	if tr.ProxyAddr() != pu.String() {
		t.Fatalf("actual ProxyAddr=%q want %q", tr.ProxyAddr(), pu)
	}
	closed := make(chan error, 1)
	go func() { closed <- tr.Close() }()
	select {
	case e := <-closed:
		t.Fatalf("closed index before writer completed: %v", e)
	case <-time.After(40 * time.Millisecond):
	}
	if err := tr.DB().Ping(); err != nil {
		t.Fatalf("index closed during capture: %v", err)
	}
	close(a.release)
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not join")
	}
	select {
	case <-requestDone:
	case <-time.After(5 * time.Second):
		t.Fatal("client stuck")
	}
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, net.ErrClosed) {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve stuck")
	}
	if err := tr.DB().Ping(); err == nil {
		t.Fatal("index remains open")
	}
	index := filepath.Join(tr.dir, "_index", "index.sqlite")
	before, err := os.ReadFile(index)
	if err != nil {
		t.Fatal(err)
	}
	treeBefore := snapshotTrafficFiles(t, tr.dir)
	if _, err := client.Get(target.URL); err == nil {
		t.Fatal("proxy accepted after Close")
	}
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Go(func() {
			if err := tr.Close(); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	after, err := os.ReadFile(index)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("post-Close index write")
	}
	if !reflect.DeepEqual(treeBefore, snapshotTrafficFiles(t, tr.dir)) {
		t.Fatal("post-Close file write")
	}
}

func TestTrafficShutdownCanceledClosesIndex(t *testing.T) {
	tr, err := Open(t.TempDir(), "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()
	s, ok := any(tr).(interface{ Shutdown(context.Context) error })
	if !ok {
		t.Fatal("missing Traffic.Shutdown")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.Shutdown(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Shutdown: %v", err)
	}
	if tr.DB().Ping() == nil {
		t.Fatal("index remains open")
	}
}

func TestTrafficCloseWaitsActiveDeleteStage(t *testing.T) {
	tr, err := Open(t.TempDir(), "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()
	stage, err := tr.StageDeleteHostsExact([]string{"owned.example"})
	if err != nil {
		t.Fatal(err)
	}
	closed := make(chan error, 1)
	go func() { closed <- tr.Close() }()
	select {
	case err := <-closed:
		stage.Rollback()
		t.Fatalf("Close outran active delete stage: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	if err := stage.Rollback(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close stuck")
	}
	if _, _, err := tr.DeleteAll(); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("post-Close deletion: %v", err)
	}
	if err := tr.RecoverHostDeleteStages(nil); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("post-Close recovery: %v", err)
	}
}

func snapshotTrafficFiles(t *testing.T, dir string) map[string][32]byte {
	t.Helper()
	snapshot := make(map[string][32]byte)
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if d.IsDir() {
			return nil
		}
		b, e := os.ReadFile(p)
		if e != nil {
			return e
		}
		relative, e := filepath.Rel(dir, p)
		if e != nil {
			return e
		}
		snapshot[relative] = sha256.Sum256(b)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func TestTrafficRejectedServeRetainsActualAddress(t *testing.T) {
	tr, err := Open(t.TempDir(), "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()
	first, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	done := make(chan error, 1)
	go func() { done <- tr.Serve(first) }()
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "captured address") }))
	defer target.Close()
	pu, _ := url.Parse("http://" + first.Addr().String())
	transport := &http.Transport{Proxy: http.ProxyURL(pu)}
	defer transport.CloseIdleConnections()
	c := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	r, err := c.Get(target.URL)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, r.Body)
	r.Body.Close()
	second, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if tr.Serve(second) == nil {
		t.Fatal("second Serve accepted")
	}
	if tr.ProxyAddr() != pu.String() {
		t.Fatalf("rejected listener changed actual address to %s", tr.ProxyAddr())
	}
	if _, err := second.Accept(); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("rejected listener not closed: %v", err)
	}
	tr.Close()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Serve stuck")
	}
}

func TestTrafficStartPreservesLegacyLoopbackClientAddress(t *testing.T) {
	tr, err := Open(t.TempDir(), ":0")
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()
	done := make(chan error, 1)
	go func() { done <- tr.Start() }()
	deadline := time.Now().Add(5 * time.Second)
	for strings.HasSuffix(tr.ProxyAddr(), ":0") && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	addr := tr.ProxyAddr()
	if !strings.HasPrefix(addr, "http://127.0.0.1:") || strings.HasSuffix(addr, ":0") {
		t.Fatalf("legacy client address=%s", addr)
	}
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "legacy listener") }))
	defer target.Close()
	pu, _ := url.Parse(addr)
	transport := &http.Transport{Proxy: http.ProxyURL(pu)}
	defer transport.CloseIdleConnections()
	c := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	r, err := c.Get(target.URL)
	if err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(r.Body)
	r.Body.Close()
	if err != nil || string(b) != "legacy listener" {
		t.Fatalf("response %q: %v", b, err)
	}
	tr.Close()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Start not joined")
	}
}
