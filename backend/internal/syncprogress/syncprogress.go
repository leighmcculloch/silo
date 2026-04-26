// Package syncprogress renders a single-line updating progress display
// for file sync operations across remote backends.
package syncprogress

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-isatty"
)

// Progress renders a single-line updating progress display for file sync.
// On TTY: updates in place with \r. On non-TTY: prints each phase on a new line.
type Progress struct {
	mu       sync.Mutex
	w        io.Writer
	isTTY    bool
	rendered bool

	phase   string // current phase description
	current int    // completed items
	total   int    // total items
	detail  string // extra detail (e.g. path being synced)
}

func New(w io.Writer) *Progress {
	isTTY := false
	if f, ok := w.(interface{ Fd() uintptr }); ok {
		isTTY = isatty.IsTerminal(f.Fd())
	}
	return &Progress{w: w, isTTY: isTTY}
}

// SetPhase updates the phase with no counter.
func (p *Progress) SetPhase(phase string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.phase = phase
	p.current = 0
	p.total = 0
	p.detail = ""
	p.render()
}

// SetProgress updates the phase with a counter and optional detail.
func (p *Progress) SetProgress(phase string, current, total int, detail string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.phase = phase
	p.current = current
	p.total = total
	p.detail = detail
	p.render()
}

// Finish clears the progress line on TTY.
func (p *Progress) Finish() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.isTTY && p.rendered {
		fmt.Fprint(p.w, "\r\033[K")
		p.rendered = false
	}
}

func (p *Progress) render() {
	if p.isTTY {
		// Update in place
		if p.rendered {
			fmt.Fprint(p.w, "\r\033[K")
		}
		p.rendered = true

		arrow := lipgloss.NewStyle().Foreground(lipgloss.Color("241")).Render("  → ")
		var line string
		if p.total > 0 {
			bar := p.renderBar()
			phase := lipgloss.NewStyle().Foreground(lipgloss.Color("205")).Render(p.phase)
			if p.detail != "" {
				det := lipgloss.NewStyle().Foreground(lipgloss.Color("241")).Render(p.detail)
				line = fmt.Sprintf("%s%s %s %s", arrow, bar, phase, det)
			} else {
				line = fmt.Sprintf("%s%s %s", arrow, bar, phase)
			}
		} else {
			phase := lipgloss.NewStyle().Foreground(lipgloss.Color("205")).Render(p.phase)
			line = fmt.Sprintf("%s%s", arrow, phase)
		}
		fmt.Fprint(p.w, line)
	} else {
		// Non-TTY: print each update on a new line
		if p.total > 0 {
			if p.detail != "" {
				fmt.Fprintf(p.w, "    [%d/%d] %s %s\n", p.current, p.total, p.phase, p.detail)
			} else {
				fmt.Fprintf(p.w, "    [%d/%d] %s\n", p.current, p.total, p.phase)
			}
		} else {
			fmt.Fprintf(p.w, "    %s\n", p.phase)
		}
	}
}

func (p *Progress) renderBar() string {
	barWidth := 15
	progress := float64(p.current) / float64(p.total)
	filled := min(int(progress*float64(barWidth)), barWidth)

	filledStr := strings.Repeat("█", filled)
	emptyStr := strings.Repeat("░", barWidth-filled)

	filledStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("86"))
	emptyStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("241"))

	return fmt.Sprintf("[%s%s]", filledStyle.Render(filledStr), emptyStyle.Render(emptyStr))
}

// TildePath shortens a home-dir prefixed path to ~/...
func TildePath(path string) string {
	home, _ := os.UserHomeDir()
	if home != "" && strings.HasPrefix(path, home) {
		return "~" + path[len(home):]
	}
	return path
}

// FormatTransferSize formats a "received/total" byte string from mutagen
// template output into a human-readable size like "(12.3 MB / 45.6 MB)".
func FormatTransferSize(raw string) string {
	parts := strings.SplitN(raw, "/", 2)
	if len(parts) != 2 {
		return ""
	}
	var received, total uint64
	if _, err := fmt.Sscanf(parts[0], "%d", &received); err != nil {
		return ""
	}
	if _, err := fmt.Sscanf(parts[1], "%d", &total); err != nil {
		return ""
	}
	if total == 0 {
		return ""
	}
	return fmt.Sprintf("(%s / %s)", humanBytes(received), humanBytes(total))
}

func humanBytes(b uint64) string {
	const (
		kb = 1024
		mb = 1024 * kb
		gb = 1024 * mb
	)
	switch {
	case b >= gb:
		return fmt.Sprintf("%.1f GB", float64(b)/float64(gb))
	case b >= mb:
		return fmt.Sprintf("%.1f MB", float64(b)/float64(mb))
	case b >= kb:
		return fmt.Sprintf("%.1f KB", float64(b)/float64(kb))
	default:
		return fmt.Sprintf("%d B", b)
	}
}
