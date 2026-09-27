package architecture

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func migrationModeViolations(path string, source []byte) ([]string, error) {
	f, err := parser.ParseFile(token.NewFileSet(), path, source, 0)
	if err != nil {
		return nil, err
	}
	aliases := map[string]bool{}
	var problems []string
	for _, imp := range f.Imports {
		value, _ := strconv.Unquote(imp.Path.Value)
		if value != "ddyidc/internal/shared/schema" {
			continue
		}
		name := "schema"
		if imp.Name != nil {
			name = imp.Name.Name
		}
		if name == "." {
			problems = append(problems, "dot import hides migration enablement")
		}
		aliases[name] = true
	}
	var allowed []*ast.BlockStmt
	if path == "main.go" {
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Name.Name != "main" || fn.Body == nil {
				continue
			}
			// Only the explicit CLI migration branch is reviewed. Similar names,
			// negated flags or arbitrary helper functions are not exemptions.
			for _, stmt := range fn.Body.List {
				branch, ok := stmt.(*ast.IfStmt)
				if !ok {
					continue
				}
				flag, ok := branch.Cond.(*ast.StarExpr)
				if !ok {
					continue
				}
				name, ok := flag.X.(*ast.Ident)
				if ok && name.Name == "migrate" {
					allowed = append(allowed, branch.Body)
				}
			}
		}
	}
	ast.Inspect(f, func(n ast.Node) bool {
		selector, ok := n.(*ast.SelectorExpr)
		if !ok || selector.Sel.Name != "EnableMigrations" {
			return true
		}
		alias, ok := selector.X.(*ast.Ident)
		if !ok || !aliases[alias.Name] {
			return true
		}
		for _, block := range allowed {
			if selector.Pos() > block.Pos() && selector.End() < block.End() {
				return true
			}
		}
		problems = append(problems, "migration mode enabled outside explicit offline CLI branch")
		return true
	})
	return problems, nil
}

func TestMigrationModeBoundary(t *testing.T) {
	root := filepath.Join("..", "..")
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" || d.Name() == "vendor" || d.Name() == "deploy" || d.Name() == "tools" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		source, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		problems, err := migrationModeViolations(filepath.ToSlash(rel), source)
		if err != nil {
			return err
		}
		for _, problem := range problems {
			t.Errorf("%s: %s", rel, problem)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestMigrationModeMutations(t *testing.T) {
	for _, tc := range []struct {
		path, body string
		bad        bool
	}{
		{"main.go", "func main(){if *migrate {m.EnableMigrations()}}", false},
		{"main.go", "func main(){m.EnableMigrations()}", true},
		{"main.go", "func main(){if !*migrate {m.EnableMigrations()}}", true},
		{"internal/new/handler.go", "func handle(){m.EnableMigrations()}", true},
		{"internal/new/handler.go", "var bypass=m.EnableMigrations", true},
		{"internal/new/handler.go", "func read(){m.MigrationsEnabled()}", false},
	} {
		source := []byte("package main\nimport m \"ddyidc/internal/shared/schema\"\n" + tc.body)
		problems, err := migrationModeViolations(tc.path, source)
		if err != nil || (len(problems) > 0) != tc.bad {
			t.Fatalf("%s: %v %v", tc.body, problems, err)
		}
	}
}
