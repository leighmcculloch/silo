package cli

import (
	"fmt"
	"io"

	"github.com/charmbracelet/lipgloss"
)

// Styles for the CLI output
var (
	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("205")).
			MarginBottom(1)

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

// LogTo prints an informational message with a prefix to the given writer
func LogTo(w io.Writer, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	fmt.Fprintln(w, infoStyle.Render("==> "+msg))
}

// LogSuccessTo prints a success message to the given writer
func LogSuccessTo(w io.Writer, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	fmt.Fprintln(w, successStyle.Render("✓ "+msg))
}

// LogWarningTo prints a warning message to the given writer
func LogWarningTo(w io.Writer, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	fmt.Fprintln(w, warningStyle.Render("! "+msg))
}

// LogErrorTo prints an error message to the given writer
func LogErrorTo(w io.Writer, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	fmt.Fprintln(w, errorStyle.Render("✗ "+msg))
}

// LogBulletTo prints a bulleted list item to the given writer
func LogBulletTo(w io.Writer, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	fmt.Fprintln(w, "  "+bulletStyle.Render()+" "+msg)
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
