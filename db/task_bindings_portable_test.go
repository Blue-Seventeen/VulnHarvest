package db

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func closeTaskBindingsDB(t *testing.T, d *DB) {
	if !strings.HasPrefix(t.Name(), "TestPortableTaskBindingsContract/") {
		d.Close()
	}
}
func TestPortableTaskBindingsContract(t *testing.T) {
	for _, engine := range []string{"SQLite", "PostgreSQL"} {
		t.Run(engine, func(t *testing.T) {
			t.Run("RegisterScopes", TestRegisterTaskAssetScopesCreatesAssetsAndPersistsTextScope)
			t.Run("AttachDetach", TestTaskAssetAttachDetachPreservesGlobalAssetAndAnchors)
			t.Run("IntentProvenance", TestIntentAssetsIncludesDirectSourceProvenance)
			t.Run("DirectContext", TestTaskAssetContextUsesDirectSourceScopeAndAnchors)
			t.Run("CascadeAssets", TestTaskDeleteCascadeAssets)
			t.Run("Lifecycle", TestTaskLifecycleAndDeleteCascade)
			t.Run("ScopedWrites", testTaskBindingScopedWrites)
			t.Run("TaskAwareScopedTx", testTaskAwareScopedTx)
			t.Run("CoverageSemantics", testTaskCoverageSemantics)
			t.Run("MappedCSegment", testTaskMappedCSegment)
			t.Run("ExistingUpsertIP", TestUpsertIP)
			t.Run("DeleteRetainsAssets", testTaskDeleteRetainsAssets)
			t.Run("PrepareBarrier", testTaskPrepareBarrier)
			t.Run("DeleteRecordsAndRelations", testTaskDeleteRecordsAndRelations)
			t.Run("MutationErrors", testTaskBindingMutationErrors)
			t.Run("ScopeCanonicalization", testTaskScopeCanonicalization)
			t.Run("DeleteMutationRollback", testTaskDeleteMutationRollback)
			t.Run("ConcurrentControls", testTaskBindingConcurrentControls)
			t.Run("CreateDeleteLockOrder", testTaskCreateDeleteLockOrder)
			if engine == "PostgreSQL" {
				t.Run("PGDeleteLocks", func(t *testing.T) { taskContractDB(t); TestDeleteTaskTrafficHostsUseOneLockedTransaction(t) })
				t.Run("PGTaskProfileLocks", func(t *testing.T) { taskContractDB(t); TestTaskLLMProfileMutationsLockTaskBeforeProfile(t) })
			}
		})
	}
}

func testTaskCreateDeleteLockOrder(t *testing.T) {
	d := taskContractDB(t)
	source, err := d.CreateTask("source FK lock", "source", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	company, _, err := d.Companies().UpsertCompany("ordered source create", "")
	if err != nil {
		t.Fatal(err)
	}
	creator := taskPeerDB(t, d)
	deleter := taskPeerDB(t, d)
	tx, err := d.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(d.SQL(`SELECT id FROM tasks WHERE id=$1 FOR UPDATE`, `UPDATE tasks SET paused=true WHERE id=$1`), source.ID); err != nil {
		t.Fatal(err)
	}
	creatorPID := prepareTaskPeerObservation(t, creator)
	created := make(chan error, 1)
	var task *Task
	go func() {
		var err error
		task, err = creator.CreateTaskWithOptions("ordered dependent", "goal", TaskCreateOptions{CompanyIDs: []int64{company}, SourceTaskIDs: []int64{source.ID}})
		created <- err
	}()
	waitTaskPeerObservation(t, d, creator, creatorPID, created)
	deleteDone := make(chan error, 1)
	if !d.IsSQLite() {
		probe, err := d.Begin()
		if err != nil {
			t.Fatal(err)
		}
		var acquired bool
		err = probe.QueryRow(`SELECT pg_try_advisory_xact_lock($1)`, companyScopeMutationLock).Scan(&acquired)
		probe.Rollback()
		if err != nil || acquired {
			t.Fatal("creator waited on source FK before company lock", acquired, err)
		}
		deleterPID := prepareTaskPeerObservation(t, deleter)
		go func() { _, err := deleter.DeleteTaskCascade(source.ID, false, false); deleteDone <- err }()
		waitTaskPeerObservation(t, d, deleter, deleterPID, deleteDone)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := waitForTaskDeleteResult(created); err != nil {
		t.Fatal(err)
	}
	if d.IsSQLite() {
		_, err := deleter.DeleteTaskCascade(source.ID, false, false)
		deleteDone <- err
	}
	if err := waitForTaskDeleteResult(deleteDone); err != nil {
		t.Fatal(err)
	}
	got, err := d.GetTask(task.ID)
	if err != nil || got == nil || len(got.CompanyIDs) != 1 || got.CompanyIDs[0] != company || len(got.SourceTaskIDs) != 0 {
		t.Fatal(got, err)
	}
	if gone, err := d.GetTask(source.ID); err != nil || gone != nil {
		t.Fatal(gone, err)
	}
}

func testTaskAwareScopedTx(t *testing.T) {
	d := taskContractDB(t)
	task, err := d.CreateTask("owning transaction", "task aware write", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	intent := mustIntent(t, d.Exploration(task.ExplorationID), "owner")
	tx, err := d.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE tasks SET deleted_at=now() WHERE id=$1`, task.ID); err != nil {
		t.Fatal(err)
	}
	s := &AssetStore{db: d, company: d.Companies(), tx: tx}
	if _, err := s.WithTaskAssetMutation(task.ID, func(*AssetStore) (int64, error) { t.Fatal("callback ran for uncommitted deleted task"); return 0, nil }); !errors.Is(err, ErrTaskAssetTaskNotFound) {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`UPDATE tasks SET deleted_at=NULL WHERE id=$1`, task.ID); err != nil {
		t.Fatal(err)
	}
	id, err := s.WithTaskAssetMutation(task.ID, func(scoped *AssetStore) (int64, error) {
		id, err := scoped.UpsertRootDomain(UpsertRootDomainReq{Domain: "owned-tx.test", TaskID: task.ID})
		if err != nil {
			return 0, err
		}
		if err := scoped.SetTaskAssetSource(task.ID, id, "agent", "owning tx", &intent); err != nil {
			return 0, err
		}
		if err := scoped.AddAutoScope(task.ID, "root_domain", "owned-tx.test", "", ""); err != nil {
			return 0, err
		}
		if err := scoped.AnchorAsset(intent, id); err != nil {
			return 0, err
		}
		return id, nil
	})
	if err != nil || id <= 0 {
		t.Fatal(id, err)
	}
	var n int
	if err := tx.QueryRow(`SELECT count(*) FROM exploration_anchors WHERE node_id=$1 AND asset_id=$2`, intent, id).Scan(&n); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	if err := d.readPool().QueryRow(`SELECT count(*) FROM assets WHERE id=$1`, id).Scan(&n); err != nil || n != 0 {
		t.Fatal("uncommitted asset visible", n, err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := d.QueryRow(`SELECT count(*) FROM assets WHERE id=$1`, id).Scan(&n); err != nil || n != 0 {
		t.Fatal("helper committed caller transaction", n, err)
	}
}

func testTaskScopeCanonicalization(t *testing.T) {
	d := taskContractDB(t)
	task, err := d.CreateTask("canonical scope", "same cidr identity", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	s := d.Assets()
	for _, network := range []string{"2001:0DB8:ABCD:0000::/64", "2001:db8:abcd::/64"} {
		if _, err := s.AddAgentScope(task.ID, "cidr", network, "canonical", "agent"); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.AddAutoScope(task.ID, "ip", "", "", "fe80::1%eth0"); err != nil {
		t.Fatal(err)
	}
	scopes, err := s.ListTaskScope(task.ID)
	if err != nil || len(scopes) != 1 || scopes[0].Net != "2001:db8:abcd::/64" {
		t.Fatal(scopes, err)
	}
}

func testTaskBindingMutationErrors(t *testing.T) {
	d := taskContractDB(t)
	task, err := d.CreateTask("invalid associations", "atomic changes", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	asset, err := d.Assets().UpsertRootDomain(UpsertRootDomainReq{Domain: "errors.test"})
	if err != nil {
		t.Fatal(err)
	}
	s := d.Assets()
	if _, err := s.AttachAssetsToTask(task.ID, []int64{asset, 9007199254740993}, "missing"); !errors.Is(err, ErrTaskAssetAssetNotFound) {
		t.Fatal(err)
	}
	if rows, err := s.QueryByTask(task.ID, "", 10, 0); err != nil || len(rows) != 0 {
		t.Fatal(rows, err)
	}
	if _, err := s.RegisterTaskAssetScopes(9007199254740993, []ScopeInput{{Kind: "domain", Value: "missing.test"}}); !errors.Is(err, ErrTaskAssetTaskNotFound) {
		t.Fatal(err)
	}
	for _, network := range []string{"invalid", "192.0.2.9/24", "fe80::1%eth0/128"} {
		if err := s.upsertTaskScope(TaskScope{TaskID: task.ID, Kind: "cidr", Net: network}); err == nil {
			t.Errorf("invalid cidr %q was stored", network)
		}
	}
	if scopes, err := s.ListTaskScope(task.ID); err != nil || len(scopes) != 0 {
		t.Fatal(scopes, err)
	}
}

func testTaskDeleteMutationRollback(t *testing.T) {
	d := taskContractDB(t)
	first, _, host, asset := createTaskDeleteRaceFixture(t, d)
	if _, err := d.Exec(`CREATE TABLE task_delete_restrict(task_id BIGINT REFERENCES tasks(id) ON DELETE RESTRICT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`INSERT INTO task_delete_restrict(task_id) VALUES ($1)`, first.ID); err != nil {
		t.Fatal(err)
	}
	prepared := false
	_, err := d.DeleteTaskCascadePrepared(first.ID, true, false, false, func(p TaskDeletePreparation) error {
		prepared = true
		if !containsDeleteHost(p.TrafficHosts, host) {
			return fmt.Errorf("exclusive host missing: %v", p)
		}
		return nil
	})
	if !prepared || err == nil {
		t.Fatal("post-prepare mutation failure", prepared, err)
	}
	if got, err := d.GetTask(first.ID); err != nil || got == nil {
		t.Fatal(got, err)
	}
	rows, err := d.Assets().GetByIDs([]int64{asset})
	if err != nil || len(rows) != 1 || len(rows[0].TaskIDs) != 1 || rows[0].TaskIDs[0] != first.ID {
		t.Fatal(rows, err)
	}
}

func testTaskBindingConcurrentControls(t *testing.T) {
	d := taskContractDB(t)
	task, err := d.CreateTask("concurrent controls", "preserve asset writes", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	peer := taskPeerDB(t, d)
	s := d.Exploration(task.ExplorationID)
	intent := mustIntent(t, s, "controlled")
	asset, err := d.Assets().UpsertRootDomain(UpsertRootDomainReq{Domain: "concurrent-control.test", TaskID: task.ID})
	if err != nil {
		t.Fatal(err)
	}
	claim := func(want int) {
		start := make(chan struct{})
		type result struct {
			won bool
			err error
		}
		results := make(chan result, 16)
		for i := 0; i < 16; i++ {
			go func(i int) {
				<-start
				store := s
				if i%2 == 1 {
					store = peer.Exploration(task.ExplorationID)
				}
				won, err := store.ClaimIntent(intent, fmt.Sprintf("worker#%d", i))
				results <- result{won, err}
			}(i)
		}
		close(start)
		wins := 0
		for i := 0; i < 16; i++ {
			select {
			case r := <-results:
				if r.err != nil {
					t.Fatal(r.err)
				}
				if r.won {
					wins++
				}
			case <-time.After(10 * time.Second):
				t.Fatal("claim timed out")
			}
		}
		if wins != want {
			t.Fatalf("claim winners=%d want %d", wins, want)
		}
	}
	claim(1)
	for _, transition := range [][2]string{{"running", "paused"}, {"paused", "open"}} {
		start := make(chan struct{})
		done := make(chan error, 2)
		go func() {
			<-start
			changed, err := s.CompareAndSetIntentState(intent, transition[0], transition[1])
			if err == nil && !changed {
				err = fmt.Errorf("CAS lost %v", transition)
			}
			done <- err
		}()
		go func() {
			<-start
			_, err := peer.Assets().AttachAssetsToTask(task.ID, []int64{asset}, "parallel writer")
			done <- err
		}()
		close(start)
		for i := 0; i < 2; i++ {
			if err := waitForTaskDeleteResult(done); err != nil {
				t.Fatal(err)
			}
		}
	}
	claim(1)
	// Global asset writes survive intent cancellation.
	start := make(chan struct{})
	done := make(chan error, 2)
	go func() { <-start; _, err := s.CancelIntent(intent); done <- err }()
	go func() {
		<-start
		_, err := peer.Assets().AttachAssetsToTask(task.ID, []int64{asset}, "cancel writer")
		done <- err
	}()
	close(start)
	for i := 0; i < 2; i++ {
		if err := waitForTaskDeleteResult(done); err != nil {
			t.Fatal(err)
		}
	}
	claim(0)
	if rows, err := peer.Assets().QueryByTask(task.ID, "", 10, 0); err != nil || len(rows) != 1 || rows[0].TaskSourceSummary != "cancel writer" {
		t.Fatal(rows, err)
	}
	if _, err := d.DeleteTaskCascade(task.ID, true, false); err != nil {
		t.Fatal(err)
	}
	claim(0)
	if rows, err := peer.Assets().GetByIDs([]int64{asset}); err != nil || len(rows) != 0 {
		t.Fatal(rows, err)
	}
}

func testTaskMappedCSegment(t *testing.T) {
	d := taskContractDB(t)
	for _, tc := range []struct{ ip, segment string }{
		{"192.0.2.9", "192.0.2.0/24"},
		{"2001:db8:abcd:1::9", "2001:db8:abcd::/48"},
		{"::ffff:192.0.2.9", "::/48"},
		{"::ffff:c000:209", "::/48"},
		{"invalid", ""},
		{"fe80::1%eth0", ""},
	} {
		if got := calcCSegment(tc.ip); got != tc.segment {
			t.Errorf("calcCSegment(%q)=%q want %q", tc.ip, got, tc.segment)
		}
	}
	company, _, err := d.Companies().UpsertCompany("mapped family", "")
	if err != nil {
		t.Fatal(err)
	}
	// This fixture models a persisted 128-bit network; the existing free-form
	// company parser intentionally remains outside this narrow family fix.
	if _, err := d.Exec(d.SQL(`INSERT INTO company_scope(company_id,kind,net,raw) VALUES ($1,'cidr',$2::cidr,$2)`, `INSERT INTO company_scope(company_id,kind,net,raw) VALUES ($1,'cidr',$2,$2)`), company, "::ffff:192.0.2.0/120"); err != nil {
		t.Fatal(err)
	}
	asset, err := d.Assets().UpsertIP(UpsertIPReq{IP: "::ffff:192.0.2.9"})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := d.Assets().GetByIDs([]int64{asset})
	if err != nil || len(rows) != 1 || rows[0].CompanyID == nil || *rows[0].CompanyID != company || rows[0].CSegment != "::/48" {
		t.Fatal(rows, err)
	}
}

// The SQLite probe returns a real native SQLITE_BUSY while the holder remains
// active. The second phase observes the original DAO's leased writer, then its
// completion after commit. PG observes the actual backend's Lock wait instead.
func prepareTaskPeerObservation(t *testing.T, peer *DB) int {
	t.Helper()
	peer.DB.SetMaxOpenConns(1)
	peer.DB.SetMaxIdleConns(1)
	if !peer.IsSQLite() {
		var pid int
		if err := peer.DB.QueryRow(`SELECT pg_backend_pid()`).Scan(&pid); err != nil {
			t.Fatal(err)
		}
		return pid
	}
	if _, err := peer.DB.Exec(`PRAGMA busy_timeout=100`); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	tx, err := peer.Begin()
	if err == nil {
		tx.Rollback()
		t.Fatal("native peer acquired held immediate writer lock")
	}
	if !IsBusyError(err) || time.Since(started) < 70*time.Millisecond {
		t.Fatal("native SQLite contention not observed", time.Since(started), err)
	}
	if _, err := peer.DB.Exec(`PRAGMA busy_timeout=5000`); err != nil {
		t.Fatal(err)
	}
	return 0
}

func waitTaskPeerObservation(t *testing.T, observer, peer *DB, pid int, done <-chan error) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-done:
			t.Fatalf("peer completed before holder commit: %v", err)
		default:
		}
		if peer.IsSQLite() {
			if peer.DB.Stats().InUse == 1 {
				return
			}
		} else {
			var blocked bool
			if err := observer.readPool().QueryRow(`SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE pid=$1 AND wait_event_type='Lock')`, pid).Scan(&blocked); err != nil {
				t.Fatal(err)
			}
			if blocked {
				return
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("peer never entered native DB contention phase")
}

func testTaskPrepareBarrier(t *testing.T) {
	for _, mutation := range []string{"asset", "anchor", "newAsset"} {
		t.Run(mutation, func(t *testing.T) {
			d := taskContractDB(t)
			first, second, host, asset := createTaskDeleteRaceFixture(t, d)
			// Keep the asset alive via a second membership; delete must detach only first.
			if _, err := d.Assets().AttachAssetsToTask(second.ID, []int64{asset}, "shared"); err != nil {
				t.Fatal(err)
			}
			node, err := d.Exploration(first.ExplorationID).OriginFactID()
			if err != nil {
				t.Fatal(err)
			}
			peer := taskPeerDB(t, d)
			prepared := make(chan TaskDeletePreparation, 1)
			release := make(chan struct{})
			deleteDone := make(chan error, 1)
			go func() {
				_, err := d.DeleteTaskCascadePrepared(first.ID, true, false, false, func(p TaskDeletePreparation) error { prepared <- p; <-release; return nil })
				deleteDone <- err
			}()
			// Release on failure before Cleanup closes either handle.
			defer func() {
				select {
				case <-release:
				default:
					close(release)
				}
			}()
			select {
			case p := <-prepared:
				if containsDeleteHost(p.TrafficHosts, host) {
					t.Fatal("shared host selected", p)
				}
			case err := <-deleteDone:
				t.Fatal("delete did not prepare", err)
			case <-time.After(5 * time.Second):
				t.Fatal("prepare timed out")
			}
			pid := prepareTaskPeerObservation(t, peer)
			done := make(chan error, 1)
			go func() {
				var err error
				switch mutation {
				case "asset":
					_, err = peer.Assets().AttachAssetsToTask(first.ID, []int64{asset}, "late")
				case "anchor":
					err = peer.Exploration(first.ExplorationID).Anchor(node, asset)
				case "newAsset":
					_, err = peer.Assets().UpsertRootDomain(UpsertRootDomainReq{Domain: "post-commit.test", TaskID: second.ID})
				}
				done <- err
			}()
			waitTaskPeerObservation(t, d, peer, pid, done)
			close(release)
			if err := waitForTaskDeleteResult(deleteDone); err != nil {
				t.Fatal(err)
			}
			writeErr := waitForTaskDeleteResult(done)
			switch mutation {
			case "asset":
				if !errors.Is(writeErr, ErrTaskAssetTaskNotFound) {
					t.Fatal(writeErr)
				}
			case "anchor":
				if !IsForeignKeyViolation(writeErr) {
					t.Fatal(writeErr)
				}
			case "newAsset":
				if writeErr != nil {
					t.Fatal(writeErr)
				}
			}
			got, err := peer.Assets().GetByIDs([]int64{asset})
			if err != nil || len(got) != 1 || len(got[0].TaskIDs) != 1 || got[0].TaskIDs[0] != second.ID {
				t.Fatal(got, err)
			}
			var anchors int
			if err := peer.QueryRow(`SELECT count(*) FROM exploration_anchors WHERE node_id=$1`, node).Scan(&anchors); err != nil || anchors != 0 {
				t.Fatal(anchors, err)
			}
		})
	}
}

func testTaskDeleteRecordsAndRelations(t *testing.T) {
	d := taskContractDB(t)
	source, err := d.CreateTask("deleting source", "source graph", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	current, err := d.CreateTaskWithOptions("dependent", "no orphan relation", TaskCreateOptions{SourceTaskIDs: []int64{source.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if err := d.EnsureLLMRecordsTable(); err != nil {
		t.Fatal(err)
	}
	if err := d.EnsureLLMUsageTable(); err != nil {
		t.Fatal(err)
	}
	id := fmt.Sprint(source.ID)
	if _, err := d.Exec(`INSERT INTO llm_records(task_id,request_body,response_body) VALUES ($1,'request','response')`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`INSERT INTO llm_usage(task_id,exploration_id,input_tokens,output_tokens) VALUES ($1,$2,7,11)`, id, source.ExplorationID); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exploration(source.ExplorationID).AddIntent(map[string]any{"summary": "delete queue"}, 1, nil, "planner"); err != nil {
		t.Fatal(err)
	}
	result, err := d.DeleteTaskCascade(source.ID, true, true, true)
	if err != nil || result.LLMRecordsDeleted != 1 {
		t.Fatal(result, err)
	}
	for _, check := range []struct {
		query string
		value any
	}{
		{`SELECT count(*) FROM llm_records WHERE task_id=$1`, id},
		{`SELECT count(*) FROM task_relations WHERE source_task_id=$1`, source.ID},
		{`SELECT count(*) FROM exploration_nodes WHERE exploration_id=$1`, source.ExplorationID},
	} {
		var n int
		err := d.QueryRow(check.query, check.value).Scan(&n)
		if err != nil || n != 0 {
			t.Fatal(check.query, n, err)
		}
	}
	var usage int
	if err := d.QueryRow(`SELECT count(*) FROM llm_usage WHERE task_id=$1 AND input_tokens=7 AND output_tokens=11`, id).Scan(&usage); err != nil || usage != 1 {
		t.Fatal(usage, err)
	}
	if got, err := d.GetTask(current.ID); err != nil || got == nil || len(got.SourceTaskIDs) != 0 {
		t.Fatal(got, err)
	}
}

func testTaskBindingScopedWrites(t *testing.T) {
	d := taskContractDB(t)
	task, err := d.CreateTask("scoped bindings", "same transaction", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	intent := mustIntent(t, d.Exploration(task.ExplorationID), "scoped")
	tx, err := d.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var company int64
	if err := tx.QueryRow(`INSERT INTO companies(name,nkey) VALUES ('uncommitted company','uncommitted company') RETURNING id`).Scan(&company); err != nil {
		t.Fatal(err)
	}
	s := &AssetStore{db: d, company: d.Companies(), tx: tx}
	if _, err := s.AddAgentScope(task.ID, "company", fmt.Sprint(company), "same tx", "agent"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddAgentScope(task.ID, "company", "uncommitted company", "same tx", "agent"); err != nil {
		t.Fatal(err)
	}
	m, err := s.RegisterTaskAssetScopes(task.ID, []ScopeInput{{Kind: "domain", Value: "scoped.test"}, {Kind: "ip", Value: "::ffff:192.0.2.9"}})
	if err != nil || m.AssetsLinked != 2 {
		t.Fatal(m, err)
	}
	rows, err := s.QueryByTask(task.ID, "", 10, 0)
	if err != nil || len(rows) != 2 {
		t.Fatal(rows, err)
	}
	if _, err := s.AttachAssetsToTask(task.ID, []int64{rows[0].ID}, "scoped manual"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetTaskAssetSource(task.ID, rows[0].ID, "agent", "scoped node", &intent); err != nil {
		t.Fatal(err)
	}
	listed, err := s.QueryByTask(task.ID, "", 10, 0)
	if err != nil || listed[0].TaskSourceNodeID == nil || *listed[0].TaskSourceNodeID != intent {
		t.Fatal(listed, err)
	}
	scopes, err := s.ListTaskScopeWithSources(task.ID)
	if err != nil || len(scopes) != 3 {
		t.Fatal(scopes, err)
	}
	if _, err := s.TaskCoverageWithSources(task.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.BuildCoverageGraph(task.ID, task.ExplorationID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DetachAssetFromTask(task.ID, rows[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.IntentAssets(task.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.HostsByTaskWithSources(task.ID); err != nil {
		t.Fatal(err)
	}
	if deleted, err := s.DeleteTaskScope(task.ID, scopes[0].ID); err != nil || !deleted {
		t.Fatal(deleted, err)
	}
	if listed, err := s.ListTaskScope(task.ID); err != nil || len(listed) != 2 {
		t.Fatal(listed, err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if rows, err := d.Assets().QueryByTask(task.ID, "", 10, 0); err != nil || len(rows) != 0 {
		t.Fatal(rows, err)
	}
	if scopes, err := d.Assets().ListTaskScope(task.ID); err != nil || len(scopes) != 0 {
		t.Fatal(scopes, err)
	}
}

func testTaskCoverageSemantics(t *testing.T) {
	d := taskContractDB(t)
	task, err := d.CreateTask("coverage distinction", "ip only and anchors", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	s := d.Assets()
	ip, err := s.UpsertIP(UpsertIPReq{IP: "192.0.2.9"})
	if err != nil {
		t.Fatal(err)
	}
	var domainOnly int64
	if err := d.QueryRow(`INSERT INTO assets(type,domain,service_type,port) VALUES ('service','192.0.2.10','other',80) RETURNING id`).Scan(&domainOnly); err != nil {
		t.Fatal(err)
	}
	mapped, err := s.UpsertIP(UpsertIPReq{IP: "::ffff:192.0.2.9"})
	if err != nil {
		t.Fatal(err)
	}
	anchorOnly, err := s.UpsertRootDomain(UpsertRootDomainReq{Domain: "anchor-only.test"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddAgentScope(task.ID, "cidr", "192.0.2.0/24", "v4", "agent"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddAgentScope(task.ID, "ip", "::ffff:192.0.2.9", "v6", "agent"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exploration(task.ExplorationID).AddNode(KindFact, map[string]any{"summary": "tested"}, 0, "confirmed", "worker", []int64{ip, anchorOnly}); err != nil {
		t.Fatal(err)
	}
	scopes, err := s.ListTaskScope(task.ID)
	if err != nil || len(scopes) != 2 || scopes[1].Net != "::ffff:192.0.2.9/128" {
		t.Fatal(scopes, err)
	}
	declared, err := s.GetByIDsInScope(task.ID, []int64{ip, domainOnly, mapped, anchorOnly})
	if err != nil || len(declared) != 3 {
		t.Fatal(declared, err)
	}
	coverage, err := s.TaskCoverage(task.ID, task.ExplorationID)
	if err != nil || coverage.Denominator != 2 || coverage.Tested != 1 {
		t.Fatal(coverage, err)
	}
	contextCoverage, err := s.TaskCoverageWithSources(task.ID)
	if err != nil || contextCoverage.Denominator != 3 || contextCoverage.Tested != 2 {
		t.Fatal(contextCoverage, err)
	}
	backlog, total, err := s.ListUntestedAssets(task.ID, task.ExplorationID, "", 1, 0)
	if err != nil || total != 1 || len(backlog) != 1 || backlog[0].ID != mapped {
		t.Fatal(backlog, total, err)
	}
	contextBacklog, total, err := s.ListUntestedAssetsWithSources(task.ID, "ip", 1, 0)
	if err != nil || total != 1 || len(contextBacklog) != 1 || contextBacklog[0].ID != mapped {
		t.Fatal(contextBacklog, total, err)
	}
	graph, err := s.BuildCoverageGraph(task.ID, task.ExplorationID)
	if err != nil {
		t.Fatal(err)
	}
	for _, node := range graph.Nodes {
		if node.AssetID == domainOnly {
			t.Fatal("IP-literal domain leaked into context coverage", node)
		}
	}
	if _, err := s.AddAgentScope(task.ID, "keyword", "192.0.2.10", "not ownership", "agent"); err != nil {
		t.Fatal(err)
	}
}

func testTaskDeleteRetainsAssets(t *testing.T) {
	d := taskContractDB(t)
	task, err := d.CreateTask("retained global assets", "delete memberships", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	asset, err := d.Assets().UpsertRootDomain(UpsertRootDomainReq{Domain: "retained.test", TaskID: task.ID})
	if err != nil {
		t.Fatal(err)
	}
	var finding int64
	if err := d.QueryRow(`INSERT INTO findings(task_id,name,severity) VALUES ($1,'retained finding','high') RETURNING id`, task.ID).Scan(&finding); err != nil {
		t.Fatal(err)
	}
	abort := errors.New("staging aborted")
	if _, err := d.DeleteTaskCascadePrepared(task.ID, false, false, false, func(p TaskDeletePreparation) error { return abort }); !errors.Is(err, abort) {
		t.Fatal(err)
	}
	if got, err := d.GetTask(task.ID); err != nil || got == nil {
		t.Fatal(got, err)
	}
	result, err := d.DeleteTaskCascade(task.ID, false, false, false)
	if err != nil || result.AssetsDetached != 1 || result.AssetsDeleted != 0 || result.FindingsDeleted != 0 {
		t.Fatal(result, err)
	}
	got, err := d.Assets().GetByIDs([]int64{asset})
	if err != nil || len(got) != 1 || len(got[0].TaskIDs) != 0 {
		t.Fatal("deleted task membership remains", got, err)
	}
	var retained int
	if err := d.QueryRow(`SELECT count(*) FROM findings WHERE id=$1 AND task_id IS NULL`, finding).Scan(&retained); err != nil || retained != 1 {
		t.Fatal(retained, err)
	}
}
