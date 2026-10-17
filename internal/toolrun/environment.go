package toolrun

import (
	"os"
	"runtime"
	"strconv"
)

func Environment() []string {
	return EnvironmentFor(runtime.GOOS, runtime.GOMAXPROCS(0))
}

func EnvironmentFor(platform string, maxProcs int) []string {
	// Keep the subprocess boundary independent of service credentials, loader
	// overrides and caller-supplied allocator settings.
	env := []string{"PATH=" + os.Getenv("PATH"), "HOME=/nonexistent", "LANG=C.UTF-8", "LC_ALL=C.UTF-8"}
	if platform == "linux" {
		// Bound glibc arena proliferation in threaded native tools using the
		// process's configured CPU budget. Other allocators may ignore this;
		// it does not change codec settings or impose a process memory limit.
		arenas := min(32, max(2, maxProcs))
		env = append(env, "MALLOC_ARENA_MAX="+strconv.Itoa(arenas))
	}
	return env
}
