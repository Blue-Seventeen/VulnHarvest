package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
)

type controlledAgentModel struct {
	mu                 sync.Mutex
	steps              map[string]int
	goal, intent, fact int64
	nonce              string
	errors             []string
	stream             bool
}

func (m *controlledAgentModel) snapshot() (int, []string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, count := range m.steps {
		n += count
	}
	return n, append([]string(nil), m.errors...)
}

// The model learns all graph IDs and the receipt exclusively from real tool results.
func (m *controlledAgentModel) respond(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()
	fail := func(err error) { m.errors = append(m.errors, err.Error()); http.Error(w, err.Error(), 400) }
	var req struct {
		Model               string `json:"model"`
		Stream              bool   `json:"stream"`
		MaxCompletionTokens int    `json:"max_completion_tokens"`
		MaxTokens           *int   `json:"max_tokens"`
		Messages            []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
		Tools []struct {
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		} `json:"tools"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(err)
		return
	}
	if r.Method != "POST" || r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer controlled-local-only" || req.Model != "controlled-agent" || req.Stream != m.stream || req.MaxCompletionTokens != 512 || req.MaxTokens != nil {
		fail(fmt.Errorf("unexpected provider wire configuration"))
		return
	}
	session := r.Header.Get("X-Acceptance-Session")
	role := ""
	switch {
	case strings.HasSuffix(session, "-goals"):
		role = "goals"
	case strings.HasSuffix(session, "-planner"):
		role = "planner"
	case strings.Contains(session, "-worker-i"):
		role = "worker"
	}
	if role == "" {
		fail(fmt.Errorf("unexpected session %q", session))
		return
	}
	m.steps[role]++
	step := m.steps[role]
	last := ""
	for _, msg := range req.Messages {
		if msg.Role == "tool" {
			if err := json.Unmarshal(msg.Content, &last); err != nil {
				fail(err)
				return
			}
		}
	}
	readID := func(pattern string, dst *int64) error {
		match := regexp.MustCompile(pattern).FindStringSubmatch(last)
		if len(match) != 2 {
			return fmt.Errorf("%s stage %d missing successful tool result: %s", role, step, last)
		}
		id, err := strconv.ParseInt(match[1], 10, 64)
		if err != nil || id <= 0 {
			return fmt.Errorf("invalid actual ID")
		}
		*dst = id
		return nil
	}
	name := ""
	args := map[string]any{}
	var err error
	switch role {
	case "goals":
		switch step {
		case 1:
			name = "set_goals"
			args["text"] = "Read the controlled receipt, record an observed fact and prove the goal."
		case 2:
			err = readID(`goal added: (\d+)`, &m.goal)
		default:
			err = fmt.Errorf("unexpected goals step %d", step)
		}
	case "planner":
		switch step {
		case 1:
			if m.goal == 0 {
				err = fmt.Errorf("planner preceded goals result")
			}
			name = "add_intent"
			args = map[string]any{"summary": "Use portable_receipt path=/receipt and record the actual body as an observed fact.", "priority": 5}
		case 2:
			err = readID(`intent created: (\d+)`, &m.intent)
		case 3:
			if m.steps["worker"] != 3 || m.fact == 0 {
				err = fmt.Errorf("second planner preceded worker completion")
			}
			name = "graph_overview"
		case 4:
			var graph map[string]any
			err = json.Unmarshal([]byte(last), &graph)
			if err == nil && (!strings.Contains(last, "Observed controlled receipt") || !strings.Contains(last, "done")) {
				err = fmt.Errorf("second planner graph lacks completed worker fact/intent: %s", last)
			}
			name = "node_detail"
			args["id"] = m.fact
		case 5:
			if m.nonce == "" || !strings.Contains(last, m.nonce) {
				err = fmt.Errorf("second planner detail lacks observed nonce")
			}
			name = "prove_goal"
			args = map[string]any{"goal_id": m.goal, "evidence_id": m.fact, "reason": "The actual controlled HTTP receipt was persisted."}
		case 6:
			if !strings.Contains(last, fmt.Sprintf("goal %d marked met", m.goal)) {
				err = fmt.Errorf("proof failed: %s", last)
			}
		default:
			err = fmt.Errorf("unexpected planner step %d", step)
		}
	case "worker":
		switch step {
		case 1:
			name = "portable_receipt"
			args["path"] = "/receipt"
		case 2:
			var output struct {
				Status int    `json:"status"`
				Body   string `json:"body"`
			}
			err = json.Unmarshal([]byte(last), &output)
			var receipt struct {
				Receipt string `json:"receipt"`
			}
			if err == nil {
				err = json.Unmarshal([]byte(output.Body), &receipt)
			}
			if err == nil && (output.Status != 200 || receipt.Receipt == "" || !strings.HasSuffix(session, fmt.Sprintf("-worker-i%d", m.intent))) {
				err = fmt.Errorf("invalid actual receipt/intent")
			}
			m.nonce = receipt.Receipt
			name = "record_fact"
			args = map[string]any{"summary": "Observed controlled receipt", "detail": output.Body, "evidence": "GET /receipt -> HTTP 200: " + output.Body, "confidence": "observed", "intent_id": m.intent}
		case 3:
			err = readID(`fact recorded: (\d+)`, &m.fact)
		default:
			err = fmt.Errorf("unexpected worker step %d", step)
		}
	}
	if err != nil {
		fail(err)
		return
	}
	if name != "" {
		found := false
		for _, tool := range req.Tools {
			if tool.Function.Name == name {
				found = true
			}
		}
		if !found {
			fail(fmt.Errorf("%s absent from actual tool schema", name))
			return
		}
	}
	reason := "stop"
	message := map[string]any{"role": "assistant", "content": "Controlled stage complete."}
	var call map[string]any
	if name != "" {
		b, _ := json.Marshal(args)
		call = map[string]any{"id": fmt.Sprintf("call_%s_%d", role, step), "type": "function", "function": map[string]any{"name": name, "arguments": string(b)}}
		message["content"] = nil
		message["tool_calls"] = []any{call}
		reason = "tool_calls"
	}
	usage := map[string]any{"prompt_tokens": 20, "completion_tokens": 8, "total_tokens": 28}
	if !m.stream {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "cmpl-controlled", "object": "chat.completion", "choices": []any{map[string]any{"index": 0, "message": message, "finish_reason": reason}}, "usage": usage})
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	delta := map[string]any{"content": message["content"]}
	if call != nil {
		call["index"] = 0
		delta = map[string]any{"tool_calls": []any{call}}
	}
	for _, chunk := range []any{map[string]any{"choices": []any{map[string]any{"index": 0, "delta": delta}}}, map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": reason}}, "usage": usage}} {
		b, _ := json.Marshal(chunk)
		_, _ = fmt.Fprintf(w, "data: %s\n\n", b)
		w.(http.Flusher).Flush()
	}
	_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	w.(http.Flusher).Flush()
}

func controlledAPI(t *testing.T, s *Server) func(string, string, any, int) map[string]any {
	t.Helper()
	token, err := signJWT(s.jwtKey)
	if err != nil {
		t.Fatal(err)
	}
	h := s.Handler()
	return func(method, path string, body any, status int) map[string]any {
		t.Helper()
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest(method, path, bytes.NewReader(b))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != status {
			t.Fatalf("%s %s: %d want %d: %s", method, path, w.Code, status, w.Body.String())
		}
		var out map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
}

func TestControlledAgentCompleteAndStream(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%t", stream), func(t *testing.T) {
			fixture := &controlledAgentModel{steps: map[string]int{}, stream: stream}
			model := httptest.NewServer(http.HandlerFunc(fixture.respond))
			defer model.Close()
			var receiptCalls atomic.Int32
			var nonceBytes [24]byte
			if _, err := rand.Read(nonceBytes[:]); err != nil {
				t.Fatal(err)
			}
			nonce := hex.EncodeToString(nonceBytes[:])
			receipt := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" || r.URL.Path != "/receipt" {
					http.Error(w, "unexpected receipt request", 400)
					return
				}
				receiptCalls.Add(1)
				_ = json.NewEncoder(w).Encode(map[string]string{"receipt": nonce})
			}))
			defer receipt.Close()
			base := t.TempDir()
			data, state, skills := filepath.Join(base, "data"), filepath.Join(base, "state"), filepath.Join(base, "skills")
			m, err := NewManagerWithOptions(ManagerOptions{Mode: "portable", DataDir: data, StateDir: state, SkillDir: skills})
			if err != nil {
				t.Fatal(err)
			}
			s, err := NewWithOptions(context.Background(), m, skills, data, state)
			if err != nil {
				_ = m.Close()
				t.Fatal(err)
			}
			defer func() {
				if err := s.Shutdown(context.Background()); err != nil {
					t.Error(err)
				}
				if err := m.Close(); err != nil {
					t.Error(err)
				}
			}()
			for _, err := range []error{m.SetWorkers(1), m.SetTrafficEnabled(false), m.SetNoaCompaction(false), m.SetLLMPoolEnabled(false)} {
				if err != nil {
					t.Fatal(err)
				}
			}
			api := controlledAPI(t, s)
			off := db.RetryRule{Attempts: -1}
			api("POST", "/api/llm/retry-policy", db.LLMRetryPolicy{Connect: off, Empty: off, Stream: off, Breaker: off, Intent: off}, 200)
			profile := api("POST", "/api/llm/profiles", map[string]any{"name": "controlled-local", "format": "openai", "base_url": model.URL + "/v1", "model": "controlled-agent", "api_key": "controlled-local-only", "streaming": stream, "session_header_key": "X-Acceptance-Session", "max_tokens": 512, "max_tokens_field": "max_completion_tokens", "context_window_k": 64, "reasoning_effort": "low", "thinking_type": "disabled", "retry": db.RetryOverride{Connect: off, Empty: off, Stream: off}}, 200)
			profileID := profile["id"]
			api("POST", "/api/llm/profiles/active", map[string]any{"id": profileID}, 200)
			api("POST", "/api/tools/custom", map[string]any{"key": "portable_receipt", "description": "Retrieve the controlled local receipt.", "enabled": true, "kind": "http", "deferred": false, "agents": []string{"worker"}, "schema": map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string", "enum": []string{"/receipt"}}}, "required": []string{"path"}}, "exec": map[string]any{"url": receipt.URL + "{path}", "method": "GET", "timeout_ms": 2000}}, 200)
			task := api("POST", "/api/tasks", map[string]any{"description": "Controlled acceptance", "goal": "Read the local receipt and prove the goal using an observed fact.", "llm_profile_ids": []any{profileID}, "seed_first_intent": false, "coverage_enabled": false, "timeout_seconds": 45, "plan_heartbeat_seconds": 600}, 201)
			id := task["id"].(string)
			deadline := time.Now().Add(40 * time.Second)
			for {
				calls, errors := fixture.snapshot()
				if len(errors) > 0 {
					t.Fatalf("model errors after %d calls: %v", calls, errors)
				}
				row := api("GET", "/api/tasks/"+id, nil, 200)
				if row["status"] == "done" && s.engine.ActiveLLMCalls(id) == 0 {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("task failed to finish: %v; model calls=%d", row, calls)
				}
				time.Sleep(20 * time.Millisecond)
			}
			if calls, errors := fixture.snapshot(); calls != 11 || len(errors) != 0 {
				t.Fatalf("model calls=%d errors=%v", calls, errors)
			}
			if receiptCalls.Load() != 1 || fixture.nonce != nonce {
				t.Fatalf("receipt count=%d actual nonce mismatch=%t", receiptCalls.Load(), fixture.nonce != nonce)
			}
			verify := func(api func(string, string, any, int) map[string]any) {
				goals := api("GET", "/api/tasks/"+id+"/goals", nil, 200)
				encoded, _ := json.Marshal(goals)
				if !bytes.Contains(encoded, []byte(`"origin":"goals"`)) || !bytes.Contains(encoded, []byte(`"state":"met"`)) {
					t.Fatalf("real goals provenance/proof missing: %s", encoded)
				}
				graph := api("GET", "/api/exploration/graph?task="+id, nil, 200)
				encoded, _ = json.Marshal(graph)
				for _, want := range []string{nonce, `"proves"`, `"done"`} {
					if !bytes.Contains(encoded, []byte(want)) {
						t.Fatalf("graph missing %s: %s", want, encoded)
					}
				}
				activity := api("GET", "/api/exploration/activity?task="+id+"&limit=500", nil, 200)
				encoded, _ = json.Marshal(activity)
				for _, want := range []string{"set_goals", "portable_receipt", "record_fact", "graph_overview", "node_detail", "prove_goal"} {
					if !bytes.Contains(encoded, []byte(want)) {
						t.Fatalf("activity missing %s", want)
					}
				}
				usage := api("GET", "/api/tokens/usage", nil, 200)
				encoded, _ = json.Marshal(usage)
				if string(encoded) == `{"by_profile":[],"daily":[]}` || !bytes.Contains(encoded, []byte("controlled-local")) {
					t.Fatalf("usage not persisted: %s", encoded)
				}
			}
			verify(api)
			for _, session := range []string{fmt.Sprintf("exp%.0f-planner", task["exploration_id"]), fmt.Sprintf("exp%.0f-worker-i%d", task["exploration_id"], fixture.intent)} {
				content, err := os.ReadFile(filepath.Join(data, "transcripts", session+".jsonl"))
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Contains(content, []byte("tool")) || !bytes.Contains(content, []byte(nonce)) {
					t.Fatalf("transcript %s lacks real tools/nonce", session)
				}
			}
			if err := s.Shutdown(context.Background()); err != nil {
				t.Fatal(err)
			}
			if err := m.Close(); err != nil {
				t.Fatal(err)
			}
			m, err = NewManagerWithOptions(ManagerOptions{Mode: "portable", DataDir: data, StateDir: state, SkillDir: skills})
			if err != nil {
				t.Fatal(err)
			}
			s, err = NewWithOptions(context.Background(), m, skills, data, state)
			if err != nil {
				t.Fatal(err)
			}
			api = controlledAPI(t, s)
			verify(api)
			if row := api("GET", "/api/tasks/"+id, nil, 200); row["status"] != "done" {
				t.Fatalf("reopened task changed: %v", row)
			}
			if calls, errors := fixture.snapshot(); calls != 11 || len(errors) > 0 || receiptCalls.Load() != 1 || len(s.engine.runtimes) != 0 {
				t.Fatalf("reopen replay: model=%d errors=%v receipt=%d", calls, errors, receiptCalls.Load())
			}
		})
	}
}
