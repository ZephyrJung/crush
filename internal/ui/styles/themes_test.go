package styles

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestThemeForConfigUsesConfiguredTheme(t *testing.T) {
	t.Parallel()

	configured := ThemeForConfig("zephyr", "hyper")
	zephyr := ZephyrBreeze()
	hyper := HypercrushObsidiana()

	require.Equal(t, hex(zephyr.Background), hex(configured.Background))
	require.Equal(t, hex(zephyr.WorkingGradFromColor), hex(configured.WorkingGradFromColor))
	require.NotEqual(t, hex(hyper.Background), hex(configured.Background))
}

func TestThemeForConfigFallsBackToProviderTheme(t *testing.T) {
	t.Parallel()

	configured := ThemeForConfig("", "hyper")
	hyper := HypercrushObsidiana()

	require.Equal(t, hex(hyper.Background), hex(configured.Background))
	require.Equal(t, hex(hyper.WorkingGradFromColor), hex(configured.WorkingGradFromColor))
}
