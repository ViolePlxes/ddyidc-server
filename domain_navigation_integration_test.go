package main

import (
	"database/sql"
	"ddyidc/internal/shared/migrations"
	"testing"
)

func checkDomainNavigationMigration(t *testing.T, pool *sql.DB, steps []migrations.Step) {
	t.Helper()
	var script string
	for _, step := range steps {
		if step.Name == "057_domain_navigation.sql" {
			script = step.SQL
		}
	}
	if script == "" {
		t.Fatal("domain migration missing")
	}
	for _, scenario := range []string{"legacy", "customized", "existing-domain-menu", "unseeded"} {
		t.Run("domain-navigation-"+scenario, func(t *testing.T) {
			tx, err := pool.Begin()
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			exec := func(query string) {
				t.Helper()
				if _, err := tx.Exec(query); err != nil {
					t.Fatal(err)
				}
			}
			var originalOthers, originalAdmin int
			if err := tx.QueryRow("SELECT COUNT(*) FROM system.navigation_nodes WHERE id NOT BETWEEN 950000100 AND 950000109").Scan(&originalOthers); err != nil {
				t.Fatal(err)
			}
			if err := tx.QueryRow("SELECT revision FROM system.navigation_scopes WHERE scope='admin'").Scan(&originalAdmin); err != nil {
				t.Fatal(err)
			}
			if scenario == "customized" {
				exec("UPDATE system.navigation_nodes SET name='自定义解析',enabled=0,sort=77 WHERE id=950000107")
			} else {
				exec("DELETE FROM system.navigation_nodes WHERE id BETWEEN 950000100 AND 950000109")
				if scenario == "unseeded" {
					exec("UPDATE system.navigation_scopes SET seeded=0 WHERE scope='console'")
				}
				if scenario == "existing-domain-menu" {
					exec("INSERT INTO system.navigation_nodes(id,scope,parent_id,name,href,target,icon,gate,enabled,sort,created_at,updated_at) VALUES(950000200,'console',0,'客户配置','','self','GlobeIcon','',1,9,NOW(),NOW()),(950000201,'console',950000200,'自定义域名','/console/domains','self','GlobeIcon','',0,1,NOW(),NOW())")
					originalOthers += 2
				}
			}
			exec(script)
			exec(script)
			var count, others, adminRevision int
			if err := tx.QueryRow("SELECT COUNT(*) FROM system.navigation_nodes WHERE id BETWEEN 950000100 AND 950000109").Scan(&count); err != nil {
				t.Fatal(err)
			}
			expected := 10
			if scenario == "existing-domain-menu" || scenario == "unseeded" {
				expected = 0
			}
			if count != expected {
				t.Fatalf("domain nodes: got %d want %d", count, expected)
			}
			if err := tx.QueryRow("SELECT COUNT(*) FROM system.navigation_nodes WHERE id NOT BETWEEN 950000100 AND 950000109").Scan(&others); err != nil {
				t.Fatal(err)
			}
			if others != originalOthers {
				t.Fatal("unrelated navigation changed")
			}
			if err := tx.QueryRow("SELECT revision FROM system.navigation_scopes WHERE scope='admin'").Scan(&adminRevision); err != nil {
				t.Fatal(err)
			}
			if adminRevision != originalAdmin {
				t.Fatal("admin navigation revision changed")
			}
			if scenario == "customized" {
				var preserved int
				if err := tx.QueryRow("SELECT COUNT(*) FROM system.navigation_nodes WHERE id=950000107 AND name='自定义解析' AND enabled=0 AND sort=77").Scan(&preserved); err != nil {
					t.Fatal(err)
				}
				if preserved != 1 {
					t.Fatal("saved customization overwritten")
				}
			}
		})
	}
}
