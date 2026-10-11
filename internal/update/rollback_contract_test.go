package update

import (
	"context"
	"errors"
	"testing"
)

type contractRunner struct {
	output string
	err    error
	calls  int
}

func (r *contractRunner) Run(ctx context.Context, dir, exe string, args, env []string) (string, error) {
	r.calls++
	if len(args) != 1 || args[0] != "-rollback-contract" || len(env) != 0 {
		return "", errors.New("unsafe invocation")
	}
	return r.output, r.err
}
func TestBackendRollbackContract(t *testing.T) {
	for _, tc := range []struct {
		out  string
		err  error
		pass bool
	}{{BackendRollbackContract + "\n", nil, true}, {"legacy-cents", nil, false}, {"", errors.New("unknown flag"), false}, {"", nil, false}} {
		runner := &contractRunner{output: tc.out, err: tc.err}
		err := VerifyBackendRollbackContract(context.Background(), runner, "/opt/ddyidc/bin/server")
		if (err == nil) != tc.pass {
			t.Fatal("incorrect rollback compatibility decision", err)
		}
	}
	runner := &contractRunner{}
	if VerifyBackendRollbackContract(context.Background(), runner, "relative") == nil || runner.calls != 0 {
		t.Fatal("untrusted relative binary path invoked")
	}
}
