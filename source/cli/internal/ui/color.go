package ui

import (
	"fmt"
	"io"
	"os"
)

// palette resolves a Status to an ANSI colour, or to nothing at all.
//
// Colour is the only part of the grammar that is ever dropped. Glyphs stay
// under --no-color, because they carry the verdict and colour only reinforces
// it — a reader piping lydite through a pager must still be able to tell a
// pass from a referral.
type palette struct {
	enabled bool
}

// Colours are the design system's, quoted from docs/design/tokens.md rather
// than approximated onto the 16-colour palette: the amber and the red are
// close enough in a default terminal theme that rounding them collapses
// "needs a human" and "you broke something" into the same signal.
var statusRGB = map[Status][3]int{
	StatusPass:       {0x16, 0xC7, 0x9A},
	StatusFail:       {0xF2, 0x42, 0x6E},
	StatusRefer:      {0xF0, 0xB4, 0x29},
	StatusUnmeasured: {0xF0, 0xB4, 0x29},
	StatusDropped:    {0xF0, 0xB4, 0x29},
	StatusNew:        {0x6E, 0x75, 0x94},
	StatusContext:    {0x6E, 0x75, 0x94},
}

func (p palette) paint(s Status, text string) string {
	rgb, ok := statusRGB[s]
	if !p.enabled || !ok || text == "" {
		return text
	}
	return fmt.Sprintf("\x1b[38;2;%d;%d;%dm%s\x1b[0m", rgb[0], rgb[1], rgb[2], text)
}

// ColorEnabled decides whether to emit colour for w, honouring the three
// signals that mean "don't" in ascending order of specificity: the writer is
// not a terminal, NO_COLOR is set (https://no-color.org), or --no-color was
// passed.
//
// The writer check is what keeps CI logs clean without anyone configuring
// anything: a captured pipe is not a character device, so a workflow log
// never fills with escape sequences that its viewer may or may not render.
func ColorEnabled(w io.Writer, noColorFlag bool) bool {
	if noColorFlag {
		return false
	}
	if _, ok := os.LookupEnv("NO_COLOR"); ok {
		return false
	}
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}
