package db

import (
	"database/sql"
	"fmt"
	"net"
	"strings"
)

// Optional dialect arguments retain PostgreSQL semantics for existing PG-only
// callers. Every SQLite mutation must pass its owning DB explicitly.
func sqliteDialect(dialect []*DB) bool { return len(dialect) > 0 && dialect[0].IsSQLite() }
func dialectSQL(dialect []*DB, postgres, sqlite string) string {
	if sqliteDialect(dialect) {
		return sqlite
	}
	return postgres
}

func (s *CompanyStore) upsertSQLiteCompany(name, logo string) (id int64, created bool, err error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, false, err
	}
	defer tx.Rollback()
	var logoValue any
	if logo != "" {
		logoValue = logo
	}
	err = tx.QueryRow(`INSERT INTO companies(name,nkey,logo) VALUES($1,$2,$3) ON CONFLICT(nkey) DO NOTHING RETURNING id`, name, companyNKey(name), logoValue).Scan(&id)
	if err == nil {
		created = true
	} else if err == sql.ErrNoRows {
		err = tx.QueryRow(`UPDATE companies SET name=$1,logo=COALESCE($3,logo),updated_at=now() WHERE nkey=$2 RETURNING id`, name, companyNKey(name), logoValue).Scan(&id)
	}
	if err != nil {
		return 0, false, err
	}
	err = tx.Commit()
	return
}

type sqliteCompanyRule struct {
	companyID           int64
	kind, domain, value string
	network             *net.IPNet
	prefix              int
}

func readSQLiteCompanyRules(q rowsQuerier) ([]sqliteCompanyRule, error) {
	rows, err := q.Query(`SELECT company_id,kind,COALESCE(domain,''),COALESCE(net,''),COALESCE(value,'') FROM company_scope WHERE kind<>'keyword' ORDER BY company_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	rules := []sqliteCompanyRule{}
	for rows.Next() {
		var r sqliteCompanyRule
		var network string
		if err = rows.Scan(&r.companyID, &r.kind, &r.domain, &network, &r.value); err != nil {
			return nil, err
		}
		if network != "" {
			_, r.network, err = net.ParseCIDR(network)
			if err != nil {
				return nil, err
			}
			r.prefix, _ = r.network.Mask.Size()
		}
		rules = append(rules, r)
	}
	return rules, rows.Err()
}

func matchSQLiteCompany(rules []sqliteCompanyRule, domain, ip string, icps ...string) *int64 {
	var chosen *int64
	for _, r := range rules {
		if r.kind == "domain" && domain != "" && r.domain == domain {
			id := r.companyID
			return &id
		}
	}
	address := net.ParseIP(ip)
	bestPrefix := -1
	for _, r := range rules {
		if r.network != nil && address != nil && r.network.Contains(address) && r.prefix > bestPrefix {
			bestPrefix = r.prefix
			id := r.companyID
			chosen = &id
		}
	}
	if chosen != nil {
		return chosen
	}
	for _, r := range rules {
		if r.kind != "icp" {
			continue
		}
		for _, icp := range icps {
			if normalized := NormalizeICP(icp); normalized != "" && normalized == r.value {
				id := r.companyID
				return &id
			}
		}
	}
	return nil
}

func resolveSQLiteCompany(q companyScopeQueryer, domain, ip, icp string) (*int64, error) {
	rules, err := readSQLiteCompanyRules(q)
	if err != nil {
		return nil, err
	}
	return matchSQLiteCompany(rules, domain, ip, icp), nil
}

func recomputeSQLiteAttributionTx(tx *sql.Tx) (string, error) {
	// Finish and close every cursor before issuing writes on this single writer.
	rules, err := readSQLiteCompanyRules(tx)
	if err != nil {
		return "", err
	}
	rows, err := tx.Query(`SELECT id,type,COALESCE(root_domain,''),COALESCE(ip,''),COALESCE(icp,''),COALESCE(app_icp,''),company_source FROM assets WHERE company_source='scope' OR company_id IS NULL`)
	if err != nil {
		return "", err
	}
	type row struct {
		id                           int64
		typ, domain, ip, icp, appICP string
		source                       string
	}
	var assets []row
	for rows.Next() {
		var a row
		if err = rows.Scan(&a.id, &a.typ, &a.domain, &a.ip, &a.icp, &a.appICP, &a.source); err != nil {
			rows.Close()
			return "", err
		}
		assets = append(assets, a)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return "", err
	}
	for _, a := range assets {
		domain, ip := "", ""
		switch a.typ {
		case "root_domain", "subdomain", "service", "endpoint":
			domain = a.domain
		}
		switch a.typ {
		case "ip", "subdomain", "service", "endpoint":
			ip = a.ip
		}
		cid := matchSQLiteCompany(rules, domain, ip, a.icp, a.appICP)
		// PG only rewrites an orphaned explicit row when a remaining scope
		// actually matches. An unmatched NULL company retains its provenance.
		if cid == nil && a.source != "scope" {
			continue
		}
		if _, err = tx.Exec(`UPDATE assets SET company_id=$1,company_source='scope' WHERE id=$2`, cid, a.id); err != nil {
			return "", err
		}
	}
	return malformedSQLiteIPWarning(tx)
}

func malformedSQLiteIPWarning(q malformedIPAssetQueryer) (string, error) {
	rows, err := q.Query(`SELECT id,ip FROM assets WHERE ip IS NOT NULL AND ip<>'' AND type IN ('ip','subdomain','service','endpoint') ORDER BY id`)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	total := 0
	samples := []string{}
	for rows.Next() {
		var id int64
		var ip string
		if err = rows.Scan(&id, &ip); err != nil {
			return "", err
		}
		if net.ParseIP(ip) == nil {
			total++
			if len(samples) < malformedIPAssetsSampled {
				samples = append(samples, fmt.Sprintf("#%d %s", id, ip))
			}
		}
	}
	if err = rows.Err(); err != nil {
		return "", err
	}
	if total == 0 {
		return "", nil
	}
	warning := fmt.Sprintf("%d 条资产的 ip 字段不是合法 IP，已跳过 IP/CIDR 范围匹配（这些资产不会被网段规则归属到企业）：%s", total, strings.Join(samples, "、"))
	if total > len(samples) {
		warning += fmt.Sprintf(" 等 %d 条", total)
	}
	return warning, nil
}
