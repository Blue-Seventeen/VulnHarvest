package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
)

type capabilityModelRequest struct {
	Model               string `json:"model"`
	Stream              bool   `json:"stream"`
	MaxTokens           int    `json:"max_tokens"`
	MaxCompletionTokens int    `json:"max_completion_tokens"`
	ReasoningEffort     string `json:"reasoning_effort"`
	Thinking            struct {
		Type string `json:"type"`
	} `json:"thinking"`
	Messages []struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"messages"`
	Auth, Session string
}

func (r capabilityModelRequest) text() string {
	var s strings.Builder
	for _, m := range r.Messages {
		s.WriteString(m.Content)
		s.WriteByte('\n')
	}
	return s.String()
}

func capabilityReadModel(w http.ResponseWriter, r *http.Request) (capabilityModelRequest, error) {
	var req capabilityModelRequest
	if r.Method != "POST" || r.URL.Path != "/v1/chat/completions" {
		return req, fmt.Errorf("unexpected model route %s %s", r.Method, r.URL.Path)
	}
	err := json.NewDecoder(r.Body).Decode(&req)
	req.Auth, req.Session = r.Header.Get("Authorization"), r.Header.Get("X-Acceptance-Session")
	return req, err
}

func capabilityModelReply(w http.ResponseWriter, content string, tool bool, call int, inputTokens int) {
	message := map[string]any{"role": "assistant", "content": content}
	finish := "stop"
	if tool {
		message["tool_calls"] = []any{map[string]any{"id": fmt.Sprintf("accept-call-%d", call), "type": "function", "function": map[string]any{"name": "LS", "arguments": `{"path":"."}`}}}
		finish = "tool_calls"
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": message, "finish_reason": finish}}, "usage": map[string]any{"prompt_tokens": inputTokens, "completion_tokens": 3, "total_tokens": inputTokens + 3}})
}

func capabilityProfile(api func(string, string, any, int) map[string]any, url, name string, extra map[string]any) int64 {
	profile := map[string]any{"name": name, "format": "openai", "base_url": url + "/v1", "model": name, "api_key": "fake-acceptance-" + name, "streaming": false, "max_tokens": 417, "max_tokens_field": "max_completion_tokens", "context_window_k": 32, "reasoning_effort": "low", "thinking_type": "disabled", "session_header_key": "X-Acceptance-Session"}
	for k, v := range extra {
		profile[k] = v
	}
	return int64(api("POST", "/api/llm/profiles", profile, 200)["id"].(float64))
}

func capabilityRunConversation(t *testing.T, h *capabilityHarness, key string, pin *int64, message string) int64 {
	t.Helper()
	api := controlledAPI(t, h.s)
	created := api("POST", "/api/conversations", map[string]any{"agent_key": key, "title": "local configuration acceptance", "llm_profile_id": pin}, 200)
	id := int64(created["id"].(float64))
	conversation, err := h.m.pg.GetConversation(id)
	if err != nil || conversation == nil {
		t.Fatalf("API-created conversation unreadable: %v", err)
	}
	h.s.runConversationSync(conversation, message, h.s.convBusyKey(id))
	return id
}

func capabilityDisableRetries(api func(string, string, any, int) map[string]any) {
	off := db.RetryRule{Attempts: -1}
	api("POST", "/api/llm/retry-policy", db.LLMRetryPolicy{Connect: off, Empty: off, Stream: off, Breaker: off, Intent: off}, 200)
}

// Catches lost prompt versions, ignored Agent budgets/wrap-up overrides, and
// profile settings that survive SQLite but never reach a real ChatAgent request.
func TestPortableReopenedAgentPromptBudgetsAndContext(t *testing.T) {
	var mu sync.Mutex
	var calls []capabilityModelRequest
	normal, summaries := 0, 0
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req, err := capabilityReadModel(w, r)
		if err != nil {
			t.Error(err)
			http.Error(w, err.Error(), 400)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, req)
		if req.Model == "active-profile" {
			capabilityModelReply(w, "ACTIVE_PROFILE_RECEIPT", false, 0, 10)
			return
		}
		if len(req.Messages) > 0 && req.Messages[0].Content == "You are a helpful AI assistant tasked with summarizing conversations." {
			summaries++
			capabilityModelReply(w, "CONTEXT_32K_SUMMARY_RECEIPT", false, 0, 10)
			return
		}
		normal++
		if normal > 7 {
			http.Error(w, "Agent budgets failed to bound tool turns", 400)
			return
		}
		inputTokens := 10
		if normal == 5 {
			// Above the 32K compactor threshold, below the default 200K threshold.
			// LS is not micro-compacted, so the accurate provider usage drives Pre.
			inputTokens = 20000
		}
		capabilityModelReply(w, "", true, normal, inputTokens)
	}))
	defer model.Close()
	h := newCapabilityHarness(t)
	api := controlledAPI(t, h.s)
	api("PUT", "/api/settings", map[string]any{"traffic_enabled": false, "noa_compaction": false, "llm_pool_enabled": false}, 200)
	capabilityDisableRetries(api)
	active := capabilityProfile(api, model.URL, "active-profile", map[string]any{"context_window_k": 200})
	bound := capabilityProfile(api, model.URL, "bound-profile", nil)
	api("POST", "/api/llm/profiles/active", map[string]any{"id": active}, 200)
	for _, key := range []string{"accept_budget", "accept_global"} {
		api("POST", "/api/agents", map[string]any{"key": key, "name": key}, 200)
	}
	api("PUT", "/api/agents/accept_budget/config", map[string]any{"llm_profile_id": bound, "max_turns": 5, "run_seconds": 30, "interactive_shell": false}, 200)
	api("PUT", "/api/agents/accept_budget/prompt", map[string]any{"template": "OLD_PROMPT_RECEIPT", "note": "older version"}, 200)
	current := api("PUT", "/api/agents/accept_budget/prompt", map[string]any{"template": "CURRENT_PROMPT_RECEIPT data={{.DataDir}}", "note": "active version"}, 200)
	api("PUT", "/api/agents/accept_budget/wrapup", map[string]any{"prompt": "CUSTOM_WRAPUP_RECEIPT", "max_turns": 2}, 200)
	h.reopen()
	api = controlledAPI(t, h.s)
	versions := api("GET", "/api/agents/accept_budget/prompts", nil, 200)["versions"].([]any)
	if len(versions) != 3 || versions[0].(map[string]any)["version"] != current["version"] {
		t.Fatalf("prompt version history/current changed after constructor reopen: %v", versions)
	}
	profileRead, _ := json.Marshal(api("GET", "/api/llm/profiles", nil, 200))
	if strings.Contains(string(profileRead), "fake-acceptance-") {
		t.Fatal("profile API exposed the complete fake key")
	}
	capabilityRunConversation(t, h, "accept_global", nil, "active profile input")
	convID := capabilityRunConversation(t, h, "accept_budget", &active, "budget and context input")
	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 9 || normal != 7 || summaries != 1 {
		t.Fatalf("calls=%d normal=%d summaries=%d; want active + 5 main + 2 settlement + 1 compaction", len(calls), normal, summaries)
	}
	if calls[0].Model != "active-profile" || calls[0].Auth != "Bearer fake-acceptance-active-profile" {
		t.Fatalf("persisted global activation did not drive the real request: %+v", calls[0])
	}
	mainCall := 0
	for _, req := range calls[1:] {
		if req.Model != "bound-profile" || req.Auth != "Bearer fake-acceptance-bound-profile" || req.Stream || req.MaxCompletionTokens != 417 || req.MaxTokens != 0 || req.Session != fmt.Sprintf("conv-%d", convID) || req.ReasoningEffort != "low" || req.Thinking.Type != "disabled" {
			t.Fatalf("bound-profile request settings missing: %+v", req)
		}
		if req.Messages[0].Content == "You are a helpful AI assistant tasked with summarizing conversations." {
			continue
		}
		mainCall++
		text := req.text()
		if !strings.Contains(req.Messages[0].Content, "CURRENT_PROMPT_RECEIPT data="+h.data) || strings.Contains(text, "OLD_PROMPT_RECEIPT") {
			t.Fatalf("actual Agent request uses stale/unrendered prompt: %s", text)
		}
		if strings.Contains(text, "CUSTOM_WRAPUP_RECEIPT") != (mainCall > 5) {
			t.Fatalf("settlement began at the wrong configured turn: %d", mainCall)
		}
	}
	if !strings.Contains(calls[len(calls)-1].text(), "CONTEXT_32K_SUMMARY_RECEIPT") {
		t.Fatal("configured context window did not produce a consumed summary in the actual Agent request")
	}
	transcript, err := os.ReadFile(filepath.Join(h.data, "transcripts", fmt.Sprintf("conv-%d.jsonl", convID)))
	if err != nil || !strings.Contains(string(transcript), "CUSTOM_WRAPUP_RECEIPT") {
		t.Fatalf("actual settlement conversation not persisted: %v", err)
	}
}

func TestPortableReopenedAgentWallClockBudget(t *testing.T) {
	var mu sync.Mutex
	var calls []capabilityModelRequest
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req, err := capabilityReadModel(w, r)
		if err != nil {
			t.Error(err)
			return
		}
		mu.Lock()
		calls = append(calls, req)
		mu.Unlock()
		if strings.Contains(req.text(), "WALL_CLOCK_WRAPUP_RECEIPT") {
			capabilityModelReply(w, "WALL_CLOCK_SETTLED", false, 0, 10)
			return
		}
		// Model calls stay live until the turn boundary. The wall-clock budget
		// interrupts actual tool execution, so use the production sleep tool.
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"id": "clock-sleep", "type": "function", "function": map[string]any{"name": "sleep", "arguments": `{"seconds":4}`}}}}, "finish_reason": "tool_calls"}}, "usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 3}})
	}))
	defer model.Close()
	h := newCapabilityHarness(t)
	api := controlledAPI(t, h.s)
	capabilityDisableRetries(api)
	profile := capabilityProfile(api, model.URL, "clock-profile", nil)
	api("POST", "/api/llm/profiles/active", map[string]any{"id": profile}, 200)
	api("POST", "/api/agents", map[string]any{"key": "accept_clock", "name": "clock budget"}, 200)
	api("PUT", "/api/agents/accept_clock/config", map[string]any{"run_seconds": 1, "max_turns": 0}, 200)
	api("PUT", "/api/agents/accept_clock/wrapup", map[string]any{"prompt": "WALL_CLOCK_WRAPUP_RECEIPT", "max_turns": 1}, 200)
	h.reopen()
	start := time.Now()
	id := capabilityRunConversation(t, h, "accept_clock", nil, "wait for local deadline")
	elapsed := time.Since(start)
	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 2 || strings.Contains(calls[0].text(), "WALL_CLOCK_WRAPUP_RECEIPT") || !strings.Contains(calls[1].text(), "WALL_CLOCK_WRAPUP_RECEIPT") || elapsed < 900*time.Millisecond || elapsed > 3*time.Second {
		t.Fatalf("wall-clock budget/settlement not enforced: calls=%d elapsed=%v", len(calls), elapsed)
	}
	out, _ := json.Marshal(controlledAPI(t, h.s)("GET", fmt.Sprintf("/api/conversations/%d/messages", id), nil, 200))
	if !strings.Contains(string(out), "WALL_CLOCK_SETTLED") {
		t.Fatalf("timeout wrap-up output absent: %s", out)
	}
}

func TestPortableReopenedProfileRetryAndBoundFallback(t *testing.T) {
	var mu sync.Mutex
	var calls []capabilityModelRequest
	failingHead := false
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req, err := capabilityReadModel(w, r)
		if err != nil {
			t.Error(err)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, req)
		if req.Model == "retry-head" {
			if failingHead {
				http.Error(w, "controlled invalid key", 401)
				return
			}
			if len(calls) == 1 {
				http.Error(w, "controlled establishment failure", 503)
				return
			}
			capabilityModelReply(w, "RETRIED_HEAD_RECEIPT", false, 0, 10)
			return
		}
		capabilityModelReply(w, "BOUND_FALLBACK_RECEIPT", false, 0, 10)
	}))
	defer model.Close()
	h := newCapabilityHarness(t)
	api := controlledAPI(t, h.s)
	capabilityDisableRetries(api)
	off := db.RetryRule{Attempts: -1}
	head := capabilityProfile(api, model.URL, "retry-head", map[string]any{"context_window_k": 64, "retry": db.RetryOverride{Connect: db.RetryRule{Attempts: 1, IntervalMS: 1}, Empty: off, Stream: off}})
	backup := capabilityProfile(api, model.URL, "retry-backup", map[string]any{"context_window_k": 64})
	api("POST", "/api/llm/profiles/active", map[string]any{"id": head}, 200)
	api("POST", "/api/agents", map[string]any{"key": "accept_retry", "name": "retry acceptance"}, 200)
	api("PUT", "/api/settings", map[string]any{"llm_pool_enabled": false, "llm_pool_bind_fallback": false, "noa_compaction": false}, 200)
	h.reopen()
	id := capabilityRunConversation(t, h, "accept_retry", nil, "retry one establishment failure")
	mu.Lock()
	if len(calls) != 2 || calls[0].Model != "retry-head" || calls[1].Model != "retry-head" {
		mu.Unlock()
		t.Fatalf("persisted profile override did not retry exactly once: %v", calls)
	}
	failingHead = true
	mu.Unlock()
	api = controlledAPI(t, h.s)
	out, _ := json.Marshal(api("GET", fmt.Sprintf("/api/conversations/%d/messages", id), nil, 200))
	if !strings.Contains(string(out), "RETRIED_HEAD_RECEIPT") {
		t.Fatalf("successful retry output missing: %s", out)
	}
	api("PUT", "/api/settings", map[string]any{"llm_pool_enabled": true, "llm_pool_bind_fallback": true}, 200)
	api("PUT", "/api/agents/accept_retry/config", map[string]any{"llm_profile_id": head}, 200)
	h.reopen()
	id = capabilityRunConversation(t, h, "accept_retry", &backup, "bound provider fails before output")
	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 4 || calls[2].Model != "retry-head" || calls[3].Model != "retry-backup" || calls[2].Auth != "Bearer fake-acceptance-retry-head" || calls[3].Auth != "Bearer fake-acceptance-retry-backup" {
		t.Fatalf("reopened binding/fallback ignored or replayed: %+v", calls)
	}
	out, _ = json.Marshal(controlledAPI(t, h.s)("GET", fmt.Sprintf("/api/conversations/%d/messages", id), nil, 200))
	if !strings.Contains(string(out), "BOUND_FALLBACK_RECEIPT") {
		t.Fatalf("actual fallback output missing: %s", out)
	}
	// Only the SDK connection retry and pre-output pool fallback are exercised;
	// the existing task_llm tests own stream replay and task-chain retry semantics.
}
