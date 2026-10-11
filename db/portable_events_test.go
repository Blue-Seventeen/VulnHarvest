package db

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/artex/notify"
)

func TestPortableNotificationContracts(t *testing.T) {
	task2Stores(t, func(t *testing.T, d *DB) {
		ctx := context.Background()
		ch := newTestChannel(t, d, "feishu", NotifyModeRealtime, `{}`)
		got, err := d.NotificationChannelByID(ctx, ch.ID)
		if err != nil || !equalJSON(got.Config, ch.Config) {
			t.Fatalf("channel: %+v %v", got, err)
		}
		// Snapshot-only event fixtures do not deliver notifications or invoke clients.
		addTestEvent(t, d, notify.EventFindingCreated, 123, notify.Snapshot{Name: "fixture", Severity: "high"})
		ec, dc, err := d.FanOutPendingEvents(ctx, 100)
		if err != nil || ec != 1 || dc != 1 {
			t.Fatalf("fanout: %d %d %v", ec, dc, err)
		}
		var wg sync.WaitGroup
		var count atomic.Int32
		var claimedID atomic.Int64
		for i := 0; i < 8; i++ {
			wg.Go(func() {
				rows, err := d.ClaimRealtimeDeliveries(ctx, ch.ID, 10, time.Minute)
				if err != nil {
					t.Error(err)
					return
				}
				count.Add(int32(len(rows)))
				if len(rows) > 0 {
					claimedID.Store(rows[0].ID)
					if rows[0].Attempts != 1 || !rows[0].NextAttemptAt.After(time.Now()) {
						t.Error("lease not advanced")
					}
				}
			})
		}
		wg.Wait()
		if count.Load() != 1 {
			t.Fatalf("duplicate claim: %d", count.Load())
		}
		id := claimedID.Load()
		if err = d.RescheduleDeliveries(ctx, []int64{id}, time.Minute, "retry"); err != nil {
			t.Fatal(err)
		}
		rows, err := d.ClaimRealtimeDeliveries(ctx, ch.ID, 10, time.Minute)
		if err != nil || len(rows) != 0 {
			t.Fatalf("retry cooldown lost: %v %v", rows, err)
		}
		past := time.Now().Add(-time.Minute).In(time.FixedZone("CST", 8*3600))
		if _, err = d.Exec(`UPDATE notification_deliveries SET next_attempt_at=$1 WHERE id=$2`, past, id); err != nil {
			t.Fatal(err)
		}
		rows, err = d.ClaimRealtimeDeliveries(ctx, ch.ID, 10, time.Minute)
		if err != nil || len(rows) != 1 || rows[0].Attempts != 2 {
			t.Fatalf("expired lease: %v %v", rows, err)
		}
		if err = d.MarkDeliveriesSent(ctx, []int64{id}); err != nil {
			t.Fatal(err)
		}
		stats, err := d.NotificationStatsSnapshot(ctx)
		if err != nil || stats.SentToday != 1 {
			t.Fatalf("stats: %+v %v", stats, err)
		}
		digest := newTestChannel(t, d, "feishu", NotifyModeDigest, `{}`)
		addTestEvent(t, d, notify.EventFindingCreated, 124, notify.Snapshot{Name: "digest", Severity: "high"})
		if _, _, err = d.FanOutPendingEvents(ctx, 100); err != nil {
			t.Fatal(err)
		}
		if _, err = d.Exec(`UPDATE notification_deliveries SET created_at=$1 WHERE channel_id=$2`, past, digest.ID); err != nil {
			t.Fatal(err)
		}
		due, err := d.DigestBatchDue(ctx, digest.ID, 30*time.Second)
		if err != nil || !due {
			t.Fatalf("digest due: %v %v", due, err)
		}
		rows, err = d.ClaimDigestBatch(ctx, digest.ID, 10, time.Minute)
		if err != nil || len(rows) != 1 || rows[0].BatchID == nil {
			t.Fatalf("digest: %v %v", rows, err)
		}
		if err = d.SetNotificationChannelEnabled(ctx, digest.ID, false); err != nil {
			t.Fatal(err)
		}
		disabled, _, err := d.ListNotificationDeliveries(ctx, NotificationDeliveryFilter{ChannelID: digest.ID}, 0, 10)
		if err != nil || len(disabled) != 1 || disabled[0].State != NotifyStateSkipped {
			t.Fatalf("disabled: %v %v", disabled, err)
		}
		var findingID int64
		if err = d.QueryRow(`INSERT INTO findings(vulnclass,name,status) VALUES('fixture','fixture','pending') RETURNING id`).Scan(&findingID); err != nil {
			t.Fatal(err)
		}
		defer d.Exec(`DELETE FROM findings WHERE id=$1`, findingID)
		from, found, notified, err := d.SetFindingStatusWithNotify(ctx, findingID, "fixed")
		if err != nil || !found || !notified || from != "pending" {
			t.Fatalf("status: %q %v %v %v", from, found, notified, err)
		}
	})
}

func TestPortableInterceptContracts(t *testing.T) {
	task2Stores(t, func(t *testing.T, d *DB) {
		rule, err := d.CreateInterceptRule("task2-returning", "tool_name", "string", "fixture", "ask", "fixture", 1, true, false, 0, "deny")
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Millisecond)
		updated, err := d.UpdateInterceptRule(rule.ID, "edited", "tool_name", "string", "fixture", "ask", "fixture", 2, true, false, 0, "deny")
		if err != nil || !updated.UpdatedAt.After(rule.UpdatedAt) {
			t.Fatalf("RETURNING stale timestamp: %v / %v / %v", rule.UpdatedAt, updated.UpdatedAt, err)
		}
		c, err := d.CreateConversation("worker", "approval", nil)
		if err != nil {
			t.Fatal(err)
		}
		audit := &InterceptAudit{RunID: "run", ToolUseID: "call", Correlation: "exact", InitialAction: "ask", ExecutionStatus: "not_started", ModelInput: json.RawMessage(`{"nested":{"a":1},"nullable":null}`)}
		id, err := d.CreateInterceptPending(0, c.ID, "", "worker", "fixture", []byte(`{"path":"fixture"}`), "reason", audit)
		if err != nil {
			t.Fatal(err)
		}
		pending, err := d.GetInterceptPending(id)
		if err != nil || pending == nil || !equalJSON(pending.ToolInput, []byte(`{"path":"fixture"}`)) {
			t.Fatalf("pending JSON: %+v %v", pending, err)
		}
		var wg sync.WaitGroup
		var wins atomic.Int32
		for i := 0; i < 8; i++ {
			wg.Go(func() {
				ok, err := d.ResolveIntercept(id, "allowed", "allow", "approved")
				if err != nil {
					t.Error(err)
				}
				if ok {
					wins.Add(1)
				}
			})
		}
		wg.Wait()
		if wins.Load() != 1 {
			t.Fatalf("decision count: %d", wins.Load())
		}
		if err = d.CompleteIntercept(id, "run", "call", "succeeded", "exact output", false); err != nil {
			t.Fatal(err)
		}
		detail, err := d.GetInterceptDetail(id)
		if err != nil || detail.Audit == nil || detail.Audit.Output != "exact output" || detail.Audit.InitialAction != "ask" {
			t.Fatalf("audit: %+v %v", detail, err)
		}
		if _, err = d.AppendConvActivity(c.ID, Activity{Kind: "tool_use", Tool: "fixture", ToolUseID: "call", Summary: "use"}); err != nil {
			t.Fatal(err)
		}
		target, err := d.GetInterceptExecution(id)
		if err != nil || target == nil || len(target.Items) != 1 {
			t.Fatalf("execution: %+v %v", target, err)
		}
		// Explicit shallow override preserves a null key and replaces the entire nested object.
		if d.IsSQLite() {
			var merged string
			if err = d.QueryRow(`SELECT portable_json_overlay($1,$2)`, `{"nested":{"a":1},"nullable":1}`, `{"nested":{"b":2},"nullable":null}`).Scan(&merged); err != nil {
				t.Fatal(err)
			}
			var obj map[string]json.RawMessage
			json.Unmarshal([]byte(merged), &obj)
			if string(obj["nested"]) != `{"b":2}` || string(obj["nullable"]) != "null" {
				t.Fatalf("overlay semantics: %s", merged)
			}
		}
	})
}
