package update

import (
	"context"
	"database/sql"
	_ "github.com/go-sql-driver/mysql"
	"os"
	"strings"
	"testing"
	"time"
)

func TestReclamationRuntimePrivilegesMySQL(t *testing.T) {
	socket := os.Getenv("FOUNDATION_MYSQL_SOCKET")
	if socket == "" {
		t.Skip("requires disposable FOUNDATION_MYSQL_SOCKET; run test_resource_reclamation.sh")
	}
	if !strings.HasPrefix(socket, "/tmp/ddyidc-foundation-") || strings.Contains(socket, "..") {
		t.Fatal("disposable socket required")
	}
	root, err := sql.Open("mysql", "root@unix("+socket+")/?parseTime=true")
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err = root.ExecContext(ctx, "CREATE USER 'reclamation_runtime_test'@'localhost' IDENTIFIED BY 'IsolatedRuntime123!'"); err != nil {
		t.Fatal(err)
	}
	defer root.Exec("DROP USER 'reclamation_runtime_test'@'localhost'")
	if err = ApplyReclamationPrivileges(ctx, root, "reclamation_runtime_test", "localhost"); err != nil {
		t.Fatal(err)
	}
	limited, err := sql.Open("mysql", "reclamation_runtime_test:IsolatedRuntime123!@unix("+socket+")/?parseTime=true")
	if err != nil {
		t.Fatal(err)
	}
	defer limited.Close()
	for _, query := range []string{
		"SELECT status FROM system.reclamation_state WHERE id=1",
		"SELECT resource_key FROM system.reclamation_resources LIMIT 1",
		"SELECT id FROM system.reclamation_runs LIMIT 1",
	} {
		rows, e := limited.QueryContext(ctx, query)
		if e != nil {
			t.Fatal(e)
		}
		rows.Close()
	}
	if _, err = limited.ExecContext(ctx, "UPDATE system.reclamation_state SET status='preview_pending',requested_at=NOW(6),requested_by=1,next_attempt_at=NULL WHERE id=1"); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		"UPDATE system.reclamation_state SET last_error='forged' WHERE id=1",
		"UPDATE system.reclamation_state SET heartbeat_at=NOW(6) WHERE id=1",
		"INSERT INTO system.reclamation_resources(resource_key,task_id,category,relative_path,status,updated_at) VALUES('forged',0,'registered_copy','x','pending',NOW(6))",
		"DELETE FROM system.reclamation_runs WHERE 1=0",
		"UPDATE billing.accounts SET balance=balance WHERE 1=0",
		"CREATE TABLE system.reclamation_privilege_leak(id INT)",
	} {
		if _, err = limited.ExecContext(ctx, query); err == nil {
			t.Fatalf("restricted identity accepted forbidden operation: %s", query)
		}
	}
	if _, err = root.ExecContext(ctx, "UPDATE system.reclamation_state SET status='pending',requested_at=NULL,requested_by=0,next_attempt_at=NULL WHERE id=1"); err != nil {
		t.Fatal(err)
	}
}
