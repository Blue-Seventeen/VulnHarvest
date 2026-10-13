package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/artex/agent"
)

// A running worker is interrupted before its first model response. Goals and
// intent IDs have already come from the original production tools. The cold
// startup must park the task, then explicit task control resumes the same intent
// and completes the original controlled model's receipt/fact/proof chain.
func TestPortableRunningTaskColdReopenRequiresExplicitResume(t *testing.T) {
	fixture := &controlledAgentModel{steps: map[string]int{}}
	workerEntered, workerCancelled, workerSettled := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var firstWorker, recovering atomic.Bool
	var modelCalls, receiptCalls atomic.Int32
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		modelCalls.Add(1)
		session := r.Header.Get("X-Acceptance-Session")
		if strings.Contains(session, "-worker-i") && firstWorker.CompareAndSwap(false, true) {
			// Drain the uploaded body so net/http can observe the client closing
			// while this response is pending. Never feed this call to the model.
			if _, err := io.Copy(io.Discard, r.Body); err != nil {
				t.Error(err)
				return
			}
			close(workerEntered)
			<-r.Context().Done()
			close(workerCancelled)
			return
		}
		if recovering.Load() && strings.HasSuffix(session, "-planner") {
			// The main test releases this only after observing the real worker's
			// persisted done state, not merely after writing its final response.
			select {
			case <-workerSettled:
			case <-r.Context().Done():
				return
			}
		}
		fixture.respond(w, r)
	}))
	// Cleanup registration order matters: real Server shutdown must cancel the
	// pending HTTP request before httptest waits for its handler to return.
	t.Cleanup(model.Close)
	var nonceBytes [16]byte
	if _, err := rand.Read(nonceBytes[:]); err != nil {
		t.Fatal(err)
	}
	nonce := hex.EncodeToString(nonceBytes[:])
	receipt := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/receipt" {
			t.Errorf("unexpected receipt %s %s", r.Method, r.URL.Path)
			http.Error(w, "unexpected receipt request", 400)
			return
		}
		receiptCalls.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]string{"receipt": nonce})
	}))
	t.Cleanup(receipt.Close)
	h := newCapabilityHarness(t)
	api := controlledAPI(t, h.s)
	api("PUT", "/api/settings", map[string]any{"workers": 1, "noa_compaction": false, "llm_pool_enabled": false}, 200)
	if err := h.m.SetTrafficEnabled(false); err != nil {
		t.Fatal(err)
	}
	capabilityDisableRetries(api)
	profile := api("POST", "/api/llm/profiles", map[string]any{"name": "cold-resume-local", "format": "openai", "base_url": model.URL + "/v1", "model": "controlled-agent", "api_key": "controlled-local-only", "streaming": false, "session_header_key": "X-Acceptance-Session", "max_tokens": 512, "max_tokens_field": "max_completion_tokens", "context_window_k": 64}, 200)
	profileID := profile["id"]
	api("POST", "/api/llm/profiles/active", map[string]any{"id": profileID}, 200)
	api("POST", "/api/tools/custom", map[string]any{"key": "portable_receipt", "description": "Retrieve the controlled local receipt.", "enabled": true, "kind": "http", "deferred": false, "agents": []string{"worker"}, "schema": map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string", "enum": []string{"/receipt"}}}, "required": []string{"path"}}, "exec": map[string]any{"url": receipt.URL + "{path}", "method": "GET", "timeout_ms": 2000}}, 200)
	task := api("POST", "/api/tasks", map[string]any{"description": "Cold resume acceptance", "goal": "Read the local receipt and prove the goal using an observed fact.", "llm_profile_ids": []any{profileID}, "seed_first_intent": false, "coverage_enabled": false, "timeout_seconds": 60, "plan_heartbeat_seconds": 600}, 201)
	id := task["id"].(string)
	select {
	case <-workerEntered:
	case <-time.After(20 * time.Second):
		calls, errors := fixture.snapshot()
		t.Fatalf("first running worker request never entered: received=%d responded=%d errors=%v", modelCalls.Load(), calls, errors)
	}
	fixture.mu.Lock()
	goalID, intentID := fixture.goal, fixture.intent
	fixture.mu.Unlock()
	if goalID <= 0 || intentID <= 0 || modelCalls.Load() != 5 || receiptCalls.Load() != 0 {
		t.Fatalf("unexpected real pre-shutdown checkpoint: goal=%d intent=%d model=%d receipt=%d", goalID, intentID, modelCalls.Load(), receiptCalls.Load())
	}
	live := api("GET", "/api/tasks/"+id, nil, 200)
	if live["paused"] != false || live["status"] != "running" || h.s.engine.ActiveLLMCalls(id) != 1 {
		t.Fatalf("fixture was not an actual running task: %v active=%d", live, h.s.engine.ActiveLLMCalls(id))
	}
	h.close()
	select {
	case <-workerCancelled:
	case <-time.After(3 * time.Second):
		t.Fatal("real Shutdown did not cancel the pending worker HTTP request")
	}
	h.open()
	api = controlledAPI(t, h.s)
	parked := api("GET", "/api/tasks/"+id, nil, 200)
	if parked["status"] != "paused" || parked["paused"] != true || parked["queued"] != false || h.s.engine.Started(id) || h.s.engine.ActiveLLMCalls(id) != 0 || len(h.s.engine.runtimes) != 0 {
		t.Fatalf("cold constructor did not park running history: %v runtimes=%d", parked, len(h.s.engine.runtimes))
	}
	restored, ok := h.m.Task(id)
	if !ok {
		t.Fatal("cold constructor lost the running task")
	}
	intent, err := restored.Store.GetNode(intentID)
	if err != nil || intent == nil || intent.State != "open" {
		t.Fatalf("shutdown/reopen left the interrupted intent unclaimable: intent=%+v err=%v; explicit task resume must reclaim it", intent, err)
	}
	// Observe beyond a worker polling interval as well as checking runtime state.
	time.Sleep(1100 * time.Millisecond)
	if calls, errors := fixture.snapshot(); modelCalls.Load() != 5 || calls != 4 || len(errors) != 0 || receiptCalls.Load() != 0 {
		t.Fatalf("cold startup replayed model/tool work: received=%d responded=%d receipt=%d errors=%v", modelCalls.Load(), calls, receiptCalls.Load(), errors)
	}
	recovering.Store(true)
	resumed := api("POST", "/api/tasks/"+id+"/control", map[string]any{"action": "resume"}, 200)
	if resumed["status"] != "running" || resumed["paused"] != false || resumed["queued"] != false {
		t.Fatalf("explicit resume was not admitted: %v", resumed)
	}
	deadline := time.Now().Add(25 * time.Second)
	settled := false
	for {
		calls, errors := fixture.snapshot()
		if len(errors) > 0 {
			t.Fatalf("recovery model errors after %d responses: %v", calls, errors)
		}
		intent, err := restored.Store.GetNode(intentID)
		if err != nil {
			t.Fatal(err)
		}
		if !settled && intent != nil && intent.State == "done" {
			close(workerSettled)
			settled = true
		}
		row := api("GET", "/api/tasks/"+id, nil, 200)
		if row["status"] == "done" && h.s.engine.ActiveLLMCalls(id) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("explicit resume did not finish: task=%v intent=%+v responded=%d received=%d", row, intent, calls, modelCalls.Load())
		}
		time.Sleep(20 * time.Millisecond)
	}
	fixture.mu.Lock()
	finalGoal, finalIntent, factID, observedNonce := fixture.goal, fixture.intent, fixture.fact, fixture.nonce
	fixture.mu.Unlock()
	if calls, errors := fixture.snapshot(); calls != 11 || len(errors) != 0 || modelCalls.Load() != 12 || receiptCalls.Load() != 1 || finalGoal != goalID || finalIntent != intentID || factID <= 0 || observedNonce != nonce || !settled {
		t.Fatalf("recovery restarted IDs or duplicated real work: responded=%d received=%d receipt=%d errors=%v goal=%d intent=%d fact=%d", calls, modelCalls.Load(), receiptCalls.Load(), errors, finalGoal, finalIntent, factID)
	}
	goals, _ := json.Marshal(api("GET", "/api/tasks/"+id+"/goals", nil, 200))
	graph, _ := json.Marshal(api("GET", "/api/exploration/graph?task="+id, nil, 200))
	if !bytes.Contains(goals, []byte(`"origin":"goals"`)) || !bytes.Contains(goals, []byte(`"state":"met"`)) || !bytes.Contains(graph, []byte(nonce)) || !bytes.Contains(graph, []byte(`"proves"`)) {
		t.Fatalf("actual resumed fact/proof not persisted: goals=%s graph=%s", goals, graph)
	}
	transcript, err := os.ReadFile(filepath.Join(h.data, "transcripts", fmt.Sprintf("exp%.0f-worker-i%d.jsonl", task["exploration_id"], intentID)))
	if err != nil || !bytes.Contains(transcript, []byte(nonce)) {
		t.Fatalf("resumed worker transcript missing receipt: %v", err)
	}
	// Normal completion survives another constructor reopen without replay too.
	h.reopen()
	if row := controlledAPI(t, h.s)("GET", "/api/tasks/"+id, nil, 200); row["status"] != "done" || modelCalls.Load() != 12 || receiptCalls.Load() != 1 || len(h.s.engine.runtimes) != 0 {
		t.Fatalf("finished recovery replayed at cold reopen: %v model=%d receipt=%d", row, modelCalls.Load(), receiptCalls.Load())
	}
}

func TestShutdownCauseReturnsInterruptedWorkToFrontier(t *testing.T) {
	e := NewEngine(nil)
	interrupted := e.execContextFor(context.Background(), "shutdown-cause")
	e.Shutdown()
	if interrupted.Err() == nil || context.Cause(interrupted) != agent.AbortShutdown {
		t.Fatalf("actual Engine.Shutdown cause=%v err=%v", context.Cause(interrupted), interrupted.Err())
	}
	for _, tc := range []struct {
		name  string
		cause error
		want  bool
	}{
		{"shutdown", agent.AbortShutdown, true},
		{"pause", agent.AbortPausedByUser, true},
		{"delete rollback", agent.AbortTaskDeleted, true},
		{"goal met", agent.AbortGoalMet, false},
		{"deadline settlement drain", agent.AbortSettleDrainTimeout, false},
		{"planner kill", agent.AbortKilledByPlanner, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := taskExecutionPaused(tc.cause); got != tc.want {
				t.Fatalf("recoverable task cancellation=%v, want %v for %v", got, tc.want, tc.cause)
			}
		})
	}
}
