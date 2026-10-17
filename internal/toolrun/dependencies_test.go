package toolrun

import (
	"go/build"
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"
)

func TestToolrunPackageUsesOnlyStandardLibrary(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	runtimeFiles := 0
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		runtimeFiles++
		// Include Windows and Unix imports even when this test runs on one host.
		file, err := parser.ParseFile(token.NewFileSet(), entry.Name(), nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, dependency := range file.Imports {
			name, err := strconv.Unquote(dependency.Path.Value)
			if err != nil {
				t.Fatal(err)
			}
			if name == "net" || strings.HasPrefix(name, "net/") || name == "database" || strings.HasPrefix(name, "database/") {
				t.Errorf("tool execution must not depend on network/database clients: %s imports %s", entry.Name(), name)
			}
			pkg, err := build.Default.Import(name, ".", build.FindOnly)
			if err != nil || !pkg.Goroot {
				t.Errorf("tool execution must not import service/edit/external packages: %s imports %s", entry.Name(), name)
			}
		}
	}
	if runtimeFiles == 0 {
		t.Fatal("no tool runtime files were checked")
	}
}
