package update

import (
	"errors"
	"strings"
)

// Preserve an exit code through wrapped runner errors without parsing command output.
func candidateExitCode(err error) int {
	var exit interface{ ExitCode() int }
	if errors.As(err, &exit) {
		return exit.ExitCode()
	}
	return -1
}

func migrationPreflightMessage(output string) string {
	switch {
	case strings.Contains(output, "Inbox runtime preflight failed:"):
		return "运行账号的消息中心权限校验失败，旧服务未停止；请检查最小权限授权"
	case strings.Contains(output, "Adapter compatibility preflight failed:"):
		return "模块兼容性校验失败，旧服务未停止；请检查已保留模块"
	case strings.Contains(output, "Rollback compatibility preflight failed:"):
		return "旧版本回滚兼容性校验失败，旧服务未停止"
	case strings.Contains(output, "Production reclamation preflight failed:"):
		return "生产回收基础设施校验失败，旧服务未停止；请更新受控回收服务"
	case strings.Contains(output, "Database migration preflight failed:"):
		return "数据库迁移历史校验失败，旧服务未停止；请核对脏迁移、校验和及迁移记录读取权限"
	default:
		return "候选进程预检未完成，旧服务未停止；请检查候选配置、运行身份及启动日志"
	}
}
