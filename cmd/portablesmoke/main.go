// Command portablesmoke validates the actual embedded portable archive natively.
// Every mutation is isolated under an owned temporary root and loopback ports.
package main

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	if len(os.Args) != 2 {
		return fmt.Errorf("usage: portablesmoke <native-portable.zip>")
	}
	fmt.Printf("NATIVE SMOKE host=%s/%s Go=%s archive=%s\n", runtime.GOOS, runtime.GOARCH, runtime.Version(), os.Args[1])
	temp, err := os.MkdirTemp(os.Getenv("VULNHARVEST_SMOKE_TEMP"), "vulnharvest-native-smoke-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(temp)
	first := filepath.Join(temp, "初始 空格")
	second := filepath.Join(temp, "搬迁 space")
	cwd := filepath.Join(temp, "unrelated cwd")
	if err = os.MkdirAll(cwd, 0700); err != nil {
		return err
	}
	root, err := extract(os.Args[1], first)
	if err != nil {
		return err
	}
	binary := filepath.Join(root, "artex")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	httpOwner, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer httpOwner.Close()
	proxyOwner, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer proxyOwner.Close()
	args := []string{"-mode", "portable", "-no-browser", "-addr", httpOwner.Addr().String(), "-proxy", proxyOwner.Addr().String()}
	child, err := start(binary, cwd, args)
	if err != nil {
		return err
	}
	defer child.stop()
	address, proxy, err := child.ready(root)
	if err != nil {
		return err
	}
	if address == httpOwner.Addr().String() || proxy == proxyOwner.Addr().String() {
		return fmt.Errorf("occupied port was reused")
	}
	fmt.Println("PASS occupied HTTP/proxy ports retained; selected", address, proxy)
	if _, err = call(address, "GET", "/api/runtime", "", nil, 401); err != nil {
		return err
	}
	html, err := call(address, "GET", "/system/settings/", "", nil, 200)
	if err != nil {
		return err
	}
	if !bytes.Contains(html, []byte("VulnHarvest v0.0.1")) {
		return fmt.Errorf("product settings page not embedded")
	}
	// The auth layout renders a loading placeholder until hydration. Verify the
	// page's actual served JS, including its runtime API and card implementation.
	var assets bytes.Buffer
	for _, match := range regexp.MustCompile(`(?:src|href)="(/_next/[^" ]+\.js)"`).FindAllSubmatch(html, -1) {
		asset, e := call(address, "GET", string(match[1]), "", nil, 200)
		if e != nil {
			return e
		}
		assets.Write(asset)
	}
	if !bytes.Contains(assets.Bytes(), []byte("运行环境")) || !bytes.Contains(assets.Bytes(), []byte("/runtime/check")) {
		return fmt.Errorf("production runtime card assets not embedded")
	}
	fmt.Println("PASS embedded production settings page and runtime card/API assets")
	var auth struct {
		Token string `json:"token"`
	}
	raw, err := call(address, "POST", "/api/auth/init", "", map[string]string{"password": "PortableSmoke-Only-Password"}, 200)
	if err != nil {
		return err
	}
	if err = json.Unmarshal(raw, &auth); err != nil {
		return err
	}
	if auth.Token == "" {
		return fmt.Errorf("missing initialization token")
	}
	raw, err = call(address, "POST", "/api/runtime/check", auth.Token, nil, 200)
	if err != nil {
		return err
	}
	var rt struct {
		Mode        string          `json:"mode"`
		Background  map[string]bool `json:"background"`
		Environment struct {
			Tools map[string]struct {
				Status string `json:"status"`
			} `json:"tools"`
		} `json:"environment"`
	}
	if err = json.Unmarshal(raw, &rt); err != nil {
		return err
	}
	if rt.Mode != "portable" {
		return fmt.Errorf("wrong mode %s", rt.Mode)
	}
	for feature, on := range rt.Background {
		if on {
			return fmt.Errorf("background %s auto-started", feature)
		}
	}
	fmt.Printf("PASS authenticated runtime rescan with empty PATH, tool states=%s\n", rawToolStates(rt.Environment.Tools))
	printRelayFootprint(root, "after startup + authenticated rescan")
	for _, route := range []struct{ method, path string }{{"GET", "check?force=1"}, {"POST", "apply"}, {"POST", "rollback"}, {"GET", "stream"}} {
		if _, err = call(address, route.method, "/api/update/"+route.path, auth.Token, nil, 403); err != nil {
			return err
		}
	}
	lockCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	lockCmd := exec.CommandContext(lockCtx, binary, args...)
	lockCmd.Dir = cwd
	lockCmd.Env = cleanEnvironment()
	locked, lockErr := lockCmd.CombinedOutput()
	if lockErr == nil || !bytes.Contains(locked, []byte("instance")) {
		return fmt.Errorf("second instance did not reject state lock: %v %s", lockErr, locked)
	}
	fmt.Println("PASS state lock and all authenticated upstream update routes denied")
	marker := filepath.Join(root, "data", "portable-smoke-marker.txt")
	if err = os.WriteFile(marker, []byte("copy-whole-directory"), 0600); err != nil {
		return err
	}
	child.stop()
	printRelayFootprint(root, "after process stop")
	if err = copyDirectory(root, second); err != nil {
		return err
	}
	os.Remove(filepath.Join(second, "state", "runtime.json"))
	relocated := filepath.Join(second, filepath.Base(binary))
	child2, err := start(relocated, cwd, args)
	if err != nil {
		return err
	}
	defer child2.stop()
	address, _, err = child2.ready(second)
	if err != nil {
		return err
	}
	printRelayFootprint(second, "after moved restart")
	raw, err = call(address, "POST", "/api/auth/login", "", map[string]string{"username": "ARTEX", "password": "PortableSmoke-Only-Password"}, 200)
	if err != nil {
		return err
	}
	if !bytes.Contains(raw, []byte("token")) {
		return fmt.Errorf("SQLite authentication did not survive move")
	}
	content, err := os.ReadFile(filepath.Join(second, "data", "portable-smoke-marker.txt"))
	if err != nil || string(content) != "copy-whole-directory" {
		return fmt.Errorf("data marker lost: %v", err)
	}
	child2.stop()
	// Launch scripts must preserve an actual binary's failing exit and stderr.
	launcher := filepath.Join(second, "start.sh")
	name := launcher
	launchArgs := []string{"-invalid-vulnharvest-smoke-flag"}
	if runtime.GOOS == "windows" {
		name = os.Getenv("COMSPEC")
		if name == "" {
			name = filepath.Join(os.Getenv("SystemRoot"), "System32", "cmd.exe")
		}
		launcher = filepath.Join(second, "start.bat")
		launchArgs = []string{"/d", "/c", "call", launcher, "-invalid-vulnharvest-smoke-flag"}
	}
	launchCtx, launchCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer launchCancel()
	launchCmd := exec.CommandContext(launchCtx, name, launchArgs...)
	launchCmd.Dir = cwd
	launchCmd.Env = cleanEnvironment()
	output, launchErr := launchCmd.CombinedOutput()
	if launchErr == nil || launchCtx.Err() != nil || !bytes.Contains(output, []byte("flag provided but not defined")) {
		return fmt.Errorf("launcher did not preserve failure: %v %s", launchErr, output)
	}
	fmt.Println("PASS Chinese/space cold-copy migration, persistent SQLite login/data, launcher stderr/exit")
	return nil
}

func printRelayFootprint(root, label string) {
	var count int
	var bytes int64
	_ = filepath.Walk(filepath.Join(root, "state", "runtime-path"), func(path string, info os.FileInfo, err error) error {
		if err == nil && info.Mode().IsRegular() && !strings.HasPrefix(info.Name(), ".") {
			count++
			bytes += info.Size()
		}
		return nil
	})
	fmt.Printf("RELAY FOOTPRINT %s aliases=%d logical_bytes=%d (Unix hardlinks may share blocks)\n", label, count, bytes)
}
func rawToolStates(tools map[string]struct {
	Status string `json:"status"`
}) string {
	b, _ := json.Marshal(tools)
	return string(b)
}
func cleanEnvironment() []string {
	env := []string{"PATH=", "ARTEX_PG_DSN=INVALID-PORTABLE-IGNORES-POSTGRES", "VULNHARVEST_MODE=postgres"}
	for _, key := range []string{"SystemRoot", "WINDIR", "COMSPEC", "TEMP", "TMP", "PATHEXT"} {
		if v := os.Getenv(key); v != "" {
			env = append(env, key+"="+v)
		}
	}
	return env
}

type process struct {
	cmd     *exec.Cmd
	done    chan error
	log     *os.File
	stopped bool
}

func start(binary, cwd string, args []string) (*process, error) {
	log, err := os.CreateTemp(filepath.Dir(cwd), "process-log-")
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(binary, args...)
	cmd.Dir = cwd
	cmd.Env = cleanEnvironment()
	cmd.Stdout = log
	cmd.Stderr = log
	if err = cmd.Start(); err != nil {
		log.Close()
		return nil, err
	}
	p := &process{cmd: cmd, done: make(chan error, 1), log: log}
	go func() { p.done <- cmd.Wait() }()
	return p, nil
}
func (p *process) stop() {
	if p.stopped {
		return
	}
	p.stopped = true
	if runtime.GOOS == "windows" {
		fmt.Println("Windows smoke stop: forced termination; graceful console signal not asserted")
		p.cmd.Process.Kill()
	} else {
		p.cmd.Process.Signal(os.Interrupt)
	}
	select {
	case <-p.done:
	case <-time.After(20 * time.Second):
		p.cmd.Process.Kill()
		<-p.done
	}
	p.log.Close()
}
func (p *process) ready(root string) (string, string, error) {
	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-p.done:
			p.stopped = true
			p.log.Close()
			b, _ := os.ReadFile(p.log.Name())
			return "", "", fmt.Errorf("process exited: %v %s", err, b)
		default:
		}
		raw, _ := os.ReadFile(filepath.Join(root, "state", "runtime.json"))
		var info struct {
			HTTP  string `json:"http_address"`
			Proxy string `json:"proxy_address"`
			Ready bool   `json:"ready"`
		}
		if json.Unmarshal(raw, &info) == nil && info.Ready {
			health, err := call(info.HTTP, "GET", "/api/health", "", nil, 200)
			if err == nil && bytes.Contains(health, []byte("VulnHarvest")) {
				return info.HTTP, info.Proxy, nil
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	b, _ := os.ReadFile(p.log.Name())
	return "", "", fmt.Errorf("readiness timeout: %s", b)
}
func call(address, method, path, token string, body any, want int) ([]byte, error) {
	b, _ := json.Marshal(body)
	req, err := http.NewRequest(method, "http://"+address+path, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != want {
		return nil, fmt.Errorf("%s %s status=%d want=%d body=%s", method, path, resp.StatusCode, want, raw)
	}
	return raw, nil
}
func extract(archive, dir string) (string, error) {
	r, err := zip.OpenReader(archive)
	if err != nil {
		return "", err
	}
	defer r.Close()
	root := ""
	for _, f := range r.File {
		clean := filepath.Clean(filepath.FromSlash(f.Name))
		if filepath.IsAbs(clean) || strings.HasPrefix(clean, "..") {
			return "", fmt.Errorf("unsafe archive path")
		}
		parts := strings.SplitN(filepath.ToSlash(clean), "/", 2)
		if root == "" {
			root = filepath.Join(dir, parts[0])
		}
		dst := filepath.Join(dir, clean)
		if f.FileInfo().IsDir() {
			if err = os.MkdirAll(dst, 0755); err != nil {
				return "", err
			}
			continue
		}
		if err = os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
			return "", err
		}
		in, e := f.Open()
		if e != nil {
			return "", e
		}
		out, e := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, f.Mode().Perm())
		if e != nil {
			in.Close()
			return "", e
		}
		_, e = io.Copy(out, in)
		in.Close()
		out.Close()
		if e != nil {
			return "", e
		}
	}
	return root, nil
}
func copyDirectory(source, dest string) error {
	return filepath.Walk(source, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		dst := filepath.Join(dest, rel)
		if info.IsDir() {
			return os.MkdirAll(dst, 0700)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("unexpected runtime artifact %s", path)
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode().Perm())
		if err != nil {
			return err
		}
		_, err = io.Copy(out, in)
		closeErr := out.Close()
		if err != nil {
			return err
		}
		return closeErr
	})
}
