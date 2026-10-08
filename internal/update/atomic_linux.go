package update

import (
	"golang.org/x/sys/unix"
	"os"
)

// exchangeDirectories never removes the live path. Both paths must exist on
// the same filesystem; unsupported filesystems fail before publication.
func exchangeDirectories(candidate, live string) error {
	return unix.Renameat2(unix.AT_FDCWD, candidate, unix.AT_FDCWD, live, unix.RENAME_EXCHANGE)
}

func syncDirectory(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
