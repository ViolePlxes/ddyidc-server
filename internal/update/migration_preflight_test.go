package update

import (
	"fmt"
	"strings"
	"testing"
)

type migrationExit int

func (e migrationExit) Error() string { return "candidate rejected" }
func (e migrationExit) ExitCode() int { return int(e) }

func TestMigrationExitCodePreservesWrappedStatus(t *testing.T) {
	if candidateExitCode(fmt.Errorf("wrapped: %w", migrationExit(20))) != 20 {
		t.Fatal("missing migration status lost")
	}
	if candidateExitCode(migrationExit(2)) == 20 || candidateExitCode(fmt.Errorf("permission denied")) == 20 {
		t.Fatal("unsafe automatic migration admission")
	}
}

func TestMigrationPreflightDoesNotMislabelOtherChecksOrExposeOutput(t *testing.T) {
	for _, category := range []string{"Inbox runtime", "Adapter compatibility", "Rollback compatibility", "Production reclamation"} {
		text := migrationPreflightMessage(category + " preflight failed: private-secret-payload")
		if strings.Contains(text, "private-secret-payload") || strings.Contains(text, "脏迁移") {
			t.Fatal("preflight was mislabeled or leaked output")
		}
	}
	if !strings.Contains(migrationPreflightMessage("Database migration preflight failed:"), "读取权限") {
		t.Fatal("database failure classification missing")
	}
}
