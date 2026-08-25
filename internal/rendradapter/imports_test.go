package rendradapter

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestRendrImportsAreConfinedToAdapter(t *testing.T) {
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
			if entry.IsDir() || filepath.Ext(path) != ".go" {
				return nil
			}
			relative, err := filepath.Rel(repository, path)
			if err != nil {
				return err
			}
			if strings.HasPrefix(filepath.ToSlash(relative), "internal/rendradapter/") {
				return nil
			}
			file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
			if err != nil {
				return err
			}
			for _, spec := range file.Imports {
				value := strings.Trim(spec.Path.Value, "\"")
				if value == "github.com/FrankoonG/rendr" || strings.HasPrefix(value, "github.com/FrankoonG/rendr/") {
					position := file.Pos()
					if spec.Pos().IsValid() {
						position = spec.Pos()
					}
					t.Errorf("%s imports rendr outside the adapter (position %d)", filepath.ToSlash(relative), position)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}
