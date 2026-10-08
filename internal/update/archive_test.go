package update

import (
	"archive/tar"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestGitSourceArchiveIsSafe(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("isolated source"), 0644); err != nil {
		t.Fatal(err)
	}
	commands := [][]string{{"init", "-b", "main"}, {"add", "README.md"}, {"-c", "user.name=Release Test", "-c", "user.email=release@example.test", "commit", "-m", "isolated"}, {"archive", "--format=tar", "--output=" + filepath.Join(root, "source.tar"), "HEAD"}}
	for _, args := range commands {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if out, e := cmd.CombinedOutput(); e != nil {
			t.Fatalf("git: %v %s", e, out)
		}
	}
	if e := validateSourceArchive(filepath.Join(root, "source.tar")); e != nil {
		t.Fatal(e)
	}
}

func TestSourceArchiveRejectsLinksAndTraversal(t *testing.T) {
	for _, header := range []*tar.Header{{Name: "escape", Typeflag: tar.TypeSymlink, Linkname: "/etc"}, {Name: "../escape", Typeflag: tar.TypeReg, Mode: 0644}, {Name: "escape", Typeflag: tar.TypeLink, Linkname: "/etc/ddyidc/config.yaml"}} {
		t.Run(header.Name, func(t *testing.T) {
			file, e := os.Create(filepath.Join(t.TempDir(), "source.tar"))
			if e != nil {
				t.Fatal(e)
			}
			writer := tar.NewWriter(file)
			if e = writer.WriteHeader(header); e != nil {
				t.Fatal(e)
			}
			writer.Close()
			file.Close()
			if validateSourceArchive(file.Name()) == nil {
				t.Fatal("unsafe archive accepted")
			}
		})
	}
}
