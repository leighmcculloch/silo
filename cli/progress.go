package cli

import (
	"fmt"
	"io"
	"regexp"
	"strings"
	"sync"

	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-isatty"
	"golang.org/x/sys/unix"
)

// ansiRegex matches ANSI escape sequences
var ansiRegex = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)

// Progress represents a slim progress bar that displays sections
type Progress struct {
	mu       sync.Mutex
	w        io.Writer
	sections []string
	current  int
	detail   string
	width    int
	isTTY    bool
	rendered bool
}

// NewProgress creates a new progress bar with the given sections
func NewProgress(w io.Writer, sections []string) *Progress {
	// Check if writer is a TTY
	isTTY := false
	if f, ok := w.(interface{ Fd() uintptr }); ok {
		isTTY = isatty.IsTerminal(f.Fd())
	}

	width := 80 // default width
	if isTTY {
		if ws, err := unix.IoctlGetWinsize(int(w.(interface{ Fd() uintptr }).Fd()), unix.TIOCGWINSZ); err == nil && ws.Col > 0 {
			width = int(ws.Col)
		}
	}

	return &Progress{
		w:        w,
		sections: sections,
		current:  0,
		width:    width,
		isTTY:    isTTY,
	}
}

// Start begins the progress display
func (p *Progress) Start() {
	if !p.isTTY || len(p.sections) == 0 {
		return
	}
	p.render()
}

// SetSection updates the current section by name
func (p *Progress) SetSection(name string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	for i, s := range p.sections {
		if s == name {
			p.current = i
			break
		}
	}
	p.detail = ""

	if p.isTTY {
		p.render()
	}
}

// SetDetail updates the detail text shown after the section name
func (p *Progress) SetDetail(detail string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	// Strip ANSI escape codes
	detail = ansiRegex.ReplaceAllString(detail, "")

	// Clean up the detail - take last non-empty line, strip whitespace
	detail = strings.TrimSpace(detail)
	if idx := strings.LastIndex(detail, "\n"); idx >= 0 {
		detail = strings.TrimSpace(detail[idx+1:])
	}

	// Only update if we have actual content (don't clear with empty strings)
	if detail == "" {
		return
	}
	p.detail = detail

	if p.isTTY {
		p.render()
	}
}

// Advance moves to the next section
func (p *Progress) Advance() {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.current < len(p.sections)-1 {
		p.current++
	}
	p.detail = ""

	if p.isTTY {
		p.render()
	}
}

// Complete finishes the progress bar
func (p *Progress) Complete() {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.current = len(p.sections)

	if p.isTTY && p.rendered {
		p.clear()
	}
}

// render draws the progress bar
func (p *Progress) render() {
	if len(p.sections) == 0 {
		return
	}

	// Clear previous line if we've rendered before
	if p.rendered {
		p.clear()
	}
	p.rendered = true

	// Calculate progress
	progress := float64(p.current) / float64(len(p.sections))

	// Build the progress bar
	// Format: [████████░░░░░░░░] Section name: detail
	barWidth := 20
	filled := int(progress * float64(barWidth))
	if filled > barWidth {
		filled = barWidth
	}

	// Build bar characters
	filledStr := strings.Repeat("█", filled)
	emptyStr := strings.Repeat("░", barWidth-filled)

	// Style the bar
	barStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("86"))
	emptyStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	sectionStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("205"))
	detailStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("241"))

	// Get current section name
	sectionName := ""
	if p.current < len(p.sections) {
		sectionName = p.sections[p.current]
	}

	// Build the status text (section + detail)
	statusText := sectionName
	if p.detail != "" {
		statusText = sectionName + ": " + p.detail
	}

	// Calculate available width for status text
	// Format: [bar] status...
	prefixLen := barWidth + 3 // "[" + bar + "] "
	maxStatusLen := p.width - prefixLen - 1
	if maxStatusLen < 10 {
		maxStatusLen = 10
	}

	// Truncate status text if needed
	displayStatus := statusText
	if len(displayStatus) > maxStatusLen {
		displayStatus = displayStatus[:maxStatusLen-3] + "..."
	}

	// Style the display - section name in pink, detail in dim
	var styledStatus string
	if p.detail != "" && len(displayStatus) > len(sectionName)+2 {
		// We have room for at least part of the detail
		styledStatus = sectionStyle.Render(sectionName+": ") + detailStyle.Render(displayStatus[len(sectionName)+2:])
	} else {
		styledStatus = sectionStyle.Render(displayStatus)
	}

	// Build and print the line
	line := fmt.Sprintf("[%s%s] %s",
		barStyle.Render(filledStr),
		emptyStyle.Render(emptyStr),
		styledStatus,
	)

	fmt.Fprint(p.w, line)
}

// clear removes the current progress line
func (p *Progress) clear() {
	// Move to beginning of line and clear it
	fmt.Fprint(p.w, "\r\033[K")
}
