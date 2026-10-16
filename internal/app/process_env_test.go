package app

import (
	"context"
	"encoding/json"
	"os"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestProcessEnvironmentSanitizesRealChild(t *testing.T) {
	// These settings must not reach the child, including allocator values that
	// conflict with the trusted cap derived from the running Go process.
	for name, value := range map[string]string{
		"MALLOC_ARENA_MAX":       "987654",
		"MALLOC_TRIM_THRESHOLD_": "987654",
		"MALLOC_MMAP_THRESHOLD_": "987654",
		"LD_PRELOAD":             "cutmy-fixture-must-never-load.so",
		"LD_LIBRARY_PATH":        "/cutmy-fixture-loader-override",
		"GLIBC_TUNABLES":         "glibc.malloc.arena_max=987654",
		"GOGC":                   "off",
		"CUTMY_PRIVATE_FIXTURE":  "not-a-real-credential",
	} {
		t.Setenv(name, value)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	data, err := runCommand(ctx, executable, "-test.run=^TestProcessEnvironmentFixture$", "--", "cutmy-process-env-fixture")
	if err != nil {
		t.Fatalf("environment fixture failed: %v", err)
	}
	var entries []string
	if err := json.Unmarshal(data, &entries); err != nil {
		t.Fatalf("environment fixture returned invalid JSON: %v", err)
	}
	want := map[string]string{"PATH": os.Getenv("PATH"), "HOME": "/nonexistent", "LANG": "C.UTF-8", "LC_ALL": "C.UTF-8"}
	if runtime.GOOS == "linux" {
		budget := runtime.GOMAXPROCS(0)
		if budget < 2 {
			budget = 2
		}
		if budget > 32 {
			budget = 32
		}
		want["MALLOC_ARENA_MAX"] = strconv.Itoa(budget)
	}
	seen := make(map[string]bool)
	for _, entry := range entries {
		name, value, ok := strings.Cut(entry, "=")
		if !ok {
			t.Fatal("child returned a malformed environment entry")
		}
		// os/exec supplies SystemRoot on Windows even with an explicit Env.
		if runtime.GOOS == "windows" && strings.EqualFold(name, "SystemRoot") {
			if value != os.Getenv("SystemRoot") {
				t.Fatal("Windows child lost its required SystemRoot")
			}
			continue
		}
		expected, allowed := want[name]
		if !allowed || seen[name] {
			t.Fatalf("unexpected or duplicate child environment key %q", name)
		}
		if value != expected {
			t.Fatalf("child environment value differs for %q", name)
		}
		seen[name] = true
	}
	for name := range want {
		if !seen[name] {
			t.Fatalf("child environment key %q is missing", name)
		}
	}
}

func TestProcessEnvironmentArenaBoundsAndPlatforms(t *testing.T) {
	for _, tc := range []struct {
		maxProcs int
		want     string
	}{
		{-1, "2"}, {0, "2"}, {1, "2"}, {2, "2"}, {8, "8"}, {31, "31"}, {32, "32"}, {1024, "32"},
	} {
		env := mediaProcessEnvironmentFor("linux", tc.maxProcs)
		if len(env) != 5 || env[4] != "MALLOC_ARENA_MAX="+tc.want {
			t.Fatalf("CPU budget %d did not produce the expected bounded arena cap", tc.maxProcs)
		}
	}
	for _, platform := range []string{"windows", "darwin", "freebsd"} {
		env := mediaProcessEnvironmentFor(platform, 1024)
		if len(env) != 4 || !strings.HasPrefix(env[0], "PATH=") || env[1] != "HOME=/nonexistent" || env[2] != "LANG=C.UTF-8" || env[3] != "LC_ALL=C.UTF-8" {
			t.Fatalf("platform %q changed the original four-entry environment", platform)
		}
	}
}

func TestProcessEnvironmentFixture(t *testing.T) {
	if len(os.Args) == 0 || os.Args[len(os.Args)-1] != "cutmy-process-env-fixture" {
		return
	}
	if json.NewEncoder(os.Stdout).Encode(os.Environ()) != nil {
		os.Exit(3)
	}
	os.Exit(0)
}
