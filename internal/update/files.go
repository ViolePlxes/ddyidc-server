package update

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

type OSFileOps struct{}

func (OSFileOps) MkdirAll(path string) error  { return os.MkdirAll(path, 0o755) }
func (OSFileOps) RemoveAll(path string) error { return os.RemoveAll(path) }

func (OSFileOps) CopyFile(source, destination string) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	if err = os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return err
	}
	temporary := destination + ".copying"
	output, err := os.Create(temporary)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(output, input)
	closeErr := output.Close()
	if copyErr != nil {
		_ = os.Remove(temporary)
		return copyErr
	}
	if closeErr != nil {
		_ = os.Remove(temporary)
		return closeErr
	}
	_ = os.Remove(destination)
	return os.Rename(temporary, destination)
}

func (ops OSFileOps) CopyDirectory(source, destination string) error {
	if err := os.RemoveAll(destination); err != nil {
		return err
	}
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, relative)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("release tree contains symbolic link: %s", relative)
		}
		return ops.CopyFile(path, target)
	})
}

func (ops OSFileOps) ReplaceDirectory(source, destination string) error {
	if _, err := os.Lstat(destination); os.IsNotExist(err) {
		return os.Rename(source, destination)
	}
	return exchangeDirectories(source, destination)
}

func normalizeStaticTree(root string) error {
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("static tree contains symbolic link: %s", path)
		}
		mode := os.FileMode(0o644)
		if entry.IsDir() {
			mode = 0o755
		}
		return os.Chmod(path, mode)
	})
}

func (OSFileOps) WriteJSON(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	temporary := path + ".writing"
	if err = os.WriteFile(temporary, data, 0o600); err != nil {
		return err
	}
	file, err := os.OpenFile(temporary, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	err = file.Sync()
	_ = file.Close()
	if err != nil {
		return err
	}
	if err = os.Rename(temporary, path); err != nil {
		_ = os.Remove(temporary)
		return fmt.Errorf("publish json: %w", err)
	}
	return syncDirectory(filepath.Dir(path))
}
