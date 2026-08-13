//go:build !windows

package server

// refreshProcessPathAfterInstall is a no-op outside Windows: Unix package
// managers install into directories that are already on the standard PATH.
func refreshProcessPathAfterInstall() {}
