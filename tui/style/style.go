package style

import (
	"fmt"
	"image/color"
	"math"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

var (
	// Base colors - Stunning gradient-inspired theme
	ColorBlueSoft      = lipgloss.Color("#82AAFF") // Soft periwinkle blue (from your reference)
	ColorBlueMuted     = lipgloss.Color("#A5B7E8") // Muted lavender-blue for secondary text
	ColorYellowWarm    = lipgloss.Color("#F0D278") // Warm golden highlight (from your reference)
	ColorYellowSoft    = lipgloss.Color("#F5E6A3") // Lighter golden yellow for emphasis
	ColorGreenSoft     = lipgloss.Color("#98FB98") // Keep original - DO NOT MODIFY
	ColorError         = lipgloss.Color("#FF6B6B") // Keep original - DO NOT MODIFY
	ColorBlueVeryLight = lipgloss.Color("#E8F0FF") // Soft white with blue tint for readability
	ColorBlueGrayMuted = lipgloss.Color("#6B7A9E") // Muted blue-gray for subtle elements
	ColorPurpleSoft    = lipgloss.Color("#B496FF") // Beautiful lavender purple (from your reference)
	ColorPurpleVibrant = lipgloss.Color("#9F7AEA") // Rich purple for titles
	ColorCyanSoft      = lipgloss.Color("#7DD3FC") // Sky blue for key bindings

	// lipgloss empty new style
	NewStyle = lipgloss.NewStyle()

	// list component style
	ItemStyle         = NewStyle
	SelectedItemStyle = NewStyle.Foreground(ColorBlueSoft)
	PaginationStyle   = NewStyle

	// Styles
	TitleStyle                                     = NewStyle.Bold(true)
	TitleCurrentComponentStyle                     = NewStyle.Foreground(ColorPurpleVibrant)
	TitleNonCurrentComponentStyle                  = NewStyle.Foreground(ColorPurpleSoft).Faint(true)
	PromptTitleStyle                               = NewStyle.Foreground(ColorPurpleSoft).Bold(true)
	BottomKeyBindingStyle                          = NewStyle.Foreground(ColorCyanSoft)
	PanelBorderStyle                               = NewStyle.Border(lipgloss.RoundedBorder()).Padding(0).Margin(0).BorderForeground(ColorBlueGrayMuted)
	SelectedBorderStyle                            = NewStyle.Border(lipgloss.DoubleBorder()).Padding(0).Margin(0).BorderForeground(ColorBlueVeryLight)
	PopUpBorderStyle                               = NewStyle.Border(lipgloss.ThickBorder()).Padding(0).Margin(0).BorderForeground(ColorBlueVeryLight)
	SpinnerStyle                                   = NewStyle.Foreground(ColorBlueSoft)
	BranchInvalidWarningStyle                      = NewStyle.Foreground(ColorBlueMuted).Faint(true)
	KeyBindingPopUpStyle                           = NewStyle.Border(lipgloss.ThickBorder()).Padding(0).Margin(0).BorderForeground(ColorPurpleSoft)
	KeyBindingAndFeatureInstructionsTitleLineStyle = NewStyle.Foreground(ColorPurpleSoft)
	KeyBindingKeyMappingInfoStyle                  = NewStyle.Foreground(ColorCyanSoft)
	FeatureInfoLineStyle                           = NewStyle.Foreground(ColorCyanSoft)
	KeyBindingAndFeatureInstructionsWarnStyle      = NewStyle.Foreground(ColorYellowWarm)
	DiffOldLineStyle                               = NewStyle.Foreground(ColorError)
	DiffNewLineStyle                               = NewStyle.Foreground(ColorGreenSoft)
	StagedFileStyle                                = NewStyle.Foreground(ColorGreenSoft)
	UnstagedFileStyle                              = NewStyle.Foreground(ColorError)
	LocalStatusStyle                               = NewStyle.Foreground(ColorGreenSoft)
	RemoteStatusStyle                              = NewStyle.Foreground(ColorError)
	StashIdStyle                                   = NewStyle.Foreground(ColorYellowWarm)
	StashMessageStyle                              = NewStyle.Foreground(ColorYellowSoft)
	StashFilePathStyle                             = NewStyle.Foreground(ColorCyanSoft)
	ErrorStyle                                     = NewStyle.Foreground(ColorError)
)

var Palette = []color.Color{
	// --- BLUE LAYER ---
	lipgloss.Color("#0087ff"), // Deep Sky Blue (Deep, saturated base)
	lipgloss.Color("#87d7ff"), // Light Sky Blue (Bright & airy, clear jump from 1)

	// --- PURPLE/SLATE LAYER ---
	lipgloss.Color("#5f5fff"), // Royal Purple-Blue (Much darker/richer than the sky blue)
	lipgloss.Color("#afafff"), // Lavender Mist (Very light, creates a "highlight" layer)

	// --- VIBRANT PURPLE LAYER ---
	lipgloss.Color("#8700ff"), // Vivid Violet (Strong hue shift)
	lipgloss.Color("#af5fff"), // Medium Purple (Softer, but clearly different)

	// --- MAGENTA LAYER ---
	lipgloss.Color("#d75fff"), // Medium Orchid (The transition to pink)
	lipgloss.Color("#ff00ff"), // Pure Magenta (High saturation "pop" color)

	// --- PINK/ROSE LAYER ---
	lipgloss.Color("#ff5faf"), // Deep Rose (A darker, warm pink)
	lipgloss.Color("#ff87ff"), // Light Orchid (Bright and glowing)

	// --- HIGHLIGHT LAYER ---
	lipgloss.Color("#ffafff"), // Hot Pink (Very bright)
	lipgloss.Color("#ffd7ff"), // Pale Pink (Almost white, the final highlight)
}

// ------------------------------------
//
//	Return a color from Palette by index, wrapping with modulo. Returns
//	lipgloss.NoColor{} for negative IDs.
//
// ------------------------------------
func GetColor(colorID int) color.Color {
	if colorID < 0 {
		return lipgloss.NoColor{}
	}
	return Palette[colorID%len(Palette)]
}

// ------------------------------------
//
//	Apply an HSL gradient to a slice of strings. Each line gets a unique hue
//	stepping from startHue by hueStep degrees, producing a smooth color sweep
//	across the output.
//
// ------------------------------------
func GradientLines(lines []string) []string {
	colored := make([]string, len(lines))

	// // Tunable values
	// startHue := 200.0 // degrees
	// hueStep := 12.0   // per line
	// sat := 0.70       // 0–1
	// light := 0.65     // 0–1

	// Enhanced gradient values for stunning visual effect
	startHue := 220.0 // degrees - start at beautiful blue-purple
	hueStep := 8.0    // per line - smoother transitions
	sat := 0.75       // 0–1 - increased saturation for vibrancy
	light := 0.68     // 0–1 - optimized brightness for both light/dark terminals

	// inline HSL→RGB→HEX conversion
	hslToHex := func(h, s, l float64) string {
		c := (1 - math.Abs(2*l-1)) * s
		x := c * (1 - math.Abs(math.Mod(h/60, 2)-1))
		m := l - c/2

		var r, g, b float64
		switch {
		case h < 60:
			r, g, b = c, x, 0
		case h < 120:
			r, g, b = x, c, 0
		case h < 180:
			r, g, b = 0, c, x
		case h < 240:
			r, g, b = 0, x, c
		case h < 300:
			r, g, b = x, 0, c
		default:
			r, g, b = c, 0, x
		}

		R := int((r + m) * 255)
		G := int((g + m) * 255)
		B := int((b + m) * 255)

		return fmt.Sprintf("#%02x%02x%02x", R, G, B)
	}

	for i, line := range lines {
		h := math.Mod(startHue+float64(i)*hueStep, 360)
		hex := hslToHex(h, sat, light)

		style := lipgloss.NewStyle().
			Foreground(lipgloss.Color(hex))

		colored[i] = style.Render(line)
	}
	return colored
}

const (
	// The space before the block and its two brackets.
	commitRefsBlockOverhead = 3
	// Below this the block would be little more than an ellipsis, so the refs are
	// dropped instead.
	commitRefsMinBlockWidth = 8
	// PopUpBorderStyle draws a thick border, which takes a column on each side of
	// whatever width the popup is given.
	popUpBorderWidth = 2

	// PopUpValueMarker stands in for a value while its line is measured. It is a
	// NUL so it cannot collide with anything a locale actually writes.
	PopUpValueMarker = "\x00"
)

// ------------------------------------
//
//	Report how much of a popup's width its content actually gets. Budgeting
//	against the width handed to PopUpBorderStyle would overrun by the border and
//	wrap the line the budget was meant to keep on one row
//
// ------------------------------------
func PopUpContentWidth(popUpWidth int) int {
	return max(popUpWidth-popUpBorderWidth, 0)
}

// ------------------------------------
//
//	Report how much room a substituted value has on its own line. A localized
//	template puts a label in front of the value, and how wide that label is
//	differs by locale, so a budget taken from the popup width alone overruns by
//	whatever the label happens to occupy. Pass the template already formatted with
//	a marker in the value's place
//
// ------------------------------------
func PopUpValueBudget(popUpWidth int, formattedWithMarker string, marker string) int {
	for _, line := range strings.Split(formattedWithMarker, "\n") {
		if strings.Contains(line, marker) {
			label := strings.Replace(line, marker, "", 1)
			return max(PopUpContentWidth(popUpWidth)-lipgloss.Width(label), 0)
		}
	}

	return PopUpContentWidth(popUpWidth)
}

// ------------------------------------
//
//	Render a commit's hash with the refs pointing at it, so a popup that acts on
//	a commit names it the way the commit log row does. The hash colour is the
//	caller's, because each popup already had one and a commit carrying no refs has
//	to render exactly as its bare hash did before
//
// ------------------------------------
func RenderCommitHashWithRefs(commitHash string, refs string, hashColor color.Color, availableWidth int) string {
	return renderCommitIdentity(commitHash, refs, hashColor, availableWidth, true)
}

// ------------------------------------
//
//	Render a commit's hash with its refs for a template that already brackets the
//	whole identity. Bracketing the refs again would nest one pair inside another,
//	and dropping the template's pair instead would change how a commit with no
//	refs renders
//
// ------------------------------------
func RenderCommitHashWithBareRefs(commitHash string, refs string, hashColor color.Color, availableWidth int) string {
	return renderCommitIdentity(commitHash, refs, hashColor, availableWidth, false)
}

// ------------------------------------
//
//	Compose the hash and the refs beside it, bounded to the width the line has
//
// ------------------------------------
func renderCommitIdentity(commitHash string, refs string, hashColor color.Color, availableWidth int, bracketRefs bool) string {
	rendered := NewStyle.Foreground(hashColor).Render(commitHash)
	if refs == "" {
		return rendered
	}

	// The decoration list has no bound: every branch, remote-tracking branch and
	// tag pointing at the commit appears in it. These popups have no height limit,
	// so an untruncated list can push a confirmation for a destructive action off
	// the screen.
	overhead := 1
	if bracketRefs {
		overhead = commitRefsBlockOverhead
	}

	budget := availableWidth - lipgloss.Width(rendered) - overhead
	if budget < commitRefsMinBlockWidth {
		return rendered
	}

	block := ansi.Truncate(refs, budget, "...")
	if bracketRefs {
		block = "[" + block + "]"
	}

	return rendered + " " + NewStyle.Foreground(ColorPurpleSoft).Bold(true).Render(block)
}
