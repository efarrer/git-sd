package github

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestEveryGHCallAndPushGoesThroughTheChokePoint enforces the design in
// DESIGN.md ("GitHub CLI access"): gh being missing or unauthenticated is
// reported identically by every command only because every gh invocation goes
// through the wrapper in gh.go, and a command can't push before verifying gh
// only because pushes go through functions that call ensureReady. Adding a
// stray exec.Command("gh", ...) or "git push" elsewhere fails this test.
func TestEveryGHCallAndPushGoesThroughTheChokePoint(t *testing.T) {
	root := filepath.Join("..", "..")
	fset := token.NewFileSet()

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		// Skip hidden entries: .git, and macOS "._name" metadata files that
		// network mounts (e.g. sshfs) create next to real sources.
		if path != root && strings.HasPrefix(d.Name(), ".") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		slash := filepath.ToSlash(path)

		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			callsEnsureReady := false
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "ensureReady" {
					callsEnsureReady = true
				}
				return true
			})
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "Command" || len(call.Args) == 0 {
					return true
				}
				if pkg, ok := sel.X.(*ast.Ident); !ok || pkg.Name != "exec" {
					return true
				}
				first := stringLiteral(call.Args[0])
				second := ""
				if len(call.Args) > 1 {
					second = stringLiteral(call.Args[1])
				}
				switch {
				case first == "gh":
					if !strings.HasSuffix(slash, "internal/github/gh.go") || fn.Name.Name != "ghRun" {
						t.Errorf("%s: %s runs exec.Command(\"gh\", ...) directly; use ghOutput/ghCombined so errors are reported consistently", slash, fn.Name.Name)
					}
				case first == "git" && second == "push":
					if !strings.HasSuffix(slash, "internal/github/github.go") || !callsEnsureReady {
						t.Errorf("%s: %s runs git push without calling ensureReady(); pushes must verify gh first (PushBranch / DeleteRemoteBranch)", slash, fn.Name.Name)
					}
				}
				return true
			})
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func stringLiteral(e ast.Expr) string {
	lit, ok := e.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return ""
	}
	s, err := strconv.Unquote(lit.Value)
	if err != nil {
		return ""
	}
	return s
}
