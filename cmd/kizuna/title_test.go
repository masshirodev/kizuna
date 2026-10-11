package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestSessionTitleNamesProfileAndProvider(t *testing.T) {
	cases := []struct {
		profile Profile
		want    string
	}{
		{Profile{Name: "codex-work", Provider: "codex"}, "kizuna · codex-work (codex)"},
		{Profile{Name: "claude", Provider: "claude"}, "kizuna · claude"},
		{Profile{Name: "scratch"}, "kizuna · scratch"},
	}
	for _, testCase := range cases {
		if got := sessionTitle(testCase.profile); got != testCase.want {
			t.Errorf("sessionTitle(%q/%q) = %q, want %q", testCase.profile.Name, testCase.profile.Provider, got, testCase.want)
		}
	}
}

// A provider is free text in profiles.json, so it must not be able to close the
// escape sequence and write arbitrary control bytes to the terminal.
func TestSanitizeTitleDropsControlBytes(t *testing.T) {
	got := sanitizeTitle("kizuna · work\x07\x1b]0;evil\x07\nrest")
	if strings.ContainsAny(got, "\x07\x1b\n") {
		t.Fatalf("control bytes survived sanitizing: %q", got)
	}
	if !strings.Contains(got, "kizuna · work") || !strings.Contains(got, "rest") {
		t.Fatalf("printable text was dropped: %q", got)
	}
}

// The escapes only make sense on a terminal; a redirected stdout must stay
// clean so piped output is not corrupted.
func TestMarkTerminalTitleSkipsNonTerminalStdout(t *testing.T) {
	if stdoutIsTerminal() {
		t.Skip("test stdout is a terminal")
	}
	var buffer bytes.Buffer
	markTerminalTitle(&buffer, "kizuna · claude")()
	if buffer.Len() != 0 {
		t.Fatalf("wrote to a redirected stdout: %q", buffer.String())
	}
}

func TestMarkTerminalTitleWritesAndRestores(t *testing.T) {
	var buffer bytes.Buffer
	restore := markTerminalTitleTo(&buffer, "kizuna · claude")
	set := buffer.String()
	if !strings.Contains(set, "\x1b[22;2t") || !strings.Contains(set, "\x1b]2;kizuna · claude\a") {
		t.Fatalf("title was not pushed and set: %q", set)
	}
	restore()
	if !strings.HasSuffix(buffer.String(), "\x1b[23;2t") {
		t.Fatalf("title was not popped: %q", buffer.String())
	}
}
