package tui

import (
	"image/color"
	"math"
	"time"

	"charm.land/lipgloss/v2"
)

// Palette follows the Codex style guide: default fg for most text, dim for
// secondary, bold for headers, cyan for accents/selection, green for success
// and additions, red for errors and deletions, magenta for the agent brand.
// Only ANSI colors are used for text so gnome-terminal's palette (Tango,
// Solarized, ...) is respected; RGB is used only for tinted backgrounds that
// are derived from the terminal's real background (queried via OSC 11).
type Styles struct {
	Plain   lipgloss.Style
	Dim     lipgloss.Style
	Bold    lipgloss.Style
	Italic  lipgloss.Style
	Accent  lipgloss.Style // cyan
	AccentB lipgloss.Style // cyan bold
	Green   lipgloss.Style
	GreenB  lipgloss.Style
	Red     lipgloss.Style
	RedB    lipgloss.Style
	Magenta lipgloss.Style
	Code    lipgloss.Style
	Link    lipgloss.Style

	// UserBg is the band behind submitted/editable prompts. Zero when the
	// terminal background is unknown.
	UserBg    lipgloss.Style
	hasBg     bool
	bg, fg    [3]uint8
	trueColor bool
}

func newStyles() *Styles {
	n := lipgloss.NewStyle()
	s := &Styles{
		Plain:   n,
		Dim:     n.Faint(true),
		Bold:    n.Bold(true),
		Italic:  n.Italic(true),
		Accent:  n.Foreground(lipgloss.Cyan),
		AccentB: n.Foreground(lipgloss.Cyan).Bold(true),
		Green:   n.Foreground(lipgloss.Green),
		GreenB:  n.Foreground(lipgloss.Green).Bold(true),
		Red:     n.Foreground(lipgloss.Red),
		RedB:    n.Foreground(lipgloss.Red).Bold(true),
		Magenta: n.Foreground(lipgloss.Magenta),
		Code:    n.Foreground(lipgloss.Cyan),
		Link:    n.Foreground(lipgloss.Cyan).Underline(true),
		UserBg:  n,
		fg:      [3]uint8{204, 204, 204},
		bg:      [3]uint8{0, 0, 0},
	}
	return s
}

func rgb(c color.Color) [3]uint8 {
	r, g, b, _ := c.RGBA()
	return [3]uint8{uint8(r >> 8), uint8(g >> 8), uint8(b >> 8)}
}

func isLight(c [3]uint8) bool {
	y := 0.299*float64(c[0]) + 0.587*float64(c[1]) + 0.114*float64(c[2])
	return y > 128
}

func blend(top, bottom [3]uint8, alpha float64) [3]uint8 {
	var out [3]uint8
	for i := range 3 {
		out[i] = uint8(math.Round(float64(top[i])*alpha + float64(bottom[i])*(1-alpha)))
	}
	return out
}

func toColor(c [3]uint8) color.Color { return color.RGBA{c[0], c[1], c[2], 255} }

// SetBackground is called with the terminal's reported background color.
// Like Codex, prompts get a fill 12% toward white on dark themes and 4%
// toward black on light themes.
func (s *Styles) SetBackground(c color.Color) {
	s.bg = rgb(c)
	s.hasBg = true
	var top [3]uint8
	alpha := 0.12
	if isLight(s.bg) {
		top, alpha = [3]uint8{0, 0, 0}, 0.04
	} else {
		top = [3]uint8{255, 255, 255}
	}
	s.UserBg = lipgloss.NewStyle().Background(toColor(blend(top, s.bg, alpha)))
}

func (s *Styles) SetForeground(c color.Color) { s.fg = rgb(c) }

var processStart = time.Now()

// Shimmer renders text with a moving highlight band (Codex "Working" header).
// Falls back to dim/plain/bold steps when the terminal bg is unknown.
func (s *Styles) Shimmer(text string) Line {
	runes := []rune(text)
	if len(runes) == 0 {
		return nil
	}
	const padding = 10
	period := len(runes) + padding*2
	sweep := 2.0
	elapsed := time.Since(processStart).Seconds()
	pos := int(math.Mod(elapsed, sweep) / sweep * float64(period))
	const half = 5.0
	out := make(Line, 0, len(runes))
	for i, r := range runes {
		dist := math.Abs(float64(i + padding - pos))
		t := 0.0
		if dist <= half {
			t = 0.5 * (1 + math.Cos(math.Pi*dist/half))
		}
		var st lipgloss.Style
		if s.hasBg && s.trueColor {
			base := blend(s.fg, s.bg, 0.55)
			c := blend(s.fg, base, t)
			st = lipgloss.NewStyle().Foreground(toColor(c)).Bold(true)
		} else {
			switch {
			case t < 0.2:
				st = s.Dim
			case t < 0.6:
				st = s.Plain
			default:
				st = s.Bold
			}
		}
		out = append(out, Span{string(r), st})
	}
	return out
}

// spinner frames used for the terminal title while working.
var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

func spinnerFrame() string {
	i := int(time.Since(processStart)/(80*time.Millisecond)) % len(spinnerFrames)
	return spinnerFrames[i]
}
