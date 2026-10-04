//go:build !linux

package app

// Other platforms retain logical admission limits. Deployments must apply an
// actual volume quota; Linux also checks available bytes on its filesystem.
func filesystemStorageAvailable(_ string, _ int64) bool { return true }
