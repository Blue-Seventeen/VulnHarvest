// Command artex runs the ARTEX backend: the dual SQLite graph stores,
// the event-driven exploration engine, and the JSON HTTP API consumed by the
// shadcn/ui frontend.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/config"
	"github.com/Autumn-27/artex/internal/toolrelay"
	"github.com/Autumn-27/artex/runtimeenv"
	"github.com/Autumn-27/artex/server"
)

// version is the build version, injected at release time via
// -ldflags "-X main.version=<tag>". Defaults to the product release version.
var version = "v0.0.1"

// Injected by portablebuild. Empty explicitly denotes a development build;
// distribution builds always require the matching external helper asset.
var relayDigest string

const banner = "\n  VulnHarvest "

// printBanner writes the startup banner + version/runtime info to stdout.
func printBanner(addr string) {
	fmt.Println(banner + version)
	fmt.Println("  AI 自主渗透测试系统")
	fmt.Printf("  版本 %s  ·  %s/%s  ·  %s  ·  监听 %s\n\n",
		version, runtime.GOOS, runtime.GOARCH, runtime.Version(), addr)
}

// main maps the return code after deferred runtime cleanup.
func main() {
	os.Exit(run())
}

func run() int {
	if handled, code := runtimeenv.RunRelay(); handled {
		return code
	}
	modeFlag := flag.String("mode", "", "storage mode: portable or postgres")
	addr := flag.String("addr", "", "HTTP listen address")
	dataFlag := flag.String("data", "", "workspace data directory")
	proxy := flag.String("proxy", "127.0.0.1:8788", "traffic recording proxy address (empty disables)")
	noBrowser := flag.Bool("no-browser", false, "print URL without opening the system browser")
	flag.Parse()
	// Pin explicit mode before config lookup, so portable never reads unrelated CWD.
	candidate := *modeFlag
	if candidate == "" {
		candidate = os.Getenv("VULNHARVEST_MODE")
	}
	config.SetRuntimeMode(candidate)
	mode, err := config.ResolveMode(*modeFlag)
	if err != nil {
		log.Printf("runtime mode: %v", err)
		return 1
	}
	config.SetRuntimeMode(mode)
	base := config.BaseDir()
	if mode == "portable" {
		exe, e := os.Executable()
		if e != nil {
			log.Printf("portable executable: %v", e)
			return 1
		}
		base = filepath.Dir(exe)
	}
	base, err = filepath.Abs(base)
	if err != nil {
		log.Printf("%s base: %v", mode, err)
		return 1
	}
	data := *dataFlag
	if data == "" {
		data = filepath.Join(base, "data")
	}
	data, err = filepath.Abs(data)
	if err != nil {
		log.Print(err)
		return 1
	}
	state := base
	if mode == "portable" {
		state = filepath.Join(base, "state")
	}
	skills := config.SkillPath()
	if mode == "portable" {
		if err = server.ValidatePortableRoots(state, data, skills); err != nil {
			log.Printf("portable roots: %v", err)
			return 1
		}
	}
	if *addr == "" {
		if mode == "portable" {
			*addr = "127.0.0.1:8787"
		} else {
			*addr = ":8787"
		}
	}
	server.BuildVersion = version
	server.StartLogCapture()
	// Distribution updates are managed manually. Never bootstrap/swizzle upstream binaries.
	if err = os.MkdirAll(state, 0700); err != nil {
		log.Printf("%s state: %v", mode, err)
		return 1
	}
	if mode == "portable" {
		lock, e := runtimeenv.LockInstance(filepath.Join(state, "instance.lock"))
		if e != nil {
			log.Printf("portable: %v", e)
			return 1
		}
		defer lock.Close()
	}
	sigCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, shutdown := shutdownContext(sigCtx)
	defer shutdown(agent.AbortShutdown)
	exe, err := os.Executable()
	if err != nil {
		log.Print(err)
		return 1
	}
	if relayDigest != "" {
		name := "vulnharvest-tool-relay"
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		exe = filepath.Join(filepath.Dir(exe), "runtime", "bin", name)
		if err = toolrelay.VerifyAsset(exe, relayDigest, runtime.GOOS, runtime.GOARCH); err != nil {
			log.Printf("portable relay: %v", err)
			return 1
		}
	}
	tools, err := runtimeenv.New(runtimeenv.Options{BaseDir: base, StateDir: state, HostPATH: os.Getenv("PATH"), RelayExecutable: exe, RelayDigest: relayDigest})
	if err != nil {
		log.Printf("%s tools: %s", mode, config.Redact(err.Error()))
		return 1
	}
	// Consumers' defers are registered later, so all stop/join before this owner.
	defer tools.Close()
	if _, err = tools.Check(ctx, runtimeenv.Config{Paths: config.Load().Tools}); err != nil {
		log.Printf("%s check: %s", mode, config.Redact(err.Error()))
		return 1
	}
	httpListener, err := runtimeenv.Listen(mode, *addr)
	if err != nil {
		log.Printf("%s HTTP listener: %v", mode, err)
		return 1
	}
	defer httpListener.Close()
	var proxyListener net.Listener
	if *proxy != "" {
		proxyListener, err = runtimeenv.Listen(mode, *proxy)
		if err != nil {
			log.Printf("%s proxy listener: %v", mode, err)
			return 1
		}
		defer proxyListener.Close()
	}
	mgr, err := server.NewManagerWithOptions(server.ManagerOptions{Mode: mode, DataDir: data, StateDir: state, SkillDir: skills, ProxyAddr: *proxy, ProxyListener: proxyListener})
	if err != nil {
		log.Printf("%s startup: %s", mode, config.Redact(err.Error()))
		return 1
	}
	defer mgr.Close()
	if tools != nil {
		cfg, e := mgr.RuntimeToolConfig()
		if e != nil {
			log.Printf("%s tools config: unavailable", mode)
			return 1
		}
		snapshot, e := tools.Check(ctx, cfg)
		if e != nil {
			log.Printf("%s tool check: %s", mode, config.Redact(e.Error()))
			return 1
		}
		logToolProblems(snapshot)
	}
	if err = os.MkdirAll(skills, 0755); err != nil {
		log.Printf("%s skills: %v", mode, err)
		return 1
	}
	srv, err := server.NewWithOptions(ctx, mgr, skills, data, state)
	if err != nil {
		log.Printf("%s server: %s", mode, config.Redact(err.Error()))
		return 1
	}
	defer srv.Shutdown(context.Background())
	proxyAddress := ""
	if proxyListener != nil {
		proxyAddress = proxyListener.Addr().String()
	}
	address := httpListener.Addr().String()
	srv.SetRuntime(tools, address, proxyAddress)
	if mode == "portable" {
		// Publish complete addresses before probing, then mark ready only after
		// both retained services respond. Successful reads see a complete record.
		if err = writeRuntimeMetadata(filepath.Join(state, "runtime.json"), map[string]any{"mode": mode, "http_address": address, "proxy_address": proxyAddress, "ready": false}); err != nil {
			log.Printf("portable runtime metadata: %v", err)
			return 1
		}
	}
	httpSrv := &http.Server{Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}
	httpDone := make(chan error, 1)
	go func() { httpDone <- httpSrv.Serve(httpListener) }()
	// Prove both retained services accept requests before publishing ready state.
	url := "http://" + browserAddress(address)
	if err = waitReady(ctx, url, proxyAddress); err != nil {
		shutdown(agent.AbortShutdown)
		httpSrv.Close()
		<-httpDone
		log.Printf("%s ready: %v", mode, err)
		return 1
	}
	if mode == "portable" {
		if err = writeRuntimeMetadata(filepath.Join(state, "runtime.json"), map[string]any{"mode": mode, "http_address": address, "proxy_address": proxyAddress, "ready": true}); err != nil {
			shutdown(agent.AbortShutdown)
			httpSrv.Close()
			<-httpDone
			log.Print(err)
			return 1
		}
	}
	printBanner(address)
	fmt.Printf("%s ready: %s\n", mode, url)
	if mode == "portable" && !*noBrowser {
		if err = openBrowser(url); err != nil {
			log.Printf("[browser] 打开失败，请访问 %s: %v", url, err)
		}
	}
	code := 0
	select {
	case <-ctx.Done():
	case err := <-httpDone:
		if err != nil && err != http.ErrServerClosed {
			log.Printf("serve: %v", err)
			code = 1
		}
		httpDone = nil
	}
	shutdown(agent.AbortShutdown)
	log.Println("shutting down...")
	closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err = httpSrv.Shutdown(closeCtx); err != nil {
		_ = httpSrv.Close()
	}
	if httpDone != nil {
		<-httpDone
	}
	_ = srv.Shutdown(context.Background())
	return code
}

func browserAddress(address string) string {
	host, port, e := net.SplitHostPort(address)
	if e != nil {
		return address
	}
	if host == "" || host == "::" || host == "0.0.0.0" {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
}
func waitReady(ctx context.Context, url, proxy string) error {
	client := &http.Client{Timeout: time.Second}
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		response, err := client.Get(url + "/api/health")
		if err == nil {
			response.Body.Close()
			if response.StatusCode == 200 {
				if proxy == "" {
					return nil
				}
				conn, e := net.DialTimeout("tcp", proxy, time.Second)
				if e == nil {
					conn.Close()
					return nil
				}
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("service readiness timeout")
		case <-tick.C:
		}
	}
}
func openBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32.exe", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	return cmd.Run()
}

// shutdownContext deliberately does not derive from signalCtx. If it did, the
// parent's plain context.Canceled could win the race before AbortShutdown was
// attached to the child, losing the diagnostic cause in every running Agent.
func shutdownContext(signalCtx context.Context) (context.Context, context.CancelCauseFunc) {
	ctx, shutdown := context.WithCancelCause(context.Background())
	go func() {
		select {
		case <-signalCtx.Done():
			shutdown(agent.AbortShutdown)
		case <-ctx.Done():
		}
	}()
	return ctx, shutdown
}
