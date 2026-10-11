package main

import (
	"encoding/base64"
	"os"
)

// osc52Clipboard is the escape that hands the terminal a string for its own
// clipboard. Copying is done by the terminal rather than by shelling out to
// xclip, wl-copy or pbcopy: which of those exists is a fact about the machine
// and would be a different guess on every host, while the terminal is the one
// thing this program is already talking to. A paste into the incoming CLI is a
// terminal gesture, so the terminal's clipboard is the one that matters — and
// an OSC 52 travels back over SSH for the same reason.
//
// The sequence is invisible: it draws nothing and moves no cursor, so writing
// it between renders cannot disturb the frame.
func osc52Clipboard(text string) string {
	sequence := "\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte(text)) + "\a"
	if os.Getenv("TMUX") != "" {
		// tmux drops an escape it does not recognise unless it is wrapped for
		// pass-through, and a handoff run inside a tmux session would
		// otherwise put the brief nowhere.
		sequence = "\x1bPtmux;\x1b" + sequence + "\x1b\\"
	}
	return sequence
}
