package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/Autumn-27/artex/sidequestion"
)

func TestPortableDAOContracts(t *testing.T) {
	task2Stores(t, func(t *testing.T, d *DB) {
		t.Run("profiles", func(t *testing.T) {
			p := &LLMProfile{Name: "task2_primary", Format: "openai", Model: "fixture", APIKey: "fixture-key-1234", Streaming: true, MaxTokens: 991, ContextWindowK: 256, ReasoningEffort: "high", Priority: 3}
			id, err := d.SaveProfile(p)
			if err != nil {
				t.Fatal(err)
			}
			p.ID = id
			if err = d.SetActiveProfile(id); err != nil {
				t.Fatal(err)
			}
			got, err := d.ActiveProfile()
			if err != nil || got == nil || got.APIKey != p.APIKey || got.MaxTokens != 991 {
				t.Fatalf("active: %+v %v", got, err)
			}
			if err = d.DeleteProfile(id); err != ErrActiveLLMProfileDelete {
				t.Fatalf("active delete: %v", err)
			}
			second := *p
			second.ID = 0
			second.Name = "task2_fallback"
			sid, err := d.SaveProfile(&second)
			if err != nil {
				t.Fatal(err)
			}
			chain, err := d.PoolProfiles()
			if err != nil || len(chain) < 2 || chain[0].ID != id {
				t.Fatalf("fallback: %v %v", chain, err)
			}
			if err = d.SetAgentLLMProfile("worker", &sid); err != nil {
				t.Fatal(err)
			}
			if err = d.DeleteProfile(sid); err != nil {
				t.Fatal(err)
			}
			agent, err := d.GetAgentByKey("worker")
			if err != nil || agent.LLMProfileID != nil {
				t.Fatalf("reference not cleared: %+v %v", agent, err)
			}
		})
		t.Run("tools", func(t *testing.T) {
			key := "task2tool"
			if err := d.SeedTool(key, "fixture", json.RawMessage(`{"type":"object"}`), json.RawMessage(`["worker","auto"]`)); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 2; i++ {
				if err := d.AddAgentToToolBinding("pentest", []string{key}); err != nil {
					t.Fatal(err)
				}
			}
			if err := d.RemoveAgentFromTool("auto", key); err != nil {
				t.Fatal(err)
			}
			if err := d.RemoveAgentFromToolBindings("pentest"); err != nil {
				t.Fatal(err)
			}
			tool, err := d.GetTool(key)
			if err != nil || !reflect.DeepEqual(tool.Agents, []string{"worker"}) {
				t.Fatalf("binding: %+v %v", tool, err)
			}
			_, _, counts, err := d.AgentBindingCounts()
			if err != nil || counts["worker"] < 1 {
				t.Fatalf("counts: %v %v", counts, err)
			}
		})
		t.Run("conversation", func(t *testing.T) {
			active, err := d.ActiveProfile()
			if err != nil {
				t.Fatal(err)
			}
			c, err := d.CreateConversation("worker", "task2 conversation", &active.ID)
			if err != nil {
				t.Fatal(err)
			}
			title := "edited"
			pin := true
			c, err = d.UpdateConversation(c.ID, ConversationPatch{Title: &title, Pinned: &pin})
			if err != nil || !c.Pinned || c.Title != title {
				t.Fatalf("patch: %+v %v", c, err)
			}
			if err = d.UpdateConversationProfile(c.ID, nil); err != nil {
				t.Fatal(err)
			}
			aid, err := d.AppendConvActivity(c.ID, Activity{Kind: "result", Summary: "done", Detail: "raw\nbody", InputTokens: intp(123), OutputTokens: intp(45), CacheReadTokens: intp(9), CacheWriteTokens: intp(8)})
			if err != nil {
				t.Fatal(err)
			}
			details, err := d.ConvActivityDetail(c.ID, aid)
			if err != nil || details != "raw\nbody" {
				t.Fatalf("detail: %q %v", details, err)
			}
			list, cursor, err := d.ConvActivityList(c.ID, 0, 10)
			if err != nil || len(list) != 1 || cursor != aid || (list[0].InputTokens == nil || *list[0].InputTokens != 123) {
				t.Fatalf("activities: %v %v", list, err)
			}
			sums, err := d.ConversationTokenSummaries()
			if err != nil || len(sums) < 1 {
				t.Fatalf("summaries: %v %v", sums, err)
			}
			ids, err := d.DeleteConversations([]int64{c.ID, 922337})
			if err != nil || len(ids) != 1 || ids[0] != c.ID {
				t.Fatalf("delete: %v %v", ids, err)
			}
		})
		t.Run("side", func(t *testing.T) {
			ctx := context.Background()
			c, err := d.CreateConversation("worker", "side", nil)
			if err != nil {
				t.Fatal(err)
			}
			s := sidequestion.Snapshot{Parent: sidequestion.Parent{ConversationID: c.ID}, RunID: 1, Version: 1, CapturedAt: time.Now().UTC()}
			if err = d.SaveSideSnapshot(ctx, s); err != nil {
				t.Fatal(err)
			}
			snap, err := d.SideSnapshot(ctx, s.Parent.Key())
			if err != nil || snap.Version != 1 {
				t.Fatalf("snapshot: %+v %v", snap, err)
			}
			e, created, err := d.StartSideRequest(ctx, s, "fixture-id", "question")
			if err != nil || !created || e.Ordinal <= 0 {
				t.Fatalf("start: %+v %v", e, err)
			}
			same, newly, err := d.StartSideRequest(ctx, s, "fixture-id", "question")
			if err != nil || newly || same.ID != e.ID {
				t.Fatalf("idempotency: %+v %v", same, err)
			}
			e.Answer = "answer"
			e.Status = "completed"
			e.Sequence = 1
			ok, err := d.UpdateSideRequest(ctx, *e)
			if err != nil || !ok {
				t.Fatalf("complete: %v %v", ok, err)
			}
			history, err := d.SideHistory(ctx, s.Parent.Key(), 0, 10)
			if err != nil || len(history) != 1 || history[0].Answer != "answer" {
				t.Fatalf("history: %v %v", history, err)
			}
			if err = d.ClearSideHistory(ctx, s.Parent.Key()); err != nil {
				t.Fatal(err)
			}
			if err = d.DeleteConversation(c.ID); err != nil {
				t.Fatal(err)
			}
		})
		t.Run("records", func(t *testing.T) {
			if err := d.EnsureLLMRecordsTable(); err != nil {
				t.Fatal(err)
			}
			if err := d.EnsureLLMUsageTable(); err != nil {
				t.Fatal(err)
			}
			r := &LLMRecord{Model: "fixture", SessionID: "TASK2SESSION", TaskID: "task2", Worker: "judge", RequestBody: "request", ResponseBody: "response", RawRequest: "raw request", RawResponse: "raw response", InputTokens: 31, OutputTokens: 7, CacheRead: 5, CacheWrite: 4, Status: "ok"}
			if err := d.InsertLLMRecord(r); err != nil {
				t.Fatal(err)
			}
			list, total, err := d.ListLLMRecords("fixture", "task2session", "task2", 0, 10)
			if err != nil || total != 1 {
				t.Fatalf("records: %v %v", list, err)
			}
			full, err := d.GetLLMRecord(list[0].ID)
			if err != nil || full.RawResponse != r.RawResponse || full.RequestBody != r.RequestBody {
				t.Fatalf("body: %+v %v", full, err)
			}
			if err = d.InsertLLMUsage(&LLMUsage{TaskID: "task2", Model: "fixture", Worker: "judge", ProfileName: "task2_primary", InputTokens: 31, OutputTokens: 7, CacheRead: 5, CacheWrite: 4, Status: "ok"}); err != nil {
				t.Fatal(err)
			}
			usage, err := d.UsageByProfile()
			if err != nil || len(usage) == 0 {
				t.Fatalf("usage: %v %v", usage, err)
			}
			daily, err := d.UsageDaily(1)
			if err != nil || len(daily) == 0 {
				t.Fatalf("daily: %v %v", daily, err)
			}
			judge, err := d.JudgeUsageStats(1)
			if err != nil || judge.Calls < 1 {
				t.Fatalf("judge: %v %v", judge, err)
			}
			if err = d.InsertSkillUsage(&SkillUsage{Skill: "task2skill", AgentKey: "worker", Found: true}); err != nil {
				t.Fatal(err)
			}
			stats, err := d.SkillStats()
			if err != nil || len(stats) == 0 || stats[0].LastUsed == nil {
				t.Fatalf("skill stats: %v %v", stats, err)
			}
			if err = d.InsertSkillUsage(&SkillUsage{Skill: "task2missing", AgentKey: "worker", Found: false}); err != nil {
				t.Fatal(err)
			}
			if _, err = d.MissingSkillStats(10); err != nil {
				t.Fatal(err)
			}
			if err = d.InsertToolUsage(&ToolUsage{ToolKey: "task2tool", AgentKey: "worker"}); err != nil {
				t.Fatal(err)
			}
			counts, err := d.ToolUsageCounts()
			if err != nil || counts["task2tool"] != 1 {
				t.Fatalf("tool calls: %v %v", counts, err)
			}
			if _, err = d.InsertLog("info", "task2", "preserved log body"); err != nil {
				t.Fatal(err)
			}
			if _, err = d.RecentLogs(10); err != nil {
				t.Fatal(err)
			}
		})
		t.Run("triggers", func(t *testing.T) {
			a, err := d.CreateAgent("task2agent", "Task2", "fixture")
			if err != nil {
				t.Fatal(err)
			}
			defer d.DeleteAgent(a.Key)
			ver, err := d.SavePrompt(a.ID, "one", "note", "user")
			if err != nil || ver != 1 {
				t.Fatal(err)
			}
			ver, err = d.SavePrompt(a.ID, "two", "note", "user")
			if err != nil || ver != 2 {
				t.Fatal(err)
			}
			versions, err := d.ListPromptVersions(a.ID)
			if err != nil || len(versions) != 2 || versions[0].Template != "two" {
				t.Fatalf("versions: %v %v", versions, err)
			}
			trig, err := d.CreateTrigger(&AgentTrigger{AgentKey: a.Key, Enabled: true, IntervalSec: 120, OnToolCall: true, ToolNames: []string{"task2tool"}})
			if err != nil {
				t.Fatal(err)
			}
			if err = d.TouchTriggerFire(trig.ID); err != nil {
				t.Fatal(err)
			}
			trigs, err := d.ListTriggersFor(a.Key)
			if err != nil || len(trigs) != 1 || trigs[0].LastFire == nil {
				t.Fatalf("trigger: %v %v", trigs, err)
			}
			if err = d.SetSchedState("task2", "42"); err != nil {
				t.Fatal(err)
			}
			got, err := d.GetSchedState("task2")
			if err != nil || got != "42" {
				t.Fatalf("scheduler state: %q %v", got, err)
			}
			if err = d.DeleteTrigger(trig.ID); err != nil {
				t.Fatal(err)
			}
		})
	})
}

func task2Stores(t *testing.T, test func(*testing.T, *DB)) {
	t.Helper()
	t.Run("sqlite", func(t *testing.T) { test(t, portableDB(t)) })
	if dsn := os.Getenv("TASK2_PG_DSN"); dsn != "" {
		t.Run("postgres", func(t *testing.T) {
			// Never reset a shared application database. The one disposable development
			// database is recreated before each contract so repeat runs have no leftovers.
			raw, err := sql.Open("pgx", dsn)
			if err != nil {
				t.Fatal(err)
			}
			var name string
			if err = raw.QueryRow(`SELECT current_database()`).Scan(&name); err != nil {
				raw.Close()
				t.Fatal(err)
			}
			if name != "vh_task2" {
				raw.Close()
				t.Fatalf("refuse reset of non-task2 database %q", name)
			}
			if _, err = raw.Exec(`DROP SCHEMA public CASCADE; CREATE SCHEMA public`); err != nil {
				raw.Close()
				t.Fatal(err)
			}
			raw.Close()
			d, err := Open(dsn)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { d.Close() })
			test(t, d)
		})
	}
}

func equalJSON(a, b []byte) bool {
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return false
	}
	return reflect.DeepEqual(x, y)
}
