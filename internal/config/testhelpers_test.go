package config

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// resetProviderState is retained for tests that used to reset the provider
// catalog's sync.Once state. Crush now uses a static built-in provider, so
// there is nothing to reset.
func resetProviderState() {}

// isolateLoadEnv points config discovery at throwaway directories so the
// host's config and environment cannot leak into a test.
func isolateLoadEnv(t *testing.T) (workDir, dataDir string) {
	t.Helper()

	isolated := t.TempDir()
	t.Setenv("HOME", isolated)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(isolated, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(isolated, ".local", "share"))
	t.Setenv("CRUSH_GLOBAL_CONFIG", filepath.Join(isolated, ".config", "crush"))
	t.Setenv("CRUSH_GLOBAL_DATA", filepath.Join(isolated, ".local", "share", "crush"))
	return t.TempDir(), t.TempDir()
}

// loadIsolated loads a store in an isolated working directory.
func loadIsolated(t *testing.T, workDir string) *ConfigStore {
	t.Helper()
	resetProviderState()
	store, err := Load(t.Context(), workDir, filepath.Join(workDir, ".crush"), false)
	require.NoError(t, err)
	return store
}
