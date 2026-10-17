package coremodel

import (
	"go/build"
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"
)

func TestCoreModelPackageHasNoServiceDependencies(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), entry.Name(), nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, dependency := range file.Imports {
			name, err := strconv.Unquote(dependency.Path.Value)
			if err != nil {
				t.Fatal(err)
			}
			if name == "github.com/Kay0k1/cutmyvideo-core/internal/edit" {
				continue
			}
			pkg, err := build.Default.Import(name, ".", build.FindOnly)
			if err != nil || !pkg.Goroot {
				t.Errorf("compatibility carrier imports service or external package: %s imports %s", entry.Name(), name)
			}
		}
	}
}
