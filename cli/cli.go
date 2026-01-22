package cli

import (
	"fmt"
	"io"
	"os"

	"github.com/charmbracelet/lipgloss"
)

// Styles for the CLI output
var (
	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("205")).
			MarginBottom(1)

	subtitleStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("241"))

	infoStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("86"))

	successStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("82"))

	warningStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("214"))

	errorStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("196"))

	bulletStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("205")).
			SetString("→")

	dimStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("241"))
)

// Log prints an informational message with a prefix to stderr
func Log(format string, args ...any) {
	LogTo(os.Stderr, format, args...)
}

// LogTo prints an informational message with a prefix to the given writer
func LogTo(w io.Writer, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	fmt.Fprintln(w, infoStyle.Render("==> "+msg))
}

// LogSuccess prints a success message to stderr
func LogSuccess(format string, args ...any) {
	LogSuccessTo(os.Stderr, format, args...)
}

// LogSuccessTo prints a success message to the given writer
func LogSuccessTo(w io.Writer, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	fmt.Fprintln(w, successStyle.Render("✓ "+msg))
}

// LogWarning prints a warning message to stderr
func LogWarning(format string, args ...any) {
	LogWarningTo(os.Stderr, format, args...)
}

// LogWarningTo prints a warning message to the given writer
func LogWarningTo(w io.Writer, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	fmt.Fprintln(w, warningStyle.Render("! "+msg))
}

// LogError prints an error message to stderr
func LogError(format string, args ...any) {
	LogErrorTo(os.Stderr, format, args...)
}

// LogErrorTo prints an error message to the given writer
func LogErrorTo(w io.Writer, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	fmt.Fprintln(w, errorStyle.Render("✗ "+msg))
}

// LogBullet prints a bulleted list item to stderr
func LogBullet(format string, args ...any) {
	LogBulletTo(os.Stderr, format, args...)
}

// LogBulletTo prints a bulleted list item to the given writer
func LogBulletTo(w io.Writer, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	fmt.Fprintln(w, "  "+bulletStyle.Render()+" "+msg)
}

// LogDim prints a dimmed message to stderr
func LogDim(format string, args ...any) {
	LogDimTo(os.Stderr, format, args...)
}

// LogDimTo prints a dimmed message to the given writer
func LogDimTo(w io.Writer, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	fmt.Fprintln(w, dimStyle.Render("  "+msg))
}

// Title returns a styled title
func Title(s string) string {
	return titleStyle.Render(s)
}

// Subtitle returns a styled subtitle
func Subtitle(s string) string {
	return subtitleStyle.Render(s)
}
