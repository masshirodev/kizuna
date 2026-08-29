package main

import (
	"fmt"
	"io"
	"os"
	"strings"
)

// sessionTitle names a running session for the terminal's window or tab title,
// so a screen owned entirely by the provider CLI still says which account it is
// spending.
func sessionTitle(profile Profile) string {
	if profile.Provider == "" || profile.Provider == profile.Name {
		return appName + " · " + profile.Name
	}
	return appName + " · " + profile.Name + " (" + profile.Provider + ")"
}

// markTerminalTitle sets the terminal title and returns a function restoring
// the previous one. The xterm title stack (CSI 22;2t / CSI 23;2t) is used
// instead of remembering a string because the current title cannot be read back
// portably; a terminal without the stack simply keeps the title ai set, which
// still names the right profile.
func markTerminalTitle(writer io.Writer, title string) func() {
	if writer == nil || !stdoutIsTerminal() {
		return func() {}
	}
	return markTerminalTitleTo(writer, title)
}

func markTerminalTitleTo(writer io.Writer, title string) func() {
	fmt.Fprintf(writer, "\x1b[22;2t\x1b]2;%s\a", sanitizeTitle(title))
	return func() { fmt.Fprint(writer, "\x1b[23;2t") }
}

func stdoutIsTerminal() bool {
	info, err := os.Stdout.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

// sanitizeTitle drops the control bytes that would end the escape sequence
// early. Profile names are already restricted, but a provider is free text in
// profiles.json and reaches the terminal verbatim without this.
func sanitizeTitle(title string) string {
	return strings.Map(func(char rune) rune {
		if char < 0x20 || char == 0x7f {
			return -1
		}
		return char
	}, title)
}
