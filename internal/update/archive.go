package update

import (
	"archive/tar"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
)

func validateSourceArchive(filename string) error {
	file, err := os.Open(filename)
	if err != nil {
		return err
	}
	defer file.Close()
	archive := tar.NewReader(file)
	for {
		header, err := archive.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		name := path.Clean(header.Name)
		if path.IsAbs(name) || name == ".." || strings.HasPrefix(name, "../") || strings.Contains(name, "\\") || len(name) > 4096 {
			return fmt.Errorf("unsafe source archive path")
		}
		if header.Typeflag == tar.TypeXGlobalHeader {
			if len(header.PAXRecords) != 1 {
				return fmt.Errorf("unexpected global archive metadata")
			}
			commit := header.PAXRecords["comment"]
			decoded, e := hex.DecodeString(commit)
			if e != nil || len(decoded) != 20 {
				return fmt.Errorf("invalid archive provenance")
			}
			continue
		}
		if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA && header.Typeflag != tar.TypeDir {
			return fmt.Errorf("source archive contains link or special file: %s", name)
		}
	}
}
