package backup

import (
	"errors"
	"github.com/google/uuid"
	"os"
	"path/filepath"
	"syscall"
)

const MaintenanceRoot = "/var/lib/ddyidc-maintenance"
const SigningKeyPath = "/etc/ddyidc/secrets/backup-signing.key"

// Both the key and every ancestor must be outside the application identity's
// control. A root-owned file inside an app-owned parent is not a trust boundary.
func checkRootPath(path string, directory bool) error {
	for current := path; ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Uid != 0 || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0022 != 0 {
			return errors.New("维护路径须由 root 独占控制")
		}
		if current == path {
			if directory && !info.IsDir() || !directory && !info.Mode().IsRegular() {
				return errors.New("维护路径类型无效")
			}
			if info.Mode().Perm()&0077 != 0 {
				return errors.New("维护路径须禁止其他用户访问")
			}
		} else if !info.IsDir() {
			return errors.New("维护路径父目录无效")
		}
		if current == filepath.Dir(current) {
			break
		}
	}
	return nil
}

func (s *Service) pendingRecovery() error {
	entries, err := os.ReadDir(filepath.Join(s.privateRoot, "restore"))
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if _, err := uuid.Parse(entry.Name()); err != nil {
			return errors.New("维护恢复目录含未知记录，需运维核验")
		}
		base := filepath.Join(s.privateRoot, "restore", entry.Name())
		phase, err := os.ReadFile(filepath.Join(base, "phase"))
		if err == nil && string(phase) == "released" {
			continue
		}
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		if err == nil {
			return errors.New("存在未放行恢复，禁止继续自动备份或恢复")
		}
		if _, err := os.Lstat(filepath.Join(base, "runtime-accounts.json")); err == nil || !os.IsNotExist(err) {
			return errors.New("存在中断的账户隔离计划，需运维核验")
		}
	}
	return nil
}

// Called only by the privileged worker, never by the HTTP application.
func (s *Service) ConfigureMaintenance() error {
	if os.Geteuid() != 0 {
		return errors.New("维护工作器需要独立特权身份")
	}
	if err := checkRootPath(MaintenanceRoot, true); err != nil {
		return err
	}
	if err := checkRootPath(SigningKeyPath, false); err != nil {
		return err
	}
	key, err := os.ReadFile(SigningKeyPath)
	if err != nil {
		return err
	}
	if len(key) != 32 {
		return errors.New("独立备份签名密钥长度无效")
	}
	s.privateRoot = MaintenanceRoot
	s.signingKey = key
	for _, name := range []string{"jobs", "restore", "results"} {
		path := filepath.Join(s.privateRoot, name)
		if err := os.MkdirAll(path, 0700); err != nil {
			return err
		}
		if err := checkRootPath(path, true); err != nil {
			return err
		}
	}
	return nil
}

// PrepareWorkerEnvironment keeps privileged subprocess scratch files out of
// application-owned directories. Never call runtimepaths.Prepare as root here.
func (s *Service) PrepareWorkerEnvironment() error {
	if s.privateRoot == "" {
		return errors.New("维护目录未初始化")
	}
	if err := checkRootPath(s.privateRoot, true); err != nil {
		return err
	}
	for _, name := range []string{"tmp", "home", "cache"} {
		path := filepath.Join(s.privateRoot, name)
		if err := os.MkdirAll(path, 0700); err != nil {
			return err
		}
		if err := checkRootPath(path, true); err != nil {
			return err
		}
	}
	for key, name := range map[string]string{"TMPDIR": "tmp", "TMP": "tmp", "TEMP": "tmp", "HOME": "home", "XDG_CACHE_HOME": "cache"} {
		if err := os.Setenv(key, filepath.Join(s.privateRoot, name)); err != nil {
			return err
		}
	}
	return nil
}
