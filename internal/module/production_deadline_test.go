package module_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xibodev/compa/v2/internal/module"
)

// Two default deadlines exist and they are NOT interchangeable:
//
//	DefaultDeadline          60s   the Runner's fallback for a hand-built Request
//	DefaultInvokeDeadlineMS  180s  what every production caller passes, and what
//	                               the contract documents to modules
//
// A sibling lane read the documented 180000 and found the shipped 60s constant,
// and reasonably concluded one of them was stale. Neither is -- but that is only
// true while every production path sets its own. The moment one forgets,
// the sibling's reading becomes correct and a real render silently gets a third
// of its budget before the process tree is killed.
//
// So the comment on DefaultDeadline makes a claim about the whole repo, and a
// claim about the whole repo that only a person can check is the kind of prose
// this contract work keeps finding wrong. This checks it.
//
// WHAT THIS CANNOT CATCH, stated so nobody assumes otherwise: it matches a
// request literal to an Invoke call inside one function. A production path that
// builds its request in a helper, or mutates DeadlineMS afterwards, is invisible
// here. It catches the realistic regression -- a new call site written by
// copying an old one and dropping a field -- not an adversarial one.
func TestEveryProductionInvocationSetsItsOwnDeadline(t *testing.T) {
	root := repoRoot(t)

	var checked int
	for _, dir := range []string{"internal", "cmd", "web"} {
		_ = filepath.WalkDir(filepath.Join(root, dir), func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				// The frontend's dependencies and the tool caches hold no Go
				// and are large.
				if name := d.Name(); name == "node_modules" || strings.HasPrefix(name, ".") {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") {
				return nil
			}
			if strings.HasSuffix(path, "_test.go") {
				return nil // tests bound themselves; the fallback exists for them
			}

			fset := token.NewFileSet()
			f, perr := parser.ParseFile(fset, path, nil, 0)
			if perr != nil {
				return nil
			}

			ast.Inspect(f, func(n ast.Node) bool {
				fn, ok := n.(*ast.FuncDecl)
				if !ok || fn.Body == nil {
					return true
				}
				if !callsInvoke(fn.Body) {
					return true
				}
				// This function invokes a module. Every modproto.Request
				// literal in it must set DeadlineMS.
				ast.Inspect(fn.Body, func(m ast.Node) bool {
					lit, ok := m.(*ast.CompositeLit)
					if !ok || !isModprotoRequest(lit) {
						return true
					}
					checked++
					if !setsField(lit, "DeadlineMS") {
						rel, _ := filepath.Rel(root, path)
						t.Errorf("%s:%d builds a modproto.Request for an Invoke"+
							" without DeadlineMS, so it silently falls back to"+
							" %v instead of the documented %dms. A real render"+
							" measured 1m55s would be killed mid-run.",
							filepath.ToSlash(rel), fset.Position(lit.Pos()).Line,
							module.DefaultDeadline, module.DefaultInvokeDeadlineMS)
					}
					return true
				})
				return true
			})
			return nil
		})
	}

	// A scan that finds nothing passes for the wrong reason -- a renamed type or
	// a moved directory would silence it exactly like a clean repo.
	if checked == 0 {
		t.Fatal("scanned no production invocation sites at all, so this test" +
			" proves nothing; the type name or the scanned directories have" +
			" probably moved")
	}
	t.Logf("checked %d production request literals on Invoke paths", checked)
}

func callsInvoke(body *ast.BlockStmt) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if ok && sel.Sel.Name == "Invoke" {
			found = true
		}
		return !found
	})
	return found
}

func isModprotoRequest(lit *ast.CompositeLit) bool {
	sel, ok := lit.Type.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && pkg.Name == "modproto" && sel.Sel.Name == "Request"
}

func setsField(lit *ast.CompositeLit, name string) bool {
	for _, e := range lit.Elts {
		kv, ok := e.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		if id, ok := kv.Key.(*ast.Ident); ok && id.Name == name {
			return true
		}
	}
	return false
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 6; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		dir = filepath.Dir(dir)
	}
	t.Fatal("could not find repo root")
	return ""
}
