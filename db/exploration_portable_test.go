package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/sidequestion"
)

func openExplorationTestDB(t *testing.T) (*DB, error) {
	if strings.HasPrefix(t.Name(), "TestPortableExplorationContract/") {
		return taskContractDB(t), nil
	}
	return Open(testDSN(t))
}

// Contract factories own Close through Cleanup, after all test cleanups. Legacy
// directly invoked tests retain their original deferred close ordering.
func closeExplorationTestDB(t *testing.T, d *DB) {
	if !strings.HasPrefix(t.Name(), "TestPortableExplorationContract/") {
		d.Close()
	}
}

func TestPortableExplorationContract(t *testing.T) {
	for _, engine := range []string{"SQLite", "PostgreSQL"} {
		t.Run(engine, func(t *testing.T) {
			t.Run("Flow", TestExplorationFlow)
			t.Run("IntentControls", TestIntentPauseResumeAndCancelCleanup)
			t.Run("NodeSearch", TestNodesPageQueryMatchesID)
			t.Run("TerminalReads", TestInheritedActivityReadsRequireTerminalIntent)
			t.Run("DirectSources", TestExplorationDirectSourceReadView)
			t.Run("SessionTokens", TestTokenStatsBySessionUsesCompleteIntentHistory)
			t.Run("StateCAS", TestCompareAndSetIntentStateAllowsSingleWinner)
			t.Run("CancelTokens", TestCancelIntentPreservesTokenMeteringWithoutDoubleCount)
			t.Run("CancelShared", TestCancelIntentPreservesOutputsYieldedByAnotherIntent)
			t.Run("SoftDelete", TestSoftDeleteIntent)
			t.Run("HardDelete", TestHardDeleteCascadesExclusiveDescendants)
			t.Run("DeleteShared", TestHardDeletePreservesSharedAndGoal)
			t.Run("LastActivity", testExplorationLastActivity)
			t.Run("Metrics", testExplorationMetrics)
			t.Run("GraphJSONSearchDigest", testExplorationGraphJSONSearchDigest)
			t.Run("Claim16Workers", testExplorationClaim16Workers)
			t.Run("MainSegments", testExplorationMainSegments)
			t.Run("InheritedIntentPages", testExplorationInheritedIntentPages)
			t.Run("DailyBoundary", testExplorationDailyBoundary)
			t.Run("PeerControlBarrier", testExplorationPeerControlBarrier)
			t.Run("Restart", testExplorationRestart)
			t.Run("TraceUnicodeSearch", testExplorationTraceUnicodeSearch)
			t.Run("EightDirectSources", testExplorationEightDirectSources)
			t.Run("SideDeletionBarrier", testExplorationSideDeletionBarrier)
		})
	}
}

func testExplorationSideDeletionBarrier(t *testing.T) {
	d := taskContractDB(t)
	task, err := d.CreateTask("side cleanup", "deleted parent", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	s := d.Exploration(task.ExplorationID)
	intent := mustIntent(t, s, "deleted intent")
	snapshot := sidequestion.Snapshot{Parent: sidequestion.Parent{TaskID: task.ID, ExplorationID: task.ExplorationID, IntentID: intent}, RunID: 1, Version: 1, CapturedAt: time.Now().UTC()}
	if err = d.SaveSideSnapshot(context.Background(), snapshot); err != nil {
		t.Fatal(err)
	}
	if _, err = s.SoftDeleteIntent(intent, "operator deleted"); err != nil {
		t.Fatal(err)
	}
	if got, err := d.SideSnapshot(context.Background(), snapshot.Parent.Key()); err != nil || got != nil {
		t.Fatal(got, err)
	}
	snapshot.Version++
	if err = d.SaveSideSnapshot(context.Background(), snapshot); !errors.Is(err, ErrSideParentGone) {
		t.Fatalf("late side snapshot recreated deleted intent session: %v", err)
	}
}

func testExplorationTraceUnicodeSearch(t *testing.T) {
	d := taskContractDB(t)
	exp, err := d.CreateExploration("trace search", "shared predicates")
	if err != nil {
		t.Fatal(err)
	}
	s := d.Exploration(exp)
	intent := mustIntent(t, s, "terminal")
	other := mustIntent(t, s, "other")
	id, err := s.AppendActivity(Activity{NodeID: &intent, Kind: "tool_result", Summary: "ÄPFEL_测试", Detail: "full ÄPFEL_测试 detail"})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.SetIntentState(intent, "done"); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{"äpfel", `äpfel\_测试`, "ä%测试"} {
		for _, search := range []func() ([]Activity, error){
			func() ([]Activity, error) { return s.ActivityTraceSearch(&intent, q, 1) },
			func() ([]Activity, error) { return s.ActivityTraceSearch(nil, q, 1) },
			func() ([]Activity, error) { return s.ActivityTraceSearchForTerminalIntent(intent, q, 1) },
			func() ([]Activity, error) { return s.ActivityTraceSearchTerminalIntents(q, 1) },
			func() ([]Activity, error) { return s.ActivityTraceSearchExcluding(other, q, 1) },
		} {
			hits, err := search()
			if err != nil || len(hits) != 1 || hits[0].ID != id {
				t.Fatalf("trace search %q=%+v / %v", q, hits, err)
			}
		}
	}
}

func testExplorationEightDirectSources(t *testing.T) {
	d := taskContractDB(t)
	grand, err := d.CreateTask("grand", "private", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	grandFact := mustNode(t, d.Exploration(grand.ExplorationID), KindFact, "indirect")
	ids := []int64{}
	for i := 0; i < MaxTaskSourceCount; i++ {
		source, err := d.CreateTaskWithOptions(fmt.Sprint("source", i), "goal", TaskCreateOptions{SourceTaskIDs: []int64{grand.ID}})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, source.ID)
		mustNode(t, d.Exploration(source.ExplorationID), KindFact, "direct-only")
	}
	task, err := d.CreateTaskWithOptions("current", "goal", TaskCreateOptions{SourceTaskIDs: ids})
	if err != nil {
		t.Fatal(err)
	}
	s := d.Exploration(task.ExplorationID)
	sources, err := s.DirectSourceStores()
	if err != nil || len(sources) != MaxTaskSourceCount {
		t.Fatal(sources, err)
	}
	page, more, total, err := s.ListByKindPageWithSources(KindFact, 0, 3, "direct-only")
	if err != nil || !more || len(page) != 3 || total != MaxTaskSourceCount {
		t.Fatal(page, more, total, err)
	}
	if node, err := s.GetNodeWithSources(grandFact); err != nil || node != nil {
		t.Fatal(node, err)
	}
	// A source which is no longer live immediately disappears from inheritance.
	if _, err = d.Exec(`UPDATE tasks SET deleted_at=now() WHERE id=$1`, ids[0]); err != nil {
		t.Fatal(err)
	}
	sources, err = s.DirectSourceStores()
	if err != nil || len(sources) != MaxTaskSourceCount-1 {
		t.Fatal(sources, err)
	}
}

func testExplorationDailyBoundary(t *testing.T) {
	d := taskContractDB(t)
	exp, err := d.CreateExploration("day boundary", "exact UTC")
	if err != nil {
		t.Fatal(err)
	}
	input := 1
	id, err := d.Exploration(exp).AppendActivity(Activity{Kind: "result", InputTokens: &input})
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC().Truncate(24 * time.Hour).Add(-time.Microsecond)
	if _, err = d.Exec(`UPDATE activity SET created_at=$1 WHERE id=$2`, at, id); err != nil {
		t.Fatal(err)
	}
	buckets, err := d.TokenDailyAll(2)
	if err != nil || len(buckets) != 1 || buckets[0].Day != at.Format(time.DateOnly) {
		t.Fatalf("near-midnight buckets=%+v want=%s err=%v", buckets, at.Format(time.DateOnly), err)
	}
}

func testExplorationPeerControlBarrier(t *testing.T) {
	d := taskContractDB(t)
	peer := taskPeerDB(t, d)
	exp, err := d.CreateExploration("control barrier", "native write transaction")
	if err != nil {
		t.Fatal(err)
	}
	s := d.Exploration(exp)
	intent := mustIntent(t, s, "locked")
	if _, err = s.AppendActivity(Activity{NodeID: &intent, Kind: "result", Detail: "preserved model output"}); err != nil {
		t.Fatal(err)
	}
	tx, err := d.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`UPDATE exploration_nodes SET state='paused', blocked_reason='preserved' WHERE id=$1`, intent); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		close(started)
		_, err := peer.Exploration(exp).SoftDeleteIntent(intent, "operator reason")
		done <- err
	}()
	<-started
	select {
	case err := <-done:
		t.Fatalf("peer control escaped native transaction: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("peer control did not complete")
	}
	n, err := s.GetNode(intent)
	if err != nil || n.State != StateIntentDeleted || n.DeleteReason != "operator reason" {
		t.Fatal(n, err)
	}
	trace, err := s.ActivityTrace(intent, 10)
	if err != nil || len(trace) != 1 {
		t.Fatal(trace, err)
	}
	detail, err := s.ActivityDetail(trace[0].ID)
	if err != nil || detail != "preserved model output" {
		t.Fatal(detail, err)
	}
}

func testExplorationRestart(t *testing.T) {
	d := taskContractDB(t)
	task, err := d.CreateTask("persisted graph", "persisted intent", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	s := d.Exploration(task.ExplorationID)
	intent := mustIntent(t, s, "persisted")
	input := 42
	if _, err = s.AppendActivity(Activity{NodeID: &intent, Worker: "work#2", Kind: "result", InputTokens: &input, Detail: "persisted result"}); err != nil {
		t.Fatal(err)
	}
	if _, err = d.Exec(`UPDATE exploration_nodes SET state='blocked',blocked_reason='quota' WHERE id=$1`, intent); err != nil {
		t.Fatal(err)
	}
	peer := taskPeerDB(t, d)
	if err = d.Close(); err != nil {
		t.Fatal(err)
	}
	s = peer.Exploration(task.ExplorationID)
	n, err := s.GetNode(intent)
	if err != nil || n.State != "blocked" || n.BlockedReason != "quota" {
		t.Fatal(n, err)
	}
	u, err := s.TokenStatsBySession()
	if err != nil || len(u) != 1 || u[0].Session != fmt.Sprintf("intent:%d", intent) || u[0].InputTokens != 42 {
		t.Fatal(u, err)
	}
	if reopened, err := s.ReopenIntent(intent); err != nil || !reopened {
		t.Fatal(reopened, err)
	}
	if won, err := s.ClaimIntent(intent, "work#3"); err != nil || !won {
		t.Fatal(won, err)
	}
	n, err = s.GetNode(intent)
	if err != nil || n.BlockedReason != "" || n.Owner != "work#3" {
		t.Fatal(n, err)
	}
	trace, err := s.ActivityTrace(intent, 10)
	if err != nil || len(trace) != 1 {
		t.Fatal(trace, err)
	}
	detail, err := s.ActivityDetail(trace[0].ID)
	if err != nil || detail != "persisted result" {
		t.Fatal(detail, err)
	}
}

func testExplorationLastActivity(t *testing.T) {
	d := taskContractDB(t)
	exp, err := d.CreateExploration("last activity", "UTC rounding")
	if err != nil {
		t.Fatal(err)
	}
	id, err := d.Exploration(exp).AppendActivity(Activity{Kind: "result"})
	if err != nil {
		t.Fatal(err)
	}
	// PG bigint epoch casts round, rather than truncate, fractional seconds.
	for _, nanos := range []int{499000000, 501000000} {
		at := time.Date(2026, 10, 11, 8, 0, 0, nanos, time.FixedZone("offset", 8*3600))
		if _, err = d.Exec(`UPDATE activity SET created_at=$1 WHERE id=$2`, at, id); err != nil {
			t.Fatal(err)
		}
		got, err := d.LastActivityAll()
		want := at.Unix()
		if nanos >= 500000000 {
			want++
		}
		if err != nil || got[exp] != want {
			t.Fatalf("last activity: got=%v want=%d err=%v", got, want, err)
		}
	}
}

func testExplorationMetrics(t *testing.T) {
	d := taskContractDB(t)
	task, err := d.CreateTask("metrics", "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	s := d.Exploration(task.ExplorationID)
	goal, err := s.AddGoal(map[string]any{"text": "met"}, "human")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.SetNodeState(goal, "met"); err != nil {
		t.Fatal(err)
	}
	intent := mustIntent(t, s, "running")
	if ok, err := s.ClaimIntent(intent, "work#1"); err != nil || !ok {
		t.Fatal(ok, err)
	}
	input := 2147483647
	id, err := s.AppendActivity(Activity{NodeID: &intent, Kind: "result", InputTokens: &input})
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC().Add(-time.Hour).Truncate(time.Second).Add(501 * time.Millisecond)
	if _, err = d.Exec(`UPDATE activity SET created_at=$1 WHERE id=$2`, at, id); err != nil {
		t.Fatal(err)
	}
	id2, err := s.AppendActivity(Activity{NodeID: &intent, Kind: "result", InputTokens: &input})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = d.Exec(`UPDATE activity SET created_at=$1 WHERE id=$2`, at, id2); err != nil {
		t.Fatal(err)
	}
	ignored, err := s.AppendActivity(Activity{Kind: "tool_result", InputTokens: &input})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = d.Exec(`UPDATE activity SET created_at=$1 WHERE id=$2`, at.Add(-time.Second), ignored); err != nil {
		t.Fatal(err)
	}
	empty, err := d.CreateTask("empty metrics", "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = d.Exec(`INSERT INTO findings(task_id,severity,name) VALUES ($1,'high','metric finding')`, task.ID); err != nil {
		t.Fatal(err)
	}
	got, err := d.TaskListMetricsAll()
	if err != nil {
		t.Fatal(err)
	}
	m := got[task.ExplorationID]
	if m.Tokens.InputTokens != input*2 || m.LastActivity != at.Unix()+1 || m.Goals != (GoalCounts{1, 1}) || m.RunningIntents != 1 || m.Findings.High != 1 {
		t.Fatalf("metrics=%+v", m)
	}
	if got[empty.ExplorationID] != (TaskListMetrics{}) {
		t.Fatalf("empty metrics=%+v", got[empty.ExplorationID])
	}
	last, err := d.LastActivityAll()
	if err != nil || last[task.ExplorationID] != m.LastActivity {
		t.Fatal(last, err)
	}
	totals, err := d.TokenTotalsAll()
	if err != nil || totals[task.ExplorationID] != m.Tokens {
		t.Fatal(totals, err)
	}
	goals, err := d.GoalCountsAll()
	if err != nil || goals[task.ExplorationID] != m.Goals {
		t.Fatal(goals, err)
	}
	daily, err := d.TokenDailyAll(2)
	if err != nil || len(daily) != 1 || daily[0].InputTokens != input*2 || daily[0].Day != at.Format(time.DateOnly) {
		t.Fatalf("daily=%+v / %v", daily, err)
	}
	// A recent UTC midnight instant expressed in a non-UTC timezone retains its UTC bucket.
	midnight := time.Now().UTC().Truncate(24 * time.Hour).Add(10 * time.Minute)
	if _, err = d.Exec(`UPDATE activity SET created_at=$1 WHERE id IN ($2,$3)`, midnight.In(time.FixedZone("west", -8*3600)), id, id2); err != nil {
		t.Fatal(err)
	}
	daily, err = d.TokenDailyAll(2)
	if err != nil || len(daily) != 1 || daily[0].Day != midnight.Format(time.DateOnly) {
		t.Fatalf("UTC bucket=%+v / %v", daily, err)
	}
}

func testExplorationGraphJSONSearchDigest(t *testing.T) {
	d := taskContractDB(t)
	task, err := d.CreateTask("graph", "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	s := d.Exploration(task.ExplorationID)
	a, err := d.Assets().UpsertRootDomain(UpsertRootDomainReq{Domain: "graph.invalid"})
	if err != nil {
		t.Fatal(err)
	}
	payload := map[string]any{"summary": "ÄPFEL_测试", "precise": json.Number("9007199254740993"), "nested": map[string]any{"items": []any{true, nil, json.Number("9007199254740993")}}}
	fact, err := s.AddNode(KindFact, payload, 0, "confirmed", "human", []int64{a})
	if err != nil {
		t.Fatal(err)
	}
	hint, err := s.AddNode(KindHint, map[string]any{"summary": "ÄPFEL_测试"}, 0, "active", "human", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Link(fact, RelDerivedFrom, hint); err != nil {
		t.Fatal(err)
	}
	n, err := s.GetNode(fact)
	if err != nil || !strings.Contains(string(n.Payload), "9007199254740993") {
		t.Fatalf("payload=%+v / %v", n, err)
	}
	var decoded map[string]json.RawMessage
	if err = json.Unmarshal(n.Payload, &decoded); err != nil || string(decoded["precise"]) != "9007199254740993" || decoded["nested"][0] != '{' {
		t.Fatalf("typed JSON=%s / %v", n.Payload, err)
	}
	for _, q := range []string{"äpfel", `äpfel\_测试`, "ä%测试"} {
		page, more, total, err := s.ListByKindPageWithSources(KindFact, 0, 1, q)
		if err != nil || more || total != 1 || len(page) != 1 || page[0].ID != fact {
			t.Fatalf("kind search %q=%+v/%v/%d/%v", q, page, more, total, err)
		}
		page, total, err = s.NodesPage(NodeFilter{Kinds: []string{KindFact}, Query: q}, 1, 1)
		if err != nil || total != 1 || len(page) != 1 {
			t.Fatalf("node search %q=%+v/%d/%v", q, page, total, err)
		}
	}
	ids, err := s.NodesByIDs([]int64{fact, hint})
	if err != nil || len(ids) != 2 {
		t.Fatal(ids, err)
	}
	edges, err := s.EdgesTouching([]int64{hint})
	if err != nil || len(edges) != 1 {
		t.Fatal(edges, err)
	}
	anchor, err := s.NodeAssets([]int64{fact, hint})
	if err != nil || len(anchor[fact]) != 1 || anchor[fact][0] != a {
		t.Fatal(anchor, err)
	}
	refs, err := s.AssetRefs(a)
	if err != nil || len(refs) != 1 || refs[0].ID != fact {
		t.Fatal(refs, err)
	}
	if _, err = s.BumpRound(); err != nil {
		t.Fatal(err)
	}
	if err = s.ApplyStampOps([]StampOp{{ID: fact, Set: true, Round: 1}}); err != nil {
		t.Fatal(err)
	}
	stamps, err := s.ColdStamps()
	if err != nil || stamps[fact] == nil || *stamps[fact] != 1 {
		t.Fatal(stamps, err)
	}
	digest, err := s.AddDigest(payload, []int64{fact})
	if err != nil {
		t.Fatal(err)
	}
	members, err := s.DigestMembers(digest)
	if err != nil || len(members) != 1 || members[0] != fact {
		t.Fatal(members, err)
	}
	covered, err := s.CoveredMembers()
	if err != nil || covered[fact] != digest {
		t.Fatal(covered, err)
	}
	versions, err := s.ContentVersions()
	if err != nil {
		t.Fatal(err)
	}
	if err = s.SetNodeState(fact, "dismissed"); err != nil {
		t.Fatal(err)
	}
	now, err := s.ContentVersions()
	if err != nil || now[fact] != versions[fact]+1 {
		t.Fatal(now, err)
	}
	if err = s.SupersedeDigests([]int64{digest}); err != nil {
		t.Fatal(err)
	}
	covered, err = s.CoveredMembers()
	if err != nil || len(covered) != 0 {
		t.Fatal(covered, err)
	}
	digests, err := s.ActiveDigests()
	if err != nil || len(digests) != 0 {
		t.Fatal(digests, err)
	}
	constraint, err := s.AddConstraint("deny", "no mutation", "human")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.UpdateConstraint(constraint, "allow", "allowed"); err != nil {
		t.Fatal(err)
	}
	cs, err := s.ListConstraints()
	if err != nil || len(cs) != 1 || cs[0].Text != "allowed" {
		t.Fatal(cs, err)
	}
	if err = s.DeleteConstraint(constraint); err != nil {
		t.Fatal(err)
	}
	open := mustIntent(t, s, "discard")
	if err = s.DiscardOpenIntent(open); err != nil || !gone(t, s, open) {
		t.Fatal(err)
	}
}

func testExplorationClaim16Workers(t *testing.T) {
	d := taskContractDB(t)
	exp, err := d.CreateExploration("claim", "atomic")
	if err != nil {
		t.Fatal(err)
	}
	s := d.Exploration(exp)
	intent := mustIntent(t, s, "one intent")
	var peer *DB
	if d.IsSQLite() {
		var path string
		rows, err := d.Query(`PRAGMA database_list`)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var seq int
			var name, file string
			if err = rows.Scan(&seq, &name, &file); err != nil {
				t.Fatal(err)
			}
			if name == "main" {
				path = file
			}
		}
		rows.Close()
		if err = rows.Err(); err != nil {
			t.Fatal(err)
		}
		peer, err = OpenSQLite(path)
		if err != nil {
			t.Fatal(err)
		}
	} else {
		pool, err := sql.Open("pgx", os.Getenv("ARTEX_TASK_TEST_PG_DSN"))
		if err != nil {
			t.Fatal(err)
		}
		peer = &DB{DB: pool}
	}
	t.Cleanup(func() { peer.Close() })
	start := make(chan struct{})
	results := make(chan bool, 16)
	errs := make(chan error, 16)
	for i := 0; i < 16; i++ {
		go func(worker int) {
			<-start
			store := s
			if worker%2 != 0 {
				store = peer.Exploration(exp)
			}
			won, err := store.ClaimIntent(intent, fmt.Sprintf("work#%d", worker))
			results <- won
			errs <- err
		}(i)
	}
	close(start)
	wins := 0
	for i := 0; i < 16; i++ {
		if <-results {
			wins++
		}
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	if wins != 1 {
		t.Fatalf("claims won=%d", wins)
	}
	if _, err = s.SoftDeleteIntent(intent, "closed"); err != nil {
		t.Fatal(err)
	}
	if won, err := peer.Exploration(exp).ClaimIntent(intent, "late"); err != nil || won {
		t.Fatal(won, err)
	}
	if _, err = s.CancelIntent(intent); err != nil {
		t.Fatal(err)
	}
	if won, err := peer.Exploration(exp).ClaimIntent(intent, "after delete"); err != nil || won {
		t.Fatal(won, err)
	}
}

func testExplorationMainSegments(t *testing.T) {
	d := taskContractDB(t)
	exp, err := d.CreateExploration("segments", "identity")
	if err != nil {
		t.Fatal(err)
	}
	s := d.Exploration(exp)
	for _, seg := range []int{0, 1, 2} {
		if seg > 0 {
			m, err := s.NewMainSession()
			if err != nil || m.Seq != seg || m.CreatedAt.IsZero() {
				t.Fatal(m, err)
			}
		}
		n := seg + 1
		if _, err = s.AppendActivity(Activity{Worker: "mainagent", Kind: "result", MainSeg: &seg, InputTokens: &n, Metadata: json.RawMessage(`{"id":9007199254740993,"nested":[true,null]}`)}); err != nil {
			t.Fatal(err)
		}
	}
	ss, err := s.ListMainSessions()
	if err != nil || len(ss) != 3 || ss[0].Seq != 2 || ss[2].Seq != 0 {
		t.Fatal(ss, err)
	}
	sessions, err := s.TokenStatsBySession()
	if err != nil || len(sessions) != 3 {
		t.Fatal(sessions, err)
	}
	for _, usage := range sessions {
		var seg int
		if _, err = fmt.Sscanf(usage.Session, "main:%d", &seg); err != nil || usage.InputTokens != seg+1 {
			t.Fatal(usage, err)
		}
	}
	seg := 1
	page, more, err := s.ActivityPage(ActivitySessionFilter{Main: true, MainSeg: &seg}, 0, 1)
	if err != nil || more || len(page) != 1 || *page[0].MainSeg != 1 || !strings.Contains(string(page[0].Metadata), "9007199254740993") {
		t.Fatal(page, more, err)
	}
	all, cursor, err := s.ActivityList(nil, 0, 2)
	if err != nil || len(all) != 2 || cursor != all[1].ID {
		t.Fatal(all, cursor, err)
	}
	older, more, err := s.ActivityPage(ActivitySessionFilter{Main: true}, 0, 1)
	if err != nil || more || len(older) != 1 || *older[0].MainSeg != 0 {
		t.Fatal(older, more, err)
	}
}

func testExplorationInheritedIntentPages(t *testing.T) {
	d := taskContractDB(t)
	source, err := d.CreateTask("source", "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	current, err := d.CreateTaskWithOptions("local", "goal", TaskCreateOptions{SourceTaskIDs: []int64{source.ID}})
	if err != nil {
		t.Fatal(err)
	}
	sourceStore := d.Exploration(source.ExplorationID)
	s := d.Exploration(current.ExplorationID)
	terminal := mustIntent(t, sourceStore, "visible")
	if err = sourceStore.SetIntentState(terminal, "done"); err != nil {
		t.Fatal(err)
	}
	// Newer live source rows may neither consume the page nor inflate its total.
	for i := 0; i < 3; i++ {
		mustIntent(t, sourceStore, "live")
	}
	local := mustIntent(t, s, "local live")
	first, more, total, err := s.ListByKindPageWithSources(KindIntent, 0, 1, "")
	if err != nil || len(first) != 1 || first[0].ID != local || first[0].Inherited || !more || total != 2 {
		t.Fatalf("first page=%+v more=%v total=%d err=%v", first, more, total, err)
	}
	second, more, total, err := s.ListByKindPageWithSources(KindIntent, local, 1, "")
	if err != nil || len(second) != 1 || second[0].ID != terminal || !second[0].Inherited || more || total != 2 {
		t.Fatalf("second page=%+v more=%v total=%d err=%v", second, more, total, err)
	}
	found, more, total, err := s.ListByKindPageWithSources(KindIntent, 0, 1, "live")
	if err != nil || len(found) != 1 || found[0].ID != local || more || total != 1 {
		t.Fatalf("search page=%+v more=%v total=%d err=%v", found, more, total, err)
	}
}
