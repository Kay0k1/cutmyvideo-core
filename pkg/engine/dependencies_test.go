package engine_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"go/build"
	"go/parser"
	"go/token"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

const engineModule = "github.com/Kay0k1/cutmyvideo-core"

// This is the runtime boundary, not a restriction on compatibility tests, which
// deliberately compare the engine's public types with the service adapters.
var engineRuntimePackages = map[string]string{
	engineModule + "/pkg/engine":              "pkg/engine",
	engineModule + "/internal/coremodel":      "internal/coremodel",
	engineModule + "/internal/edit":           "internal/edit",
	engineModule + "/internal/fsdurable":      "internal/fsdurable",
	engineModule + "/internal/localmedia":     "internal/localmedia",
	engineModule + "/internal/mediaerror":     "internal/mediaerror",
	engineModule + "/internal/mediaexport":    "internal/mediaexport",
	engineModule + "/internal/mediainfo":      "internal/mediainfo",
	engineModule + "/internal/mediapreflight": "internal/mediapreflight",
	engineModule + "/internal/mediasession":   "internal/mediasession",
	engineModule + "/internal/subtitles":      "internal/subtitles",
	engineModule + "/internal/toolrun":        "internal/toolrun",
}

func forbiddenEngineDependency(path string) bool {
	for _, prefix := range []string{"database/sql", "net", engineModule + "/internal/app", engineModule + "/internal/server"} {
		if path == prefix || strings.HasPrefix(path, prefix+"/") {
			return true
		}
	}
	return false
}

// Parse every runtime file, including files for other operating systems and
// build tags. Following repository imports catches an indirect service bridge.
func engineRuntimeImports(root string, allowed map[string]string) (map[string]bool, error) {
	seen := map[string]bool{}
	queue := []string{engineModule + "/pkg/engine"}
	for len(queue) > 0 {
		path := queue[0]
		queue = queue[1:]
		if seen[path] {
			continue
		}
		seen[path] = true
		directory, ok := allowed[path]
		if !ok {
			return nil, fmt.Errorf("unapproved runtime package %s", path)
		}
		files, err := os.ReadDir(filepath.Join(root, directory))
		if err != nil {
			return nil, err
		}
		count := 0
		for _, entry := range files {
			name := entry.Name()
			if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") {
				continue
			}
			count++
			file, err := parser.ParseFile(token.NewFileSet(), filepath.Join(root, directory, name), nil, parser.ImportsOnly)
			if err != nil {
				return nil, err
			}
			for _, spec := range file.Imports {
				dependency, err := strconv.Unquote(spec.Path.Value)
				if err != nil {
					return nil, err
				}
				if forbiddenEngineDependency(dependency) {
					return nil, fmt.Errorf("%s/%s imports forbidden dependency %s", directory, name, dependency)
				}
				if _, ok := allowed[dependency]; ok {
					queue = append(queue, dependency)
					continue
				}
				standard, err := build.Default.Import(dependency, "", build.FindOnly)
				if err != nil || !standard.Goroot {
					return nil, fmt.Errorf("%s/%s imports unapproved dependency %s", directory, name, dependency)
				}
			}
		}
		if count == 0 {
			return nil, fmt.Errorf("runtime package %s has no Go files", path)
		}
	}
	return seen, nil
}

func engineTestRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	module, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil || !bytes.Contains(module, []byte("module "+engineModule+"\n")) {
		t.Fatalf("cannot locate engine module: %s: %v", root, err)
	}
	return root
}

func engineGoCommand(ctx context.Context, directory string, args ...string) *exec.Cmd {
	name := "go"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	command := exec.CommandContext(ctx, filepath.Join(runtime.GOROOT(), "bin", name), args...)
	command.Dir = directory
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		switch strings.ToUpper(key) {
		case "GOWORK", "GOPROXY", "GOSUMDB", "GOFLAGS", "GOTOOLCHAIN", "GOOS", "GOARCH":
			continue
		}
		command.Env = append(command.Env, entry)
	}
	command.Env = append(command.Env, "GOWORK=off", "GOPROXY=off", "GOSUMDB=off", "GOFLAGS=", "GOTOOLCHAIN=local", "GOOS="+runtime.GOOS, "GOARCH="+runtime.GOARCH)
	return command
}

func TestEngineRuntimeDependencyBoundary(t *testing.T) {
	root := engineTestRoot(t)
	seen, err := engineRuntimeImports(root, engineRuntimePackages)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"internal/localmedia", "internal/coremodel", "internal/mediaerror"} {
		if !seen[engineModule+"/"+path] {
			t.Errorf("engine runtime did not traverse %s", path)
		}
	}

	// go list supplies the actual transitive runtime closure for this native CI
	// target. It intentionally omits -test, so test-only service imports do not
	// disguise the consumer's dependency boundary.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	command := engineGoCommand(ctx, root, "list", "-deps", "-json", "./pkg/engine")
	var stderr bytes.Buffer
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil {
		t.Fatalf("runtime dependency listing: %v\n%s", err, stderr.String())
	}
	decoder := json.NewDecoder(bytes.NewReader(output))
	actual := map[string]bool{}
	for {
		var pkg struct {
			ImportPath string
			Standard   bool
			Incomplete bool
			Error      json.RawMessage
			DepsErrors []json.RawMessage
		}
		if err := decoder.Decode(&pkg); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		if pkg.ImportPath == "" || pkg.Incomplete || len(pkg.Error) > 0 || len(pkg.DepsErrors) > 0 {
			t.Fatalf("incomplete runtime package: %+v", pkg)
		}
		if forbiddenEngineDependency(pkg.ImportPath) || !pkg.Standard && engineRuntimePackages[pkg.ImportPath] == "" {
			t.Errorf("unapproved transitive runtime dependency: %s", pkg.ImportPath)
		}
		actual[pkg.ImportPath] = true
	}
	if !actual[engineModule+"/pkg/engine"] || !actual[engineModule+"/internal/localmedia"] {
		t.Fatal("runtime listing did not include the engine and concrete backend")
	}

	for _, test := range []struct {
		name, engine, backend, platform, testImport string
		valid                                       bool
	}{
		{name: "stdlib-and-test-only-service", engine: `"context"`, testImport: `"` + engineModule + `/internal/app"`, valid: true},
		{name: "direct-service", engine: `"` + engineModule + `/internal/app"`},
		{name: "indirect-service", engine: `"` + engineModule + `/internal/localmedia"`, backend: `"` + engineModule + `/internal/app"`},
		{name: "other-platform-service", engine: `"context"`, platform: `"` + engineModule + `/internal/app"`},
		{name: "sql", engine: `"database/sql"`},
		{name: "http", engine: `"net/http"`},
		{name: "external", engine: `"example.com/service"`},
		{name: "cgo", engine: `"C"`},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := t.TempDir()
			files := map[string]string{"pkg/engine/engine.go": test.engine}
			if test.backend != "" {
				files["internal/localmedia/backend.go"] = test.backend
			}
			if test.platform != "" {
				files["pkg/engine/bridge_windows.go"] = test.platform
			}
			if test.testImport != "" {
				files["pkg/engine/compatibility_test.go"] = test.testImport
			}
			var names []string
			for name := range files {
				names = append(names, name)
			}
			sort.Strings(names)
			for _, name := range names {
				path := filepath.Join(fixture, name)
				if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("package boundary\nimport "+files[name]+"\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			_, err := engineRuntimeImports(fixture, engineRuntimePackages)
			if (err == nil) != test.valid {
				t.Fatalf("valid=%v, scan error=%v", test.valid, err)
			}
		})
	}
}
