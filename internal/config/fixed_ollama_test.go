package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBuiltinOllamaProvider(t *testing.T) {
	providers := BuiltinProviders()
	require.Len(t, providers, 1)

	p := providers[0]
	require.Equal(t, OllamaProviderID, string(p.ID))
	require.Equal(t, "Ollama", p.Name)
	require.Equal(t, OllamaBaseURL, p.APIEndpoint)
	require.Equal(t, []string{OllamaLargeModelID, OllamaSmallModelID}, []string{
		p.Models[0].ID,
		p.Models[1].ID,
	})
	require.Len(t, p.Models, 2)
}

func TestLoadUsesFixedOllamaModels(t *testing.T) {
	workDir, _ := isolateLoadEnv(t)
	store := loadIsolated(t, workDir)
	cfg := store.Config()

	ids := []string{}
	for id := range cfg.Providers.Seq2() {
		ids = append(ids, id)
	}
	require.Equal(t, []string{OllamaProviderID}, ids)

	provider, ok := cfg.Providers.Get(OllamaProviderID)
	require.True(t, ok)
	require.Equal(t, OllamaBaseURL, provider.BaseURL)

	large := cfg.Models[SelectedModelTypeLarge]
	small := cfg.Models[SelectedModelTypeSmall]
	require.Equal(t, OllamaProviderID, large.Provider)
	require.Equal(t, OllamaLargeModelID, large.Model)
	require.Equal(t, OllamaProviderID, small.Provider)
	require.Equal(t, OllamaSmallModelID, small.Model)
}
