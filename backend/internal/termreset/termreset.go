// Package termreset writes escape sequences that restore the local terminal
// to a sane state after an interactive backend session ends. A remote
// process normally sends these on exit; if it crashes or the connection
// drops abruptly the sequences never arrive and the user is left with mouse
// tracking on, the cursor hidden, bracketed paste stuck on, etc.
package termreset

import (
	"io"
	"os"

	"github.com/mattn/go-isatty"
)

// Reset deliberately does NOT exit the alternate screen buffer (\x1b[?1049l).
// Doing so when the remote app never entered the alternate buffer erases
// the user's scrollback, including any error output. Apps that use the
// alternate buffer emit \x1b[?1049l themselves on clean shutdown.
const resetSeq = "" +
	"\x1b[?1000l" + // disable mouse click tracking
	"\x1b[?1002l" + // disable mouse button-event tracking
	"\x1b[?1003l" + // disable any-event mouse tracking
	"\x1b[?1006l" + // disable SGR extended mouse mode
	"\x1b[?2004l" + // disable bracketed paste mode
	"\x1b[?25h" //   show cursor

// Reset writes the reset sequences to w. It is a no-op unless w is an
// *os.File pointing at a TTY, so it's safe to defer unconditionally.
func Reset(w io.Writer) {
	f, ok := w.(*os.File)
	if !ok {
		return
	}
	if !isatty.IsTerminal(f.Fd()) {
		return
	}
	_, _ = io.WriteString(w, resetSeq)
}
