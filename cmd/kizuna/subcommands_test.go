package main

import "testing"

func TestSubcommandIndexSkipsLeadingFlags(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want int
	}{
		{"empty", nil, -1},
		{"only flags", []string{"--auto", "--model", "x"}, 2},
		{"subcommand first", []string{"run", "msg"}, 0},
		{"flag then subcommand", []string{"--print", "exec", "hi"}, 1},
		{"empty word is not a flag", []string{"", "run"}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := subcommandIndex(tc.args); got != tc.want {
				t.Fatalf("subcommandIndex(%v) = %d, want %d", tc.args, got, tc.want)
			}
		})
	}
}

func TestSubcommandDispositionKnowsEachProviderTable(t *testing.T) {
	cases := []struct {
		provider      string
		word          string
		known         bool
		takesDefaults bool
	}{
		{"opencode", "run", true, true},
		{"opencode", "models", true, false},
		{"opencode", "auth", true, false},
		{"opencode", "mcp", true, false},
		{"opencode", "plug", true, false},
		{"deepseek", "run", true, true},
		{"deepseek", "models", true, false},
		{"claude", "mcp", true, false},
		{"claude", "update", true, false},
		{"claude", "config", false, false},
		{"codex", "exec", true, true},
		{"codex", "resume", true, true},
		{"codex", "fork", true, true},
		{"codex", "review", true, false},
		{"codex", "mcp", true, false},
		{"antigravity", "models", true, false},
		{"antigravity", "mcp", true, false},
		{"opencode", "fix the bug", false, false},
		{"unknown-provider", "run", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.provider+"/"+tc.word, func(t *testing.T) {
			known, takes := subcommandDisposition(tc.provider, tc.word)
			if known != tc.known || takes != tc.takesDefaults {
				t.Fatalf("subcommandDisposition(%q, %q) = %v, %v; want %v, %v",
					tc.provider, tc.word, known, takes, tc.known, tc.takesDefaults)
			}
		})
	}
}
