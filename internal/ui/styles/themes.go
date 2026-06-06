package styles

import (
	"image/color"

	"github.com/charmbracelet/x/exp/charmtone"
)

// ThemeForConfig returns the Styles associated with the given configured theme
// name. Unknown or empty theme names fall back to the provider theme.
func ThemeForConfig(themeName, providerID string) Styles {
	switch themeName {
	case "crush":
		return CharmtonePantera()
	case "zephyr":
		return ZephyrBreeze()
	default:
		return ThemeForProvider(providerID)
	}
}

// ThemeForProvider returns the Styles associated with the given provider
// ID. Unknown or empty provider IDs yield the default Charmtone Pantera
// theme.
func ThemeForProvider(providerID string) Styles {
	switch providerID {
	case "hyper":
		return HypercrushObsidiana()
	default:
		return CharmtonePantera()
	}
}

// CharmtonePantera returns the Charmtone dark theme. It's the default style
// for the UI.
func CharmtonePantera() Styles {
	return quickStyle(quickStyleOpts{
		primary:   charmtone.Charple,
		secondary: charmtone.Dolly,
		accent:    charmtone.Bok,
		keyword:   charmtone.Blush,

		fgBase:       charmtone.Sash,
		fgMoreSubtle: charmtone.Squid,
		fgSubtle:     charmtone.Smoke,
		fgMostSubtle: charmtone.Oyster,

		onPrimary: charmtone.Butter,

		bgBase:         charmtone.Pepper,
		bgLeastVisible: charmtone.BBQ,
		bgLessVisible:  charmtone.Char,
		bgMostVisible:  charmtone.Iron,

		separator: charmtone.Char,

		destructive:       charmtone.Coral,
		error:             charmtone.Sriracha,
		warningSubtle:     charmtone.Zest,
		warning:           charmtone.Mustard,
		denied:            charmtone.Tang,
		busy:              charmtone.Citron,
		info:              charmtone.Malibu,
		infoMoreSubtle:    charmtone.Sardine,
		infoMostSubtle:    charmtone.Damson,
		success:           charmtone.Julep,
		successMoreSubtle: charmtone.Bok,
		successMostSubtle: charmtone.Guac,
	})
}

// HypercrushObsidiana returns the Hypercrush dark theme.
func HypercrushObsidiana() Styles {
	return quickStyle(quickStyleOpts{
		primary:   charmtone.Charple,
		secondary: charmtone.Dolly,
		accent:    charmtone.Bok,

		fgBase:       charmtone.Sash,
		fgMoreSubtle: charmtone.Squid,
		fgSubtle:     charmtone.Smoke,
		fgMostSubtle: charmtone.Oyster,

		onPrimary: charmtone.Butter,

		bgBase:         charmtone.Pepper,
		bgLeastVisible: charmtone.BBQ,
		bgLessVisible:  charmtone.Char,
		bgMostVisible:  charmtone.Iron,

		separator: charmtone.Char,

		destructive:       charmtone.Coral,
		error:             charmtone.Sriracha,
		warningSubtle:     charmtone.Zest,
		warning:           charmtone.Mustard,
		denied:            charmtone.Tang,
		busy:              charmtone.Citron,
		info:              charmtone.Malibu,
		infoMoreSubtle:    charmtone.Sardine,
		infoMostSubtle:    charmtone.Damson,
		success:           charmtone.Julep,
		successMoreSubtle: charmtone.Bok,
		successMostSubtle: charmtone.Guac,
	})
}

// ZephyrBreeze returns a cool low-contrast blue-cyan dark theme.
func ZephyrBreeze() Styles {
	return quickStyle(quickStyleOpts{
		primary:   color.RGBA{R: 0x3d, G: 0x7f, B: 0xac, A: 0xff},
		secondary: color.RGBA{R: 0x50, G: 0xa5, B: 0x9d, A: 0xff},
		accent:    color.RGBA{R: 0x67, G: 0x9d, B: 0xd4, A: 0xff},
		keyword:   color.RGBA{R: 0x66, G: 0x8f, B: 0xc7, A: 0xff},

		fgBase:       color.RGBA{R: 0x8f, G: 0xa6, B: 0xb3, A: 0xff},
		fgMoreSubtle: color.RGBA{R: 0x5f, G: 0x78, B: 0x86, A: 0xff},
		fgSubtle:     color.RGBA{R: 0x73, G: 0x8d, B: 0x9a, A: 0xff},
		fgMostSubtle: color.RGBA{R: 0x3f, G: 0x59, B: 0x66, A: 0xff},

		onPrimary: color.RGBA{R: 0x07, G: 0x17, B: 0x22, A: 0xff},

		bgBase:         color.RGBA{R: 0x0b, G: 0x16, B: 0x20, A: 0xff},
		bgLeastVisible: color.RGBA{R: 0x10, G: 0x1f, B: 0x2b, A: 0xff},
		bgLessVisible:  color.RGBA{R: 0x16, G: 0x2a, B: 0x38, A: 0xff},
		bgMostVisible:  color.RGBA{R: 0x20, G: 0x3a, B: 0x4a, A: 0xff},

		separator: color.RGBA{R: 0x22, G: 0x3b, B: 0x49, A: 0xff},

		destructive:       color.RGBA{R: 0xc9, G: 0x62, B: 0x68, A: 0xff},
		error:             color.RGBA{R: 0xd4, G: 0x4f, B: 0x5f, A: 0xff},
		warningSubtle:     color.RGBA{R: 0xa2, G: 0x7b, B: 0x42, A: 0xff},
		warning:           color.RGBA{R: 0xba, G: 0x92, B: 0x4f, A: 0xff},
		denied:            color.RGBA{R: 0xcc, G: 0x76, B: 0x55, A: 0xff},
		busy:              color.RGBA{R: 0x7e, G: 0xa5, B: 0x5a, A: 0xff},
		info:              color.RGBA{R: 0x4e, G: 0x9c, B: 0xbd, A: 0xff},
		infoMoreSubtle:    color.RGBA{R: 0x2f, G: 0x68, B: 0x7c, A: 0xff},
		infoMostSubtle:    color.RGBA{R: 0x1d, G: 0x42, B: 0x51, A: 0xff},
		success:           color.RGBA{R: 0x65, G: 0xa8, B: 0x8d, A: 0xff},
		successMoreSubtle: color.RGBA{R: 0x3d, G: 0x7f, B: 0x6a, A: 0xff},
		successMostSubtle: color.RGBA{R: 0x25, G: 0x53, B: 0x49, A: 0xff},
	})
}
