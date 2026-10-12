// Command artex runs the ARTEX backend: the dual SQLite graph stores,
// the event-driven exploration engine, and the JSON HTTP API consumed by the
// shadcn/ui frontend.
package main

import (
	"context"
	"encoding/json"
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
	"github.com/Autumn-27/artex/runtimeenv"
	"github.com/Autumn-27/artex/selfupdate"
	"github.com/Autumn-27/artex/server"
)

// version is the build version, injected at release time via
// -ldflags "-X main.version=<tag>". Defaults to "dev" for local builds.
var version = "dev"

const banner = `
    _    ____ _____ _______  __
   / \  |  _ \_   _| ____\ \/ /
  / _ \ | |_) || | |  _|  \  /
 / ___ \|  _ < | | | |___ /  \
/_/   \_\_| \_\|_| |_____/_/\_\
`

// printBanner writes the startup banner + version/runtime info to stdout.
func printBanner(addr string) {
	fmt.Print(banner)
	fmt.Println("  AI 自主渗透测试系统")
	fmt.Printf("  版本 %s  ·  %s/%s  ·  %s  ·  监听 %s\n\n",
		version, runtime.GOOS, runtime.GOARCH, runtime.Version(), addr)
}

// main only maps run's result onto the process exit code. The exit code is part
// of the update protocol — the supervising start script reads it to decide
// whether to relaunch us (see selfupdate.ExitRestart) — so the body has to live
// in a function that can *return* rather than os.Exit past its own defers.
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
	var tools *runtimeenv.ToolManager
	if mode == "portable" {
		exe, e := os.Executable()
		if e != nil {
			log.Print(e)
			return 1
		}
		tools, e = runtimeenv.New(runtimeenv.Options{BaseDir: base, StateDir: state, HostPATH: os.Getenv("PATH"), RelayExecutable: exe})
		if e != nil {
			log.Printf("portable tools: %v", e)
			return 1
		}
		defer tools.Close()
		snap, e := tools.Check(ctx, runtimeenv.Config{Paths: config.Load().Tools})
		if e != nil {
			log.Printf("portable check: %s", config.Redact(e.Error()))
			return 1
		}
		for _, tool := range snap.Tools {
			if tool.Status != "available" {
				log.Printf("[runtime] %s: %s (影响 %v)", tool.Name, tool.Status, tool.AffectedFeatures)
			}
		}
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
	mgr, err := server.NewManagerWithOptions(server.ManagerOptions{Mode: mode, DataDir: data, StateDir: state, ProxyAddr: *proxy, ProxyListener: proxyListener})
	if err != nil {
		log.Printf("%s startup: %s", mode, config.Redact(err.Error()))
		return 1
	}
	defer mgr.Close()
	if tools != nil {
		cfg, e := mgr.RuntimeToolConfig()
		if e != nil {
			log.Printf("portable tools config: unavailable")
			return 1
		}
		if _, e = tools.Check(ctx, cfg); e != nil {
			log.Printf("portable tool check: %s", config.Redact(e.Error()))
			return 1
		}
	}
	skills := config.SkillDir()
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
		// Publish the retained listener addresses before the readiness probe so an
		// external supervisor can observe the exact endpoints even while startup
		// is still completing; the final write below is identical and atomic at the
		// file level for this small metadata record.
		raw, _ := json.Marshal(map[string]any{"mode": mode, "http_address": address, "proxy_address": proxyAddress})
		_ = os.WriteFile(filepath.Join(state, "runtime.json"), raw, 0600)
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
		raw, _ := json.Marshal(map[string]any{"mode": mode, "http_address": address, "proxy_address": proxyAddress})
		if err = os.WriteFile(filepath.Join(state, "runtime.json"), raw, 0600); err != nil {
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
	case <-server.RestartRequested():
		code = selfupdate.ExitRestart
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
