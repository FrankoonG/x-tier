package xrayrt

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestProductionXrayCoreConstructionIsConfinedToRuntime(t *testing.T) {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate test source")
	}
	repository := filepath.Clean(filepath.Join(filepath.Dir(filename), "..", ".."))
	for _, tree := range []string{"cmd", "internal"} {
		root := filepath.Join(repository, tree)
		err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() || filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			relative, err := filepath.Rel(repository, path)
			if err != nil {
				return err
			}
			allowed := filepath.ToSlash(relative) == "internal/xrayrt/runtime.go" ||
				filepath.ToSlash(relative) == "internal/xraycarrier/freedom.go"
			file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
			if err != nil {
				return err
			}
			ast.Inspect(file, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				selector, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				packageName, ok := selector.X.(*ast.Ident)
				if ok && packageName.Name == "core" &&
					(selector.Sel.Name == "New" || selector.Sel.Name == "NewWithContext" || selector.Sel.Name == "StartInstance") && !allowed {
					t.Errorf("%s constructs an Xray core outside an approved runtime boundary", filepath.ToSlash(relative))
				}
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}
