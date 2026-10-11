package db

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestPortableErrorClassification(t *testing.T) {
	if !IsBusyError(fmt.Errorf("wrapped: %w", &pgconn.PgError{Code: "55P03"})) {
		t.Fatal("PG lock_not_available not classified")
	}
	if !IsCheckViolation(fmt.Errorf("wrapped: %w", &pgconn.PgError{Code: "23514"})) {
		t.Fatal("PG check violation not classified")
	}
	d := portableDB(t)
	_, err := d.Exec(`INSERT INTO agents(key,name,role) VALUES('Invalid!','fixture','assistant')`)
	if err == nil || !IsCheckViolation(err) || IsBusyError(err) {
		t.Fatalf("SQLite CHECK classifier: %v", err)
	}
	if IsCheckViolation(errors.New("CHECK text decoy")) || IsBusyError(errors.New("database is locked decoy")) {
		t.Fatal("classification must use codes")
	}
}

func TestPortableProfileTaskChain(t *testing.T) {
	task2Stores(t, func(t *testing.T, d *DB) {
		var exp, task int64
		if err := d.QueryRow(`INSERT INTO explorations(goal) VALUES('task2-chain') RETURNING id`).Scan(&exp); err != nil {
			t.Fatal(err)
		}
		if err := d.QueryRow(`INSERT INTO tasks(description,goal,exploration_id) VALUES('task2-chain','goal',$1) RETURNING id`, exp).Scan(&task); err != nil {
			t.Fatal(err)
		}
		ids := make([]int64, 3)
		for i := range ids {
			var err error
			ids[i], err = d.SaveProfile(&LLMProfile{Name: fmt.Sprintf("chain-%d", i), Format: "openai", Model: "fixture", APIKey: "fixture", Streaming: true})
			if err != nil {
				t.Fatal(err)
			}
		}
		if err := d.ReplaceTaskLLMProfiles(task, ids, ids[1]); err != nil {
			t.Fatal(err)
		}
		if err := d.DeleteProfile(ids[1]); err != nil {
			t.Fatal(err)
		}
		var active sql.NullInt64
		if err := d.QueryRow(`SELECT active_llm_profile_id FROM tasks WHERE id=$1`, task).Scan(&active); err != nil || !active.Valid || active.Int64 != ids[2] {
			t.Fatalf("cursor successor: %v %v", active, err)
		}
		if err := d.DeleteProfile(ids[2]); err != nil {
			t.Fatal(err)
		}
		chain, err := d.TaskLLMProfiles(task)
		if err != nil || len(chain) != 0 {
			t.Fatalf("prior profile must not revive: %v %v", chain, err)
		}
	})
}

func TestPortableUnicodeCommands(t *testing.T) {
	task2Stores(t, func(t *testing.T, d *DB) {
		var exp int64
		if err := d.QueryRow(`INSERT INTO explorations(goal) VALUES('commands') RETURNING id`).Scan(&exp); err != nil {
			t.Fatal(err)
		}
		if _, err := d.Exec(`INSERT INTO activity(exploration_id,kind,tool,tool_use_id,detail) VALUES($1,'tool_use','Écho','fixture-call','escaped fixture')`, exp); err != nil {
			t.Fatal(err)
		}
		if _, err := d.Exec(`INSERT INTO activity(exploration_id,kind,tool,tool_use_id,detail,is_error) VALUES($1,'tool_result','Écho','fixture-call','output',true)`, exp); err != nil {
			t.Fatal(err)
		}
		list, total, err := d.ListCommands(&exp, "écho", 0, 10)
		if err != nil || total != 1 || len(list) != 1 || list[0].Output != "output" {
			t.Fatalf("Unicode list: %v %v", list, err)
		}
		stats, err := d.ToolStats(&exp, "écho")
		if err != nil || len(stats) != 1 || stats[0].Errors != 1 {
			t.Fatalf("Unicode counts: %v %v", stats, err)
		}
		if err = d.EnsureLLMRecordsTable(); err != nil {
			t.Fatal(err)
		}
		if err = d.InsertLLMRecord(&LLMRecord{SessionID: "Écho", Model: "fixture"}); err != nil {
			t.Fatal(err)
		}
		_, total, err = d.ListLLMRecords("", "écho", "", 0, 10)
		if err != nil || total != 1 {
			t.Fatalf("Unicode session: %d %v", total, err)
		}
	})
}

func TestPortablePreparedTimeAndScanners(t *testing.T) {
	d := portableDB(t)
	base := time.Date(2026, 1, 1, 0, 0, 0, 123456789, time.FixedZone("CST", 8*3600))
	tx, err := d.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare(`INSERT INTO server_logs(level,tag,text,created_at) VALUES('info','time','fixture',$1)`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = stmt.Exec(base); err != nil {
		t.Fatal(err)
	}
	stmt.Close()
	var got time.Time
	if err = tx.QueryRow(`SELECT created_at FROM server_logs WHERE tag='time'`).Scan(&got); err != nil || !got.Equal(base) {
		t.Fatalf("native prepared time: %v %v", got, err)
	}
	var n int
	if err = tx.QueryRow(`SELECT count(*) FROM server_logs WHERE created_at >= $1`, base.Add(time.Nanosecond)).Scan(&n); err != nil || n != 0 {
		t.Fatalf("nanosecond since boundary: %d %v", n, err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var shifted string
	if err = d.QueryRow(`SELECT portable_time_shift($1,$2)`, base, 0.000000001).Scan(&shifted); err != nil || shifted != base.Add(time.Nanosecond).UTC().Format(sqliteTimeLayout) {
		t.Fatalf("fractional arithmetic: %s %v", shifted, err)
	}
	var raw json.RawMessage
	for _, value := range []any{`{"x":null}`, []byte(`{"x":null}`), nil} {
		if err = (JSONScanner{&raw}).Scan(value); err != nil {
			t.Fatal(err)
		}
	}
	if raw != nil {
		t.Fatal("nullable JSON scanner")
	}
	bytes := []byte(`{"x":1}`)
	if err = (JSONScanner{&raw}).Scan(bytes); err != nil {
		t.Fatal(err)
	}
	bytes[2] = 'y'
	if !reflect.DeepEqual(raw, json.RawMessage(`{"x":1}`)) {
		t.Fatal("scanner retained driver slice")
	}
}

func TestPortableCustomToolEmptyBinding(t *testing.T) {
	task2Stores(t, func(t *testing.T, d *DB) {
		tool := &Tool{Key: "empty_binding", Kind: "script", Exec: json.RawMessage(`{"language":"python","code":"fixture text only","timeout":3}`), Enabled: true}
		if err := d.CreateCustomTool(tool); err != nil {
			t.Fatal(err)
		}
		if err := d.AddAgentToToolBinding("worker", []string{tool.Key}); err != nil {
			t.Fatal(err)
		}
		got, err := d.GetTool(tool.Key)
		if err != nil || !reflect.DeepEqual(got.Agents, []string{"worker"}) {
			t.Fatalf("nil must be empty array: %+v %v", got, err)
		}
		tool.Description = "updated"
		tool.Exec = json.RawMessage(`{"code":"preserved updated fixture","args":["x"],"env":{"TEST":"value"}}`)
		if err = d.UpdateCustomTool(tool); err != nil {
			t.Fatal(err)
		}
		if err = d.AddAgentToToolBinding("auto", []string{tool.Key}); err != nil {
			t.Fatal(err)
		}
		got, err = d.GetTool(tool.Key)
		if err != nil || !equalJSON(got.Exec, tool.Exec) || !reflect.DeepEqual(got.Agents, []string{"auto"}) {
			t.Fatalf("spec/binding lost: %+v %v", got, err)
		}
		if err = d.DeleteCustomTool(tool.Key); err != nil {
			t.Fatal(err)
		}
	})
}

func TestPortableColdStatistics(t *testing.T) {
	task2Stores(t, func(t *testing.T, d *DB) {
		if err := d.EnsureLLMUsageTable(); err != nil {
			t.Fatal(err)
		}
		var exp, task int64
		if err := d.QueryRow(`INSERT INTO explorations(goal) VALUES('cold') RETURNING id`).Scan(&exp); err != nil {
			t.Fatal(err)
		}
		if err := d.QueryRow(`INSERT INTO tasks(description,goal,exploration_id,archived_at) VALUES('cold','goal',$1,now()) RETURNING id`, exp).Scan(&task); err != nil {
			t.Fatal(err)
		}
		aggregate := `{"token_profiles":[{"profile_name":"cold","calls":2,"tasks":1,"input_tokens":42,"output_tokens":4,"cache_read_tokens":3,"cache_write_tokens":2}],"skills":{"cold-skill":5},"tools":{"cold-tool":7}}`
		if _, err := d.Exec(`INSERT INTO task_archives(task_id,aggregate_stats,state) VALUES($1,$2,'ready')`, task, aggregate); err != nil {
			t.Fatal(err)
		}
		profiles, err := d.UsageByProfile()
		if err != nil || len(profiles) != 1 || profiles[0].InputTokens != 42 || profiles[0].CacheWriteTokens != 2 {
			t.Fatalf("cold profile stats: %v %v", profiles, err)
		}
		skills, err := d.SkillStats()
		if err != nil || len(skills) != 1 || skills[0].Calls != 5 {
			t.Fatalf("cold skills: %v %v", skills, err)
		}
		tools, err := d.ToolUsageCounts()
		if err != nil || tools["cold-tool"] != 7 {
			t.Fatalf("cold tools: %v %v", tools, err)
		}
	})
}

func TestPortableJSONStorageIsText(t *testing.T) {
	d := portableDB(t)
	if err := d.SeedTool("jsontext", "fixture", json.RawMessage(`{}`), json.RawMessage(`["worker"]`)); err != nil {
		t.Fatal(err)
	}
	var typ string
	if err := d.QueryRow(`SELECT typeof(agents) FROM tools WHERE key='jsontext'`).Scan(&typ); err != nil || typ != "text" {
		t.Fatalf("agents must be TEXT: %q %v", typ, err)
	}
	id, err := d.CreateInterceptPending(0, 0, "", "fixture", "fixture", []byte(`{}`), "fixture", &InterceptAudit{InitialAction: "ask"})
	if err != nil {
		t.Fatal(err)
	}
	if err = d.QueryRow(`SELECT typeof(tool_input)||','||typeof(audit) FROM intercept_pending WHERE id=$1`, id).Scan(&typ); err != nil || typ != "text,text" {
		t.Fatalf("audit must be TEXT: %q %v", typ, err)
	}
}
