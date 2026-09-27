package backup

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

func copyStorage(ctx context.Context, dataRoot, destination string) error {
	data, err := os.OpenRoot(dataRoot)
	if err != nil {
		return err
	}
	defer data.Close()
	source, err := data.OpenRoot("storage")
	if err != nil {
		return errors.New("附件根目录不可用，拒绝生成不完整备份")
	}
	defer source.Close()
	if err = os.MkdirAll(destination, 0700); err != nil {
		return err
	}
	target, err := os.OpenRoot(destination)
	if err != nil {
		return err
	}
	defer target.Close()
	return fs.WalkDir(source.FS(), ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("附件快照拒绝符号链接")
		}
		if entry.IsDir() {
			return target.MkdirAll(name, 0700)
		}
		if !entry.Type().IsRegular() {
			return errors.New("附件快照拒绝特殊文件")
		}
		in, err := source.Open(name)
		if err != nil {
			return err
		}
		defer in.Close()
		stat, err := in.Stat()
		if err != nil {
			return err
		}
		if !stat.Mode().IsRegular() {
			return errors.New("附件类型发生变化")
		}
		if err = target.MkdirAll(filepath.Dir(name), 0700); err != nil {
			return err
		}
		out, err := target.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		defer out.Close()
		if _, err = io.Copy(out, in); err != nil {
			return err
		}
		if err = out.Sync(); err != nil {
			return err
		}
		return out.Close()
	})
}
