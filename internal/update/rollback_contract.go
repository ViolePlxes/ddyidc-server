package update

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
)

// Change only for an incompatible persisted-data contract. This mode does not
// load application configuration, create a session or connect to the database.
const BackendRollbackContract = "ddyidc:cdn-yuan-v1"

func VerifyBackendRollbackContract(ctx context.Context, runner CommandRunner, executable string) error {
	if !filepath.IsAbs(executable) {
		return errors.New("回退二进制路径无效")
	}
	output, err := runner.Run(ctx, "/", executable, []string{"-rollback-contract"}, nil)
	if err != nil || strings.TrimSpace(output) != BackendRollbackContract {
		return errors.New("旧后端不兼容CDN元金额数据，禁止切换；须先完成兼容版本过渡与回滚演练")
	}
	return nil
}
