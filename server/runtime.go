package server

import (
	"context"
	"fmt"
	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/config"
	"github.com/Autumn-27/artex/runtimeenv"
	"io"
	"net/http"
	"sync/atomic"
	"time"
)

// RuntimeInfo is session metadata only; never include saved MCP env/args or keys.
type RuntimeInfo struct {
	Mode                  string              `json:"mode"`
	HTTP                  string              `json:"http_address"`
	Proxy                 string              `json:"proxy_address"`
	Tools                 runtimeenv.Snapshot `json:"environment"`
	Background            map[string]bool     `json:"background"`
	PendingDeliveryEffect string              `json:"pending_delivery_effect"`
}

func (s *Server) SetRuntime(tools *runtimeenv.ToolManager, httpAddress, proxyAddress string) {
	s.runtimeMu.Lock()
	defer s.runtimeMu.Unlock()
	s.runtimeTools = tools
	s.httpAddress = httpAddress
	s.proxyAddress = proxyAddress
}
func (s *Server) runtimeInfo() RuntimeInfo {
	s.runtimeMu.Lock()
	defer s.runtimeMu.Unlock()
	info := RuntimeInfo{Mode: s.m.mode, HTTP: s.httpAddress, Proxy: s.proxyAddress, Background: map[string]bool{}, PendingDeliveryEffect: "恢复通知将处理保留的待投递事件和通知；本会话恢复不修改已保存启用配置"}
	for k, v := range s.background {
		info.Background[k] = v
	}
	if s.runtimeTools != nil {
		info.Tools = s.runtimeTools.Snapshot()
	}
	return info
}
func (s *Server) getRuntime(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.runtimeInfo())
}
func (s *Server) checkRuntime(w http.ResponseWriter, r *http.Request) {
	s.runtimeMu.Lock()
	tm := s.runtimeTools
	s.runtimeMu.Unlock()
	if tm == nil {
		writeErr(w, 409, "工具运行时未启用")
		return
	}
	cfg, err := s.m.RuntimeToolConfig()
	if err != nil {
		writeErr(w, 500, "无法读取工具配置")
		return
	}
	if _, err := tm.Check(r.Context(), cfg); err != nil {
		writeErr(w, 500, config.Redact(err.Error()))
		return
	}
	writeJSON(w, 200, s.runtimeInfo())
}

// RuntimeToolConfig selects paths only. Business args/env and provider keys are
// never passed to probes. Read this after storage open and before Server startup.
func (m *Manager) RuntimeToolConfig() (runtimeenv.Config, error) {
	paths := config.Load().Tools
	if paths == nil {
		paths = map[string]string{}
	}
	if paths["python"] == "" {
		value, _, err := m.pg.GetSetting(settingPythonInterp)
		if err != nil {
			return runtimeenv.Config{}, err
		}
		paths["python"] = value
	}
	cfg := runtimeenv.Config{Paths: paths}
	rows, err := m.pg.ListMCP()
	if err != nil {
		return cfg, err
	}
	for _, row := range rows {
		cfg.MCPCommands = append(cfg.MCPCommands, row.Command)
	}
	return cfg, nil
}
func (s *Server) resumeRuntime(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Features []string `json:"features"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	for _, f := range req.Features {
		if f != "scheduler" && f != "notifier" && f != "mcp" && f != "archives" {
			writeErr(w, 400, "invalid runtime feature")
			return
		}
	}
	for _, f := range req.Features {
		s.startBackground(f)
	}
	writeJSON(w, 200, s.runtimeInfo())
}
func (s *Server) startBackground(feature string) {
	s.runtimeMu.Lock()
	defer s.runtimeMu.Unlock()
	if s.background == nil {
		s.background = map[string]bool{}
	}
	if s.background[feature] || s.ctx.Err() != nil {
		return
	}
	s.background[feature] = true
	switch feature {
	case "scheduler":
		s.goOwned(func() { newScheduler(s).Run(s.ctx) })
	case "notifier":
		s.goOwned(func() { newNotifier(s).Run(s.ctx) })
	case "mcp":
		s.goOwned(s.discoverEmptyMCPsOnStartup)
	case "archives":
		s.startTaskArchiveWorker()
	}
}

// All application publishers join here. Add and closing share one admission gate.
func (s *Server) goOwned(fn func()) bool {
	s.ownerMu.Lock()
	defer s.ownerMu.Unlock()
	if s.closing {
		return false
	}
	s.ownerWG.Add(1)
	go func() { defer s.ownerWG.Done(); fn() }()
	return true
}

func (s *Server) Shutdown(ctx context.Context) error {
	s.shutdownOnce.Do(func() {
		s.ownerMu.Lock()
		s.closing = true
		bodies := make([]io.ReadCloser, 0, len(s.activeBodies))
		for body := range s.activeBodies {
			bodies = append(bodies, body)
		}
		s.ownerMu.Unlock()
		for _, body := range bodies {
			_ = body.Close()
		}
		if s.cancel != nil {
			s.cancel(agent.AbortShutdown)
		}
		s.chatMu.Lock()
		for _, cancel := range s.chatCancel {
			cancel(agent.AbortShutdown)
		}
		s.chatMu.Unlock()
		if s.side != nil {
			s.side.mu.Lock()
			for _, run := range s.side.runs {
				run.cancel()
			}
			s.side.mu.Unlock()
		}
		s.ownerWG.Wait()
		if s.engine != nil {
			s.engine.Shutdown()
		}
		if s.side != nil {
			s.side.mu.Lock()
			runs := make([]<-chan struct{}, 0, len(s.side.runs))
			for _, run := range s.side.runs {
				runs = append(runs, run.done)
			}
			s.side.mu.Unlock()
			for _, done := range runs {
				<-done
			}
			<-s.side.done
			s.flushSideSnapshots()
		}
		s.archiveWG.Wait()
		if s.restoreHooks != nil {
			s.restoreHooks()
		}
		if s.m != nil {
			logSink.CloseDB(s.m.pg)
		}
	})
	return ctx.Err()
}

func (e *Engine) Shutdown() {
	e.deleteMu.Lock()
	e.closing.Store(true)
	e.deleteMu.Unlock()
	e.execMu.Lock()
	for _, cancel := range e.execCancel {
		cancel(agent.AbortShutdown)
	}
	e.execMu.Unlock()
	e.runtimeMu.Lock()
	runtimes := make([]*taskRuntime, 0, len(e.runtimes))
	for _, rt := range e.runtimes {
		rt.cancel()
		runtimes = append(runtimes, rt)
	}
	e.runtimeMu.Unlock()
	for _, rt := range runtimes {
		rt.wg.Wait()
	}
	// Interactive intent/bootstrap admissions are accounted independently of loops.
	for {
		busy := false
		e.inflight.Range(func(_, value any) bool {
			if atomic.LoadInt64(value.(*int64)) > 0 {
				busy = true
			}
			return true
		})
		if !busy {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (s *Server) pausePortableHistory() error {
	if _, err := s.m.pg.Exec(`UPDATE tasks SET paused=true,queued=false,queued_at=NULL WHERE deleted_at IS NULL AND status NOT IN ('done','failed','timeout')`); err != nil {
		return fmt.Errorf("pause portable tasks: %w", err)
	}
	for _, t := range s.m.LoadExisting() {
		if !isTerminalStatus(t.lifecycleSnapshot().Status) {
			s.engine.Pause(t.ID, agent.AbortPausedOnReload)
		}
		_, _ = t.Store.ResetRunningIntents()
	}
	return nil
}

// Saved global hooks are process-scoped SDK integration. Detach only the owner
// that is currently published; closing an older fixture cannot clear a new one.
var hooksOwner *Server

func (s *Server) captureHooks() {
	priorOwner := hooksOwner
	prompt, wrap, turns, timeout, timeoutTurns := agent.PromptOverride, agent.WrapupOverride, agent.WrapupMaxTurnsOverride, agent.WrapupTaskTimeoutOverride, agent.WrapupTaskTimeoutTurnsOverride
	augment, resolve, binding := agent.ToolAugment, agent.ToolResolve, agent.FindingTrafficBindingEnabled
	hooksOwner = s
	s.restoreHooks = func() {
		if hooksOwner != s {
			return
		}
		agent.PromptOverride = prompt
		agent.WrapupOverride = wrap
		agent.WrapupMaxTurnsOverride = turns
		agent.WrapupTaskTimeoutOverride = timeout
		agent.WrapupTaskTimeoutTurnsOverride = timeoutTurns
		agent.ToolAugment = augment
		agent.ToolResolve = resolve
		agent.FindingTrafficBindingEnabled = binding
		hooksOwner = priorOwner
		if priorOwner != nil {
			priorOwner.ownerMu.Lock()
			closed := priorOwner.closing
			priorOwner.ownerMu.Unlock()
			if closed && priorOwner.restoreHooks != nil {
				priorOwner.restoreHooks()
			}
		}
	}
}

func (s *Server) recoverArchiveFiles() error {
	if err := s.m.pg.RecoverTaskArchiveJobs(); err != nil {
		return err
	}
	if err := recoverTaskArchiveStages(s.m.dir, s.m.pg); err != nil {
		return err
	}
	if err := recoverTaskArchiveRestoreStages(s.m.dir, s.m.pg); err != nil {
		return err
	}
	return recoverTaskArchiveDeletePackages(s.m.dir, s.m.pg)
}
