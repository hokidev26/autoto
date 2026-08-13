//go:build windows

package server

import (
	"os"
	"strings"

	"golang.org/x/sys/windows/registry"
)

// refreshProcessPathAfterInstall merges the machine and user PATH values from
// the registry into this process's PATH. Installers such as winget update the
// registry and broadcast WM_SETTINGCHANGE, but an already-running process keeps
// its stale environment, so a freshly installed git would stay "missing" until
// the app restarts. Existing entries keep their order; new ones are appended.
func refreshProcessPathAfterInstall() {
	registryEntries := append(
		readRegistryPathList(registry.LOCAL_MACHINE, `SYSTEM\CurrentControlSet\Control\Session Manager\Environment`),
		readRegistryPathList(registry.CURRENT_USER, `Environment`)...,
	)
	if len(registryEntries) == 0 {
		return
	}
	current := strings.Split(os.Getenv("PATH"), ";")
	seen := make(map[string]bool, len(current))
	merged := make([]string, 0, len(current)+len(registryEntries))
	appendEntry := func(entry string) {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			return
		}
		key := strings.ToLower(entry)
		if seen[key] {
			return
		}
		seen[key] = true
		merged = append(merged, entry)
	}
	for _, entry := range current {
		appendEntry(entry)
	}
	for _, entry := range registryEntries {
		appendEntry(entry)
	}
	os.Setenv("PATH", strings.Join(merged, ";"))
}

func readRegistryPathList(root registry.Key, keyPath string) []string {
	key, err := registry.OpenKey(root, keyPath, registry.QUERY_VALUE)
	if err != nil {
		return nil
	}
	defer key.Close()
	value, valueType, err := key.GetStringValue("Path")
	if err != nil {
		return nil
	}
	if valueType == registry.EXPAND_SZ {
		if expanded, expandErr := registry.ExpandString(value); expandErr == nil {
			value = expanded
		}
	}
	return strings.Split(value, ";")
}
