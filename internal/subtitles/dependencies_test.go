package subtitles

import (
	"go/build"
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"
)

func TestSubtitlePackageHasNoRuntimeDependencies(t *testing.T) {
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
		// Parse every platform's runtime files, independent of current build tags.
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
			for _, boundary := range []string{"os", "io/fs", "io/ioutil", "path/filepath", "syscall", "unsafe", "plugin", "net", "database", "runtime/cgo"} {
				if name == boundary || strings.HasPrefix(name, boundary+"/") {
					t.Errorf("supplied subtitle runtime must not depend on filesystem/process/network facilities: %s imports %s", entry.Name(), name)
				}
			}
			pkg, err := build.Default.Import(name, ".", build.FindOnly)
			if err != nil || !pkg.Goroot {
				t.Errorf("supplied subtitle runtime may import only standard library and edit: %s imports %s", entry.Name(), name)
			}
		}
	}
	if runtimeFiles == 0 {
		t.Fatal("no subtitle runtime files were checked")
	}
}
