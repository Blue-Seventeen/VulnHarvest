package db

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Reset only the explicitly supplied, disposable Task3 database. Never consult
// application configuration or infer a database from the user's installation.
func taskContractDB(t *testing.T) *DB {
	t.Helper()
	if strings.Contains(t.Name(), "/SQLite/") {
		d, err := OpenSQLite(filepath.Join(t.TempDir(), "tasks.db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { d.Close() })
		return d
	}
	dsn := os.Getenv("ARTEX_TASK_TEST_PG_DSN")
	if dsn == "" {
		t.Fatal("ARTEX_TASK_TEST_PG_DSN required")
	}
	pool, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var name string
	if err = pool.QueryRow("SELECT current_database()").Scan(&name); err != nil {
		t.Fatal(err)
	}
	if name != "vh_task3" {
		t.Fatalf("refusing reset of %q", name)
	}
	if _, err = pool.Exec("DROP SCHEMA public CASCADE; CREATE SCHEMA public"); err != nil {
		t.Fatal(err)
	}
	d, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func openTaskTestDB(t *testing.T) (*DB, error) {
	if strings.HasPrefix(t.Name(), "TestPortableTaskContract/") {
		return taskContractDB(t), nil
	}
	return Open(testDSN(t))
}

func TestPortableTaskContract(t *testing.T) {
	for _, engine := range []string{"SQLite", "PostgreSQL"} {
		t.Run(engine, func(t *testing.T) {
			t.Run("PeerCompanySnapshot", testTaskPeerCompanySnapshot)
			t.Run("PeerStaleRevision", testTaskPeerStaleRevision)
			t.Run("CategoryErrors", testTaskCategoryErrors)
			t.Run("FirstRunAndStatus", testTaskFirstRunAndStatus)
			t.Run("NullableTemplateAndTimestamps", testTaskNullableTemplateAndTimestamps)
			t.Run("Intercept", testTaskInterceptContract)
			t.Run("ScopedIntercept", testTaskScopedIntercept)
			t.Run("RestartAndDirectSources", testTaskRestartAndDirectSources)
			t.Run("AtomicAssociations", testTaskAtomicAssociations)
			t.Run("BulkLargeIDs", testTaskBulkLargeIDs)
			t.Run("CategoryCRUD", TestTaskCategoryCRUDAndTaskAssignment)
			t.Run("MissingCategoryRollback", TestCreateTaskRejectsMissingCategoryAtomically)
			t.Run("CategoryBatch", TestSetTasksCategoryBatch)
			t.Run("CategoryBatchReject", TestSetTasksCategoryRejectsBadInputAtomically)
			t.Run("TemplateCRUD", TestTaskTemplateCRUDAndNormalizedUniqueness)
			t.Run("TemplateConcurrentPatch", TestTaskTemplateDisjointPatchesCompose)
			t.Run("Metadata", TestTaskPinOrderingAndRename)
			t.Run("Queue", TestTaskQueuePreservesBootstrapAndFIFOPosition)
			t.Run("LLMChain", TestTaskRelationsAndLLMFailoverChain)
			t.Run("TerminalAndDuplicateChain", TestTaskContextRejectsDuplicatesAndAllowsTerminalLLMEdits)
			t.Run("CompanySnapshot", TestCreateTaskWithCompanyScopes)
			t.Run("BulkContext", TestListTasksBulkHydratesTaskContext)
			t.Run("TaskOrder", TestListTasksOrderByIDDesc)
		})
	}
}

func testTaskScopedIntercept(t *testing.T) {
	d := taskContractDB(t)
	task, err := d.CreateTask("scoped intercept", "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	original, err := d.Assets().CreateTaskInterceptRule(task.ID, "block", "exact_domain", "original.test", "original", true)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := d.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`UPDATE task_intercept_rules SET note='uncommitted' WHERE id=$1`, original.ID); err != nil {
		t.Fatal(err)
	}
	scoped := &AssetStore{db: d, company: d.Companies(), tx: tx}
	listed, err := scoped.ListTaskInterceptRules(task.ID)
	if err != nil || len(listed) != 1 || listed[0].Note != "uncommitted" {
		t.Fatalf("scoped read escaped tx: %+v / %v", listed, err)
	}
	updated, err := scoped.UpdateTaskInterceptRule(task.ID, original.ID, "allow", "exact_domain", "updated.test", "updated", false)
	if err != nil {
		t.Fatal(err)
	}
	var finalTime time.Time
	if err = tx.QueryRow(`SELECT updated_at FROM task_intercept_rules WHERE id=$1`, original.ID).Scan(&finalTime); err != nil || !finalTime.Equal(updated.UpdatedAt) {
		t.Fatalf("scoped updated stamp: %v / %v / %v", finalTime, updated.UpdatedAt, err)
	}
	created, err := scoped.CreateTaskInterceptRule(task.ID, "block", "exact_ip", "10.0.0.1", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if err = scoped.ToggleTaskInterceptRule(task.ID, created.ID, true); err != nil {
		t.Fatal(err)
	}
	deleted, err := scoped.DeleteTaskInterceptRule(task.ID, created.ID)
	if err != nil || !deleted {
		t.Fatalf("scoped delete: %v / %v", deleted, err)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	listed, err = d.Assets().ListTaskInterceptRules(task.ID)
	if err != nil || len(listed) != 1 || listed[0].Note != "original" || listed[0].Action != "block" || !listed[0].Enabled {
		t.Fatalf("scoped writes escaped rollback: %+v / %v", listed, err)
	}
}

func testTaskFirstRunAndStatus(t *testing.T) {
	d := taskContractDB(t)
	disabled := false
	task, err := d.CreateTaskWithOptions("first run", "goal", TaskCreateOptions{TimeoutSeconds: 60, PlanHeartbeatSeconds: 1, CoverageEnabled: &disabled})
	if err != nil {
		t.Fatal(err)
	}
	before := time.Now().UTC()
	deadline, err := d.StampFirstRun(task.ID, 60)
	if err != nil {
		t.Fatal(err)
	}
	got, err := d.GetTask(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if deadline == nil || got.FirstRunAt == nil || deadline.Sub(*got.FirstRunAt) != time.Minute || got.FirstRunAt.Before(before.Add(-time.Second)) {
		t.Fatalf("first-run deadline: %+v / %v", got, deadline)
	}
	again, err := d.StampFirstRun(task.ID, 120)
	if err != nil || !again.Equal(*deadline) {
		t.Fatalf("stamp reset: %v / %v", again, err)
	}
	if got.PlanHeartbeatSeconds != MinPlanHeartbeatSeconds {
		t.Fatalf("heartbeat=%d", got.PlanHeartbeatSeconds)
	}
	if got.CoverageEnabled {
		t.Fatal("explicit false coverage flag lost")
	}
	if err = d.SetParentRef(task.ID, "123"); err != nil {
		t.Fatal(err)
	}
	if err = d.SetPaused(task.ID, true); err != nil {
		t.Fatal(err)
	}
	won, err := d.SetTerminalStatusGuarded(task.ID, "timeout")
	if err != nil || !won {
		t.Fatalf("first terminal: %v / %v", won, err)
	}
	won, err = d.SetTerminalStatusGuarded(task.ID, "done")
	if err != nil || won {
		t.Fatalf("duplicate terminal: %v / %v", won, err)
	}
	got, err = d.GetTask(task.ID)
	if err != nil || got.CompletedAt == nil || !got.Paused || got.ParentRef != "123" {
		t.Fatalf("metadata: %+v / %v", got, err)
	}
	if err = d.SetStatus(task.ID, "running"); err != nil {
		t.Fatal(err)
	}
	got, err = d.GetTask(task.ID)
	if err != nil || got.CompletedAt != nil {
		t.Fatalf("terminal clear: %+v / %v", got, err)
	}
	unlimited, err := d.CreateTask("unlimited", "goal", nil, -1, 0)
	if err != nil {
		t.Fatal(err)
	}
	none, err := d.StampFirstRun(unlimited.ID, 0)
	if err != nil || none != nil {
		t.Fatalf("unlimited: %v / %v", none, err)
	}
}

func testTaskNullableTemplateAndTimestamps(t *testing.T) {
	d := taskContractDB(t)
	cat, err := d.CreateTaskCategory("templates")
	if err != nil {
		t.Fatal(err)
	}
	rules := []TaskInterceptRuleInput{{Enabled: false, Action: "allow", Kind: "exact_domain", Pattern: "example.test"}}
	preset, err := d.CreateTaskTemplate(TaskTemplateInput{Name: "preset", Description: "description", Goal: "goal", CategoryID: &cat.ID, InterceptRules: rules})
	if err != nil {
		t.Fatal(err)
	}
	past := time.Date(2001, 2, 3, 4, 5, 6, 0, time.FixedZone("UTC+8", 8*3600))
	if _, err = d.Exec(`UPDATE task_templates SET updated_at=$2 WHERE id=$1`, preset.ID, past); err != nil {
		t.Fatal(err)
	}
	goal := "new goal"
	patched, err := d.PatchTaskTemplate(preset.ID, TaskTemplatePatch{Goal: &goal})
	if err != nil {
		t.Fatal(err)
	}
	if patched.CategoryID == nil || *patched.CategoryID != cat.ID || len(patched.InterceptRules) != 1 || patched.InterceptRules[0].Enabled {
		t.Fatalf("absent fields lost: %+v", patched)
	}
	persisted, err := d.GetTaskTemplate(preset.ID)
	if err != nil || !persisted.UpdatedAt.Equal(patched.UpdatedAt) || !patched.UpdatedAt.After(past) {
		t.Fatalf("returned timestamp stale: %+v / %+v / %v", patched, persisted, err)
	}
	patched, err = d.PatchTaskTemplate(preset.ID, TaskTemplatePatch{SetCategoryID: true, SetInterceptRules: true})
	if err != nil || patched.CategoryID != nil || patched.InterceptRules == nil || len(patched.InterceptRules) != 0 {
		t.Fatalf("explicit clear: %+v / %v", patched, err)
	}
	other, err := d.CreateTaskTemplate(TaskTemplateInput{Name: "other", Description: "d", Goal: "g"})
	if err != nil {
		t.Fatal(err)
	}
	name := preset.Name
	if _, err = d.PatchTaskTemplate(other.ID, TaskTemplatePatch{Name: &name}); !errors.Is(err, ErrTaskTemplateNameConflict) {
		t.Fatalf("rename conflict: %v", err)
	}
	if _, err = d.Exec(`UPDATE task_categories SET updated_at=$2 WHERE id=$1`, cat.ID, past); err != nil {
		t.Fatal(err)
	}
	renamed, err := d.RenameTaskCategory(cat.ID, "renamed")
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := d.GetTaskCategory(cat.ID)
	if err != nil || !loaded.UpdatedAt.Equal(renamed.UpdatedAt) || !renamed.UpdatedAt.After(past) {
		t.Fatalf("category timestamp: %+v / %+v / %v", renamed, loaded, err)
	}
}

func testTaskInterceptContract(t *testing.T) {
	d := taskContractDB(t)
	rules := []TaskInterceptRuleInput{{Enabled: false, Action: "allow", Kind: "exact_domain", Pattern: "allow.test"}, {Enabled: true, Kind: "exact_ip", Pattern: "10.0.0.1"}}
	task, err := d.CreateTaskWithOptions("intercept", "goal", TaskCreateOptions{InterceptRules: rules})
	if err != nil {
		t.Fatal(err)
	}
	other, err := d.CreateTask("other", "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	s := d.Assets()
	block, allow, err := s.TaskInterceptRulesSplit(task.ID)
	if err != nil || len(block) != 1 || len(allow) != 1 || allow[0].Enabled || block[0].Action != "block" {
		t.Fatalf("split: %v / %v / %v", block, allow, err)
	}
	if _, err = s.UpdateTaskInterceptRule(other.ID, allow[0].ID, "block", "exact_domain", "bad", "", true); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("cross-task edit: %v", err)
	}
	past := time.Date(2001, 2, 3, 4, 5, 6, 0, time.UTC)
	if _, err = d.Exec(`UPDATE task_intercept_rules SET updated_at=$2 WHERE id=$1`, allow[0].ID, past); err != nil {
		t.Fatal(err)
	}
	updated, err := s.UpdateTaskInterceptRule(task.ID, allow[0].ID, "block", "exact_domain", "updated", "note", true)
	if err != nil {
		t.Fatal(err)
	}
	listed, err := s.ListTaskInterceptRules(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range listed {
		if r.ID == updated.ID && (!r.UpdatedAt.Equal(updated.UpdatedAt) || !updated.UpdatedAt.After(past)) {
			t.Fatalf("intercept timestamp: %+v / %+v", updated, r)
		}
	}
	if err = s.ToggleTaskInterceptRule(task.ID, updated.ID, false); err != nil {
		t.Fatal(err)
	}
	deleted, err := s.DeleteTaskInterceptRule(other.ID, updated.ID)
	if err != nil || deleted {
		t.Fatalf("cross-task delete: %v / %v", deleted, err)
	}
	deleted, err = s.DeleteTaskInterceptRule(task.ID, updated.ID)
	if err != nil || !deleted {
		t.Fatalf("delete: %v / %v", deleted, err)
	}
	created, err := s.CreateTaskInterceptRule(task.ID, "invalid", "exact_domain", "new", "", false)
	if err != nil || created.Action != "block" || created.Enabled {
		t.Fatalf("create: %+v / %v", created, err)
	}
}

func reopenTaskDB(t *testing.T, d *DB) *DB {
	t.Helper()
	var next *DB
	var err error
	if d.IsSQLite() {
		var seq int
		var name, path string
		if err = d.DB.QueryRow(`PRAGMA database_list`).Scan(&seq, &name, &path); err != nil {
			t.Fatal(err)
		}
		if err = d.Close(); err != nil {
			t.Fatal(err)
		}
		next, err = OpenSQLite(path)
	} else {
		if err = d.Close(); err != nil {
			t.Fatal(err)
		}
		next, err = Open(os.Getenv("ARTEX_TASK_TEST_PG_DSN"))
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { next.Close() })
	return next
}

func testTaskRestartAndDirectSources(t *testing.T) {
	d := taskContractDB(t)
	root, err := d.CreateTask("root", "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	source, err := d.CreateTaskWithOptions("source", "goal", TaskCreateOptions{SourceTaskIDs: []int64{root.ID}})
	if err != nil {
		t.Fatal(err)
	}
	profile, err := d.SaveProfile(&LLMProfile{Name: "restart", Format: "openai", Model: "test", APIKey: "test-fixture"})
	if err != nil {
		t.Fatal(err)
	}
	task, err := d.CreateTaskWithOptions("child", "goal", TaskCreateOptions{SourceTaskIDs: []int64{source.ID}, LLMProfileIDs: []int64{profile}})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := d.GetTask(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = d.MarkTaskLLMProfileQuotaExhaustedAtRevision(task.ID, profile, snapshot.LLMChainRevision, "quota"); err != nil {
		t.Fatal(err)
	}
	local := time.Date(2026, 3, 1, 8, 0, 0, 123000000, time.FixedZone("UTC+8", 8*3600))
	if _, err = d.Exec(`UPDATE tasks SET created_at=$2 WHERE id=$1`, task.ID, local); err != nil {
		t.Fatal(err)
	}
	if err = d.SetPaused(task.ID, true); err != nil {
		t.Fatal(err)
	}
	if err = d.Enqueue(task.ID, "resume"); err != nil {
		t.Fatal(err)
	}
	d = reopenTaskDB(t, d)
	got, err := d.GetTask(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.SourceTaskIDs) != 1 || got.SourceTaskIDs[0] != source.ID || !got.Paused || !got.Queued || got.QueueMode != "resume" || got.ActiveLLMProfileID != nil || got.LLMFailoverState != "chain_exhausted" {
		t.Fatalf("restart changed context: %+v", got)
	}
	sources, err := d.TaskSources(task.ID)
	if err != nil || len(sources) != 1 || sources[0].TaskID != source.ID {
		t.Fatalf("direct sources: %v / %v", sources, err)
	}
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	var api struct {
		CreatedAt string `json:"created_at"`
	}
	if err = json.Unmarshal(raw, &api); err != nil {
		t.Fatal(err)
	}
	decoded, err := time.Parse(time.RFC3339Nano, api.CreatedAt)
	if err != nil || !decoded.Equal(local) {
		t.Fatalf("nonUTC API instant: %q / %v", api.CreatedAt, err)
	}
}

func testTaskAtomicAssociations(t *testing.T) {
	d := taskContractDB(t)
	profile, err := d.SaveProfile(&LLMProfile{Name: "atomic", Format: "openai", Model: "test", APIKey: "test-fixture"})
	if err != nil {
		t.Fatal(err)
	}
	company, _, err := d.Companies().UpsertCompany("atomic company", "")
	if err != nil {
		t.Fatal(err)
	}
	asset, err := d.Assets().UpsertRootDomain(UpsertRootDomainReq{Domain: "atomic.test"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = d.Exec(`UPDATE assets SET company_id=$2,company_source='explicit' WHERE id=$1`, asset, company); err != nil {
		t.Fatal(err)
	}
	source, err := d.CreateTask("source", "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	for i, opts := range []TaskCreateOptions{
		{CompanyIDs: []int64{company}, LLMProfileIDs: []int64{profile, profile}},
		{CompanyIDs: []int64{company}, LLMProfileIDs: []int64{1 << 62}},
		{CompanyIDs: []int64{company}, SourceTaskIDs: []int64{source.ID, source.ID}},
		{CompanyIDs: []int64{company}, SourceTaskIDs: []int64{source.ID, 1 << 62}},
		{CompanyIDs: []int64{company, 1 << 62}},
		{CompanyIDs: []int64{company}, LLMProfileIDs: []int64{profile}, SourceTaskIDs: []int64{source.ID}, InterceptRules: []TaskInterceptRuleInput{{Kind: "invalid", Pattern: "rollback"}}},
	} {
		description := fmt.Sprintf("rejected-%d", i)
		if _, err = d.CreateTaskWithOptions(description, "goal", opts); err == nil {
			t.Fatalf("invalid associations %d accepted", i)
		}
		var leaked int
		if err = d.QueryRow(`SELECT count(*) FROM explorations WHERE description=$1`, description).Scan(&leaked); err != nil || leaked != 0 {
			t.Fatalf("leaked: %d / %v", leaked, err)
		}
		assets, err := d.Assets().GetByIDs([]int64{asset})
		if err != nil {
			t.Fatal(err)
		}
		if len(assets) != 1 || len(assets[0].TaskIDs) != 0 {
			t.Fatalf("association survived rollback: %+v", assets)
		}
		if err = d.QueryRow(`SELECT count(*) FROM task_asset_links WHERE asset_id=$1`, asset).Scan(&leaked); err != nil || leaked != 0 {
			t.Fatalf("link leaked: %d / %v", leaked, err)
		}
	}
	sources := make([]int64, 0, 8)
	for i := 0; i < 8; i++ {
		s, err := d.CreateTask(fmt.Sprintf("source-%d", i), "goal", nil, 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		sources = append(sources, s.ID)
	}
	child, err := d.CreateTaskWithOptions("eight", "goal", TaskCreateOptions{SourceTaskIDs: sources})
	if err != nil {
		t.Fatal(err)
	}
	got, err := d.GetTask(child.ID)
	if err != nil || len(got.SourceTaskIDs) != 8 {
		t.Fatalf("eight direct sources: %+v / %v", got, err)
	}
	if _, err = d.CreateTaskWithOptions("nine", "goal", TaskCreateOptions{SourceTaskIDs: append(sources, source.ID)}); err == nil {
		t.Fatal("nine sources accepted")
	}
}

func taskPeerDB(t *testing.T, d *DB) *DB {
	t.Helper()
	var peer *DB
	var err error
	if d.IsSQLite() {
		var seq int
		var name, path string
		if err = d.DB.QueryRow(`PRAGMA database_list`).Scan(&seq, &name, &path); err != nil {
			t.Fatal(err)
		}
		peer, err = OpenSQLite(path)
	} else {
		peer, err = Open(os.Getenv("ARTEX_TASK_TEST_PG_DSN"))
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { peer.Close() })
	return peer
}

func testTaskPeerCompanySnapshot(t *testing.T) {
	d := taskContractDB(t)
	a, _, err := d.Companies().UpsertCompany("company A", "")
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := d.Companies().UpsertCompany("company B", "")
	if err != nil {
		t.Fatal(err)
	}
	asset, err := d.Assets().UpsertRootDomain(UpsertRootDomainReq{Domain: "snapshot.test"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = d.Exec(`UPDATE assets SET company_id=$2,company_source='explicit' WHERE id=$1`, asset, a); err != nil {
		t.Fatal(err)
	}
	peer := taskPeerDB(t, d)
	tx, err := d.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err = lockCompanyScopeMutation(tx, d); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(`UPDATE assets SET company_id=$2 WHERE id=$1`, asset, b); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	done := make(chan error, 1)
	var created *Task
	go func() {
		close(started)
		var createErr error
		created, createErr = peer.CreateTaskWithOptions("peer snapshot", "goal", TaskCreateOptions{CompanyIDs: []int64{b, a}})
		done <- createErr
	}()
	<-started
	select {
	case err := <-done:
		t.Fatalf("snapshot crossed mutation barrier: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("peer snapshot did not complete")
	}
	got, err := peer.GetTask(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.CompanyIDs) != 2 || got.CompanyIDs[0] != b || got.CompanyIDs[1] != a {
		t.Fatalf("company order: %+v", got)
	}
	assets, err := peer.Assets().QueryByTask(created.ID, "", 10, 0)
	if err != nil || len(assets) != 1 || assets[0].ID != asset || assets[0].TaskSourceSummary != "任务创建时关联企业：company B" {
		t.Fatalf("snapshot missed committed ownership: %+v / %v", assets, err)
	}
}

func testTaskPeerStaleRevision(t *testing.T) {
	d := taskContractDB(t)
	profile, err := d.SaveProfile(&LLMProfile{Name: "peer revision", Format: "openai", Model: "test", APIKey: "test-fixture"})
	if err != nil {
		t.Fatal(err)
	}
	task, err := d.CreateTaskWithOptions("peer revision", "goal", TaskCreateOptions{LLMProfileIDs: []int64{profile}})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := d.GetTask(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	peer := taskPeerDB(t, d)
	tx, err := d.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`UPDATE tasks SET llm_chain_revision=llm_chain_revision+1 WHERE id=$1`, task.ID); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	done := make(chan error, 1)
	var transition TaskLLMTransition
	go func() {
		close(started)
		var quotaErr error
		transition, quotaErr = peer.MarkTaskLLMProfileQuotaExhaustedAtRevision(task.ID, profile, snapshot.LLMChainRevision, "late quota")
		done <- quotaErr
	}()
	<-started
	select {
	case err := <-done:
		t.Fatalf("quota crossed task mutation barrier: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("quota did not complete")
	}
	if !transition.Stale || transition.Advanced || transition.ChainExhausted {
		t.Fatalf("stale mutation result: %+v", transition)
	}
	got, err := d.GetTask(task.ID)
	if err != nil || got.LLMFailoverState != "ready" || got.ActiveLLMProfileID == nil || *got.ActiveLLMProfileID != profile {
		t.Fatalf("stale changed cursor: %+v / %v", got, err)
	}
}

func testTaskCategoryErrors(t *testing.T) {
	d := taskContractDB(t)
	cat, err := d.CreateTaskCategory("first")
	if err != nil {
		t.Fatal(err)
	}
	second, err := d.CreateTaskCategory("second")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = d.RenameTaskCategory(second.ID, cat.Name); !errors.Is(err, ErrTaskCategoryNameConflict) {
		t.Fatalf("rename conflict: %v", err)
	}
	missing := int64(1 << 62)
	if _, err = d.RenameTaskCategory(missing, "missing"); !errors.Is(err, ErrTaskCategoryNotFound) {
		t.Fatalf("rename missing: %v", err)
	}
	if _, err = d.SetTaskCategory(missing, &cat.ID); !errors.Is(err, ErrTaskCategoryTaskNotFound) {
		t.Fatalf("missing task: %v", err)
	}
	task, err := d.CreateTask("assign", "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = d.SetTaskCategory(task.ID, &missing); !errors.Is(err, ErrTaskCategoryNotFound) {
		t.Fatalf("missing category: %v", err)
	}
	if _, err = d.SetTaskCategory(missing, nil); !errors.Is(err, ErrTaskCategoryTaskNotFound) {
		t.Fatalf("missing clear: %v", err)
	}
	categories, err := d.ListTaskCategories()
	if err != nil || len(categories) != 2 {
		t.Fatalf("category list: %+v / %v", categories, err)
	}
	absent, err := d.GetTaskCategory(missing)
	if err != nil || absent != nil {
		t.Fatalf("missing category read: %+v / %v", absent, err)
	}
}

func testTaskBulkLargeIDs(t *testing.T) {
	d := taskContractDB(t)
	ids := []int64{9007199254740993, 9007199254740995}
	for i, id := range ids {
		exp, err := d.CreateExploration("large", "goal")
		if err != nil {
			t.Fatal(err)
		}
		if _, err = d.Exec(`INSERT INTO tasks(id,description,goal,exploration_id) VALUES ($1,$2,'goal',$3)`, id, fmt.Sprintf("large-%d", i), exp); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := d.Exec(`INSERT INTO task_relations(task_id,source_task_id) VALUES($1,$2)`, ids[1], ids[0]); err != nil {
		t.Fatal(err)
	}
	tasks, err := d.ListTasks()
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range tasks {
		if task.ID == ids[1] {
			if len(task.SourceTaskIDs) != 1 || task.SourceTaskIDs[0] != ids[0] {
				t.Fatalf("large ID hydration changed identity: %+v", task)
			}
			return
		}
	}
	t.Fatal("large task missing")
}
