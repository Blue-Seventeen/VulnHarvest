package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Snapshot every column, as PostgreSQL's to_jsonb(row) does. SQLite's JSON TEXT
// columns must remain JSON values, SQL NULL stays null, and integers never pass
// through float64. Every query uses the creation transaction's writer snapshot.
func sqliteRetestSnapshot(ctx context.Context, tx *sql.Tx, findingID int64) (string, []byte, error) {
	finding, err := retestSnapshotRows(ctx, tx, `SELECT * FROM findings WHERE id=$1`, findingID)
	if err != nil {
		return "", nil, err
	}
	if len(finding) != 1 {
		return "", nil, sql.ErrNoRows
	}
	assets, err := retestSnapshotRows(ctx, tx, `SELECT * FROM assets WHERE id IN
		(SELECT value FROM json_each((SELECT asset_ids FROM findings WHERE id=$1))) ORDER BY id`, findingID)
	if err != nil {
		return "", nil, err
	}
	constraints, err := retestSnapshotRows(ctx, tx, `SELECT c.* FROM task_constraints c
		JOIN tasks t ON t.exploration_id=c.exploration_id JOIN findings f ON f.task_id=t.id WHERE f.id=$1 ORDER BY c.id`, findingID)
	if err != nil {
		return "", nil, err
	}
	title, _ := finding[0]["name"].(string)
	if title == "" {
		title, _ = finding[0]["vulnclass"].(string)
	}
	if title == "" {
		title = "未分类"
	}
	raw, err := json.Marshal(map[string]any{"finding": finding[0], "assets": assets, "constraints": constraints})
	return title, raw, err
}

func retestSnapshotRows(ctx context.Context, tx *sql.Tx, query string, id int64) ([]map[string]any, error) {
	rows, err := tx.QueryContext(ctx, query, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns, err := rows.ColumnTypes()
	if err != nil {
		return nil, err
	}
	out := []map[string]any{}
	for rows.Next() {
		values, dest := make([]any, len(columns)), make([]any, len(columns))
		for i := range values {
			dest[i] = &values[i]
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		row := map[string]any{}
		for i, col := range columns {
			value := values[i]
			if value != nil {
				switch col.Name() {
				case "asset_ids", "task_ids", "bound_domains", "open_ports", "record_value", "technologies", "auth", "params", "extra":
					raw := json.RawMessage(fmt.Sprint(value))
					if b, ok := value.([]byte); ok {
						raw = append(json.RawMessage(nil), b...)
					}
					if !json.Valid(raw) {
						return nil, fmt.Errorf("invalid snapshot JSON in %s", col.Name())
					}
					value = raw
				default:
					if strings.EqualFold(col.DatabaseTypeName(), "BOOLEAN") {
						if n, ok := value.(int64); ok {
							value = n != 0
						}
					} else if stamp, ok := value.(time.Time); ok {
						value = stamp.UTC()
					}
				}
			}
			row[col.Name()] = value
		}
		out = append(out, row)
	}
	return out, rows.Err()
}
