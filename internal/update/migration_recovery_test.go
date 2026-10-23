package update

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

type migrationRecoveryRunner struct{ calls [][]string }

func (r *migrationRecoveryRunner) Run(_ context.Context, _, _ string, args []string, _ []string) (string, error) {
	r.calls = append(r.calls, args)
	return "", nil
}

func TestMigrationFailureRestoresOnlyItsOwnPreparedSource(t *testing.T) {
	root := t.TempDir()
	runner := &migrationRecoveryRunner{}
	config := Config{ResultDir: root, GitPath: "/usr/bin/git", Backend: TargetConfig{Workspace: filepath.Join(root, "source"), Executable: filepath.Join(root, "server")}}
	m := &Manager{config: config, runner: runner}
	plan := HelperPlan{NeedsMigration: true, TaskID: "99", BeforeCommit: "before", Workspace: config.Backend.Workspace, CurrentExecutable: config.Backend.Executable, ResultFile: filepath.Join(root, "99.result.json")}
	write := func() {
		data, _ := json.Marshal(plan)
		if err := os.WriteFile(config.helperPlanPath(), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write()
	task := &Task{ID: 99, BeforeCommit: "before"}
	result := HelperResult{TaskID: "99", Status: StatusFailed, RollbackStatus: "not_required"}
	if ok, err := m.restoreMigrationSource(context.Background(), result, task); !ok || err != nil {
		t.Fatal(ok, err)
	}
	if len(runner.calls) != 1 || runner.calls[0][len(runner.calls[0])-1] != "before" {
		t.Fatal("wrong source reset")
	}
	plan.BeforeCommit = "foreign"
	write()
	if _, err := m.restoreMigrationSource(context.Background(), result, task); err == nil {
		t.Fatal("foreign recovery admitted")
	}
	if len(runner.calls) != 1 {
		t.Fatal("foreign source was reset")
	}
	plan.BeforeCommit = "before"
	plan.NeedsMigration = false
	write()
	if ok, err := m.restoreMigrationSource(context.Background(), result, task); !ok || err != nil {
		t.Fatal("ordinary pre-switch refusal did not restore its source", ok, err)
	}
}

func TestLegacyHelperRefusalRestoresSourceWithoutPlan(t *testing.T) {
	runner := &migrationRecoveryRunner{}
	m := &Manager{config: Config{ResultDir: t.TempDir(), GitPath: "/usr/bin/git", Backend: TargetConfig{Workspace: "/prepared/source"}}, runner: runner}
	task := &Task{ID: 99, Target: TargetBackend, Status: StatusRunning, BeforeCommit: "before"}
	result := HelperResult{TaskID: "99", Status: StatusFailed, RollbackStatus: "not_required"}
	if ok, err := m.restoreMigrationSource(context.Background(), result, task); !ok || err != nil || len(runner.calls) != 1 {
		t.Fatal("legacy rejection left prepared source installed", ok, err)
	}
	for _, foreign := range []*Task{nil, {ID: 100, Target: TargetBackend, Status: StatusRunning, BeforeCommit: "before"}, {ID: 99, Target: TargetFrontend, Status: StatusRunning, BeforeCommit: "before"}, {ID: 99, Target: TargetBackend, Status: StatusSuccess, BeforeCommit: "before"}} {
		if ok, err := m.restoreMigrationSource(context.Background(), result, foreign); ok || err != nil {
			t.Fatal("unrelated result admitted", ok, err)
		}
	}
	if len(runner.calls) != 1 {
		t.Fatal("unrelated checkout changed")
	}
}
