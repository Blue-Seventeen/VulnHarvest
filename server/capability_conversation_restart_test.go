package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
)

// Real API-created chat and retest turns reach the production provider. Shutdown
// cancels both HTTP calls; reopening constructors retain history but do not replay
// either input. Retests are stopped/sealed, standalone conversations become idle.
func TestPortableRunningConversationAndRetestDoNotReplayOnReopen(t *testing.T) {
	entered, cancelled := make(chan string, 4), make(chan string, 4)
	var calls atomic.Int32
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		req, err := capabilityReadModel(w, r)
		if err != nil || req.Model != "conversation-restart" || req.Auth != "Bearer fake-acceptance-conversation-restart" || !strings.HasPrefix(req.Session, "conv-") || req.Stream {
			t.Errorf("invalid local conversation provider request: %+v err=%v", req, err)
			http.Error(w, "invalid local request", 400)
			return
		}
		_, _ = io.Copy(io.Discard, r.Body)
		entered <- req.Session
		<-r.Context().Done()
		cancelled <- req.Session
	}))
	t.Cleanup(model.Close)
	h := newCapabilityHarness(t)
	api := controlledAPI(t, h.s)
	capabilityDisableRetries(api)
	profile := capabilityProfile(api, model.URL, "conversation-restart", nil)
	api("POST", "/api/llm/profiles/active", map[string]any{"id": profile}, 200)
	api("POST", "/api/agents", map[string]any{"key": "accept_restart_chat", "name": "Restart chat"}, 200)
	convID := int64(api("POST", "/api/conversations", map[string]any{"agent_key": "accept_restart_chat", "title": "running history"}, 200)["id"].(float64))
	api("POST", fmt.Sprintf("/api/conversations/%d/messages", convID), map[string]any{"message": "CHAT_RESTART_INPUT_RECEIPT"}, 202)
	// The source finding is created by the production evidence service, with a
	// real Manager task/exploration. No retest state or result is injected in SQL.
	task, err := h.m.CreateTask("Retest restart source", "Preserve original evidence", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.m.SetTaskPaused(task.ID, true); err != nil {
		t.Fatal(err)
	}
	finding, err := h.s.evidenceStore().Record(t.Context(), db.RecordFindingInput{TaskID: numericTaskID(task.ID), ExplorationID: task.ExpID, Name: "Restart fixture", VulnClass: "TEST", Severity: "low", Summary: "Restart source", Evidence: "RETEST_SOURCE_EVIDENCE_RECEIPT", Worker: "test"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	retestRoute := fmt.Sprintf("/api/exploration/findings/%d/retests", finding.FindingID)
	started := api("POST", retestRoute, map[string]any{"notes": "RETEST_RESTART_INPUT_RECEIPT"}, 202)["retest"].(map[string]any)
	retestConvID := int64(started["conversation_id"].(float64))
	wantSessions := map[string]bool{fmt.Sprintf("conv-%d", convID): false, fmt.Sprintf("conv-%d", retestConvID): false}
	for range 2 {
		select {
		case session := <-entered:
			if seen, ok := wantSessions[session]; !ok || seen {
				t.Fatalf("unexpected/duplicate real provider session %s", session)
			}
			wantSessions[session] = true
		case <-time.After(8 * time.Second):
			t.Fatal("real chat/retest provider calls did not both start")
		}
	}
	for _, id := range []int64{convID, retestConvID} {
		if row := api("GET", fmt.Sprintf("/api/conversations/%d/messages", id), nil, 200); row["running"] != true {
			t.Fatalf("API-created turn was not actually running: %v", row)
		}
	}
	live := api("GET", retestRoute, nil, 200)["retests"].([]any)
	if len(live) != 1 || live[0].(map[string]any)["status"] != "running" {
		t.Fatalf("retest had not entered production running state: %v", live)
	}
	h.close()
	for range 2 {
		select {
		case session := <-cancelled:
			if !wantSessions[session] {
				t.Fatalf("unexpected cancellation %s", session)
			}
			wantSessions[session] = false
		case <-time.After(3 * time.Second):
			t.Fatal("real shutdown failed to cancel/join chat/retest HTTP calls")
		}
	}
	h.open()
	api = controlledAPI(t, h.s)
	for _, id := range []int64{convID, retestConvID} {
		row := api("GET", fmt.Sprintf("/api/conversations/%d/messages", id), nil, 200)
		if row["running"] != false || len(row["items"].([]any)) == 0 {
			t.Fatalf("cold reopen lost history or restarted a conversation: %v", row)
		}
	}
	chat, _ := json.Marshal(api("GET", fmt.Sprintf("/api/conversations/%d/messages", convID), nil, 200))
	if !strings.Contains(string(chat), "CHAT_RESTART_INPUT_RECEIPT") {
		t.Fatalf("interrupted user input not preserved: %s", chat)
	}
	history := api("GET", retestRoute, nil, 200)["retests"].([]any)
	if len(history) != 1 {
		t.Fatalf("retest history lost: %v", history)
	}
	row := history[0].(map[string]any)
	if row["status"] != "stopped" || row["finished_at"] == nil || row["verdict"] != "" || row["notes"] != "RETEST_RESTART_INPUT_RECEIPT" {
		t.Fatalf("interrupted retest was not stopped/sealed: %v", row)
	}
	active := api("GET", "/api/exploration/findings/retests/active", nil, 200)["retests"].([]any)
	if len(active) != 0 || len(h.s.engine.runtimes) != 0 {
		t.Fatal("startup retained a phantom running retest/task")
	}
	original, err := h.m.pg.GetFinding(finding.FindingID)
	if err != nil || original.Evidence != "RETEST_SOURCE_EVIDENCE_RECEIPT" || original.Status != db.FindingPending {
		t.Fatalf("interrupted retest changed original finding: %+v %v", original, err)
	}
	time.Sleep(1100 * time.Millisecond)
	if calls.Load() != 2 {
		t.Fatalf("cold constructors replayed conversation/retest provider calls: %d", calls.Load())
	}
}
