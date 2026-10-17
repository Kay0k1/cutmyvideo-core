package mediasession

import (
	"go/build"
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"
)

func TestMediaSessionPackageHasNoServiceDependencies(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	files := 0
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		files++
		file, err := parser.ParseFile(token.NewFileSet(), entry.Name(), nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, dependency := range file.Imports {
			name, err := strconv.Unquote(dependency.Path.Value)
			if err != nil {
				t.Fatal(err)
			}
			if name == "github.com/Kay0k1/cutmyvideo-core/internal/edit" ||
				name == "github.com/Kay0k1/cutmyvideo-core/internal/mediaexport" ||
				name == "github.com/Kay0k1/cutmyvideo-core/internal/mediainfo" ||
				name == "github.com/Kay0k1/cutmyvideo-core/internal/subtitles" {
				continue
			}
			for _, boundary := range []string{"os/exec", "syscall", "unsafe", "plugin", "net", "database", "runtime/cgo"} {
				if name == boundary || strings.HasPrefix(name, boundary+"/") {
					t.Errorf("media sessions must not execute tools or depend on network/service facilities: %s imports %s", entry.Name(), name)
				}
			}
			pkg, err := build.Default.Import(name, ".", build.FindOnly)
			if err != nil || !pkg.Goroot {
				t.Errorf("media sessions may import only standard library and neutral media packages: %s imports %s", entry.Name(), name)
			}
		}
	}
	if files == 0 {
		t.Fatal("no media session runtime files were checked")
	}
}
