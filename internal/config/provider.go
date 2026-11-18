package config

import (
	"context"

	"charm.land/catwalk/v3/pkg/catwalk"
)

// Crush is fixed to a single local Ollama provider. There is no external
// provider catalog, no credentials and no model auto-discovery: the two
// models below are the only models Crush can use.
const (
	OllamaProviderID   = "ollama"
	OllamaBaseURL      = "http://localhost:11434/v1"
	OllamaLargeModelID = "qwen3.8:27b-mlx"
	OllamaSmallModelID = "qwen3.6:35b-mlx"
)

// BuiltinProviders returns the one and only provider catalog Crush uses.
func BuiltinProviders() []catwalk.Provider {
	return []catwalk.Provider{builtinOllamaProvider()}
}

func builtinOllamaProvider() catwalk.Provider {
	return catwalk.Provider{
		ID:                  OllamaProviderID,
		Name:                "Ollama",
		APIEndpoint:         OllamaBaseURL,
		Type:                catwalk.TypeCompletions,
		DefaultLargeModelID: OllamaLargeModelID,
		DefaultSmallModelID: OllamaSmallModelID,
		Models: []catwalk.Model{
			{
				ID:               OllamaLargeModelID,
				Name:             OllamaLargeModelID,
				ContextWindow:    32768,
				DefaultMaxTokens: 4096,
			},
			{
				ID:               OllamaSmallModelID,
				Name:             OllamaSmallModelID,
				ContextWindow:    32768,
				DefaultMaxTokens: 4096,
			},
		},
	}
}

// Providers returns the fixed built-in provider catalog. The config argument
// is kept for call-site compatibility; user provider configuration is ignored.
func Providers(cfg *Config) ([]catwalk.Provider, error) {
	return BuiltinProviders(), nil
}

// LoadProviders is Providers with a context, kept for call-site compatibility.
// It always returns the built-in catalog without touching the network.
func LoadProviders(ctx context.Context, cfg *Config) ([]catwalk.Provider, error) {
	return BuiltinProviders(), nil
}
