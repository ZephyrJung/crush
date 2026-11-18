package agent

import (
	"testing"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/stretchr/testify/require"
)

func TestTurnKeepsItsModelAcrossARebuild(t *testing.T) {
	t.Parallel()

	model := func(provider, id string) Model {
		return Model{
			Model:    &finishStreamModel{text: provider + "/" + id},
			ModelCfg: config.SelectedModel{Provider: provider, Model: id},
		}
	}
	started := model("openai", "gpt")
	current := started
	a := &sessionAgent{models: func() ModelSet { return ModelSet{Large: current} }}

	rebuilt := model("openai", "gpt")
	current = rebuilt
	require.Same(t, rebuilt.Model, a.currentBuildOf(started), "a refreshed build of the same model is used")

	current = model("anthropic", "claude")
	require.Same(t, started.Model, a.currentBuildOf(started), "a model switch waits for the next turn")
}
