package backup

import (
	"io"
	"os"
	"os/user"
	"strconv"
)

// Pin the runtime root and modify permissions through the newly created file
// descriptor. An application-controlled parent rename/symlink cannot redirect
// privileged archive writes or chown into /etc or another filesystem tree.
func (s *Service) publishArchive(source, relative string) error {
	root, err := os.OpenRoot(s.Root)
	if err != nil {
		return err
	}
	defer root.Close()
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := root.OpenFile(relative, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	done := false
	defer func() {
		out.Close()
		if !done {
			_ = root.Remove(relative)
		}
	}()
	if _, err = io.Copy(out, in); err != nil {
		return err
	}
	u, err := user.Lookup("ddyidc")
	if err != nil {
		return err
	}
	uid, err := strconv.Atoi(u.Uid)
	if err != nil {
		return err
	}
	gid, err := strconv.Atoi(u.Gid)
	if err != nil {
		return err
	}
	if err = out.Chown(uid, gid); err != nil {
		return err
	}
	if err = out.Sync(); err != nil {
		return err
	}
	if err = out.Close(); err != nil {
		return err
	}
	done = true
	return nil
}
