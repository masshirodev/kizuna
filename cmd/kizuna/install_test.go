package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// scopePATH makes dir the only place a command can be found, apart from the
// system directories the test's own shell scripts need to run at all.
func scopePATH(t *testing.T, dir string) {
	t.Helper()
	t.Setenv("PATH", strings.Join([]string{dir, "/bin", "/usr/bin"}, string(os.PathListSeparator)))
}

func TestProviderInstallersPointAtEachVendorsOwnScript(t *testing.T) {
	cases := map[string]string{
		"codex":       "chatgpt.com/codex/install.sh",
		"claude":      "claude.ai/install.sh",
		"antigravity": "antigravity.google/cli/install.sh",
		"opencode":    "opencode.ai/install",
		// DeepSeek runs through OpenCode, so it is OpenCode that gets installed.
		"deepseek": "opencode.ai/install",
	}
	for provider, want := range cases {
		install, err := providerInstaller(provider)
		if err != nil {
			t.Fatalf("providerInstaller(%q) = %v", provider, err)
		}
		if !strings.HasPrefix(install.url, "https://") || !strings.Contains(install.url, want) {
			t.Errorf("providerInstaller(%q).url = %q, want an https URL containing %q", provider, install.url, want)
		}
		if install.shell != "sh" && install.shell != "bash" {
			t.Errorf("providerInstaller(%q).shell = %q", provider, install.shell)
		}
	}
	if _, err := providerInstaller("unknown"); err == nil {
		t.Fatal("an unknown provider was given an installer rather than an error")
	}
}

// The command shown before the confirmation has to be the one the vendor
// documents, because that is what the user is being asked to recognise.
func TestInstallCommandIsTheDocumentedOneLiner(t *testing.T) {
	install := providerInstall{provider: "claude", url: "https://claude.ai/install.sh", shell: "bash"}
	if got := install.command(); got != "curl -fsSL https://claude.ai/install.sh | bash" {
		t.Fatalf("command() = %q", got)
	}
}

func TestInstallTargetAcceptsAProfileOrABareProvider(t *testing.T) {
	cfg := Config{Profiles: []Profile{{Name: "work", Provider: "codex", Command: "codex"}}}

	fromProfile, err := installTarget(cfg, "work")
	if err != nil || fromProfile.provider != "codex" {
		t.Fatalf("installTarget(profile) = %+v, %v", fromProfile, err)
	}
	// Installing a CLI is the one thing that is useful before a profile exists.
	fromProvider, err := installTarget(cfg, "opencode")
	if err != nil || fromProvider.provider != "opencode" {
		t.Fatalf("installTarget(provider) = %+v, %v", fromProvider, err)
	}
	err = func() error { _, err := installTarget(cfg, "typo"); return err }()
	if err == nil || !strings.Contains(err.Error(), "not a profile") {
		t.Fatalf("installTarget(unknown) = %v, want an error naming both misses", err)
	}
}

func TestFetchInstallScriptWritesExactlyWhatWasServed(t *testing.T) {
	script := "#!/bin/sh\necho installed\n"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(script))
	}))
	defer server.Close()

	dir := t.TempDir()
	path, err := fetchInstallScript(context.Background(), server.URL, dir)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != script {
		t.Fatalf("downloaded script = %q, want %q", data, script)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("script mode = %v, want 0600", info.Mode().Perm())
	}
}

// Downloading before running is only worth doing if a bad response stops here
// rather than reaching a shell.
func TestFetchInstallScriptRefusesAnythingButAServedScript(t *testing.T) {
	cases := []struct {
		name    string
		handler http.HandlerFunc
		want    string
	}{
		{"not found", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		}, "404"},
		{"empty", func(w http.ResponseWriter, r *http.Request) {}, "empty script"},
		{"oversized", func(w http.ResponseWriter, r *http.Request) {
			w.Write(make([]byte, maxInstallScript+1))
		}, "refusing to run it"},
	}
	for _, testCase := range cases {
		server := httptest.NewServer(testCase.handler)
		_, err := fetchInstallScript(context.Background(), server.URL, t.TempDir())
		server.Close()
		if err == nil || !strings.Contains(err.Error(), testCase.want) {
			t.Errorf("%s: err = %v, want it to mention %q", testCase.name, err, testCase.want)
		}
	}
}

func TestRunInstallRunsTheDownloadedScriptAndReportsWhereItLanded(t *testing.T) {
	binDir := t.TempDir()
	// The script stands in for a vendor installer: it drops the CLI somewhere on
	// PATH, which is what runInstall then reports back.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("#!/bin/sh\necho vendor installer ran\nprintf '#!/bin/sh\\n' > " +
			filepath.Join(binDir, "codex") + "\nchmod 0755 " + filepath.Join(binDir, "codex") + "\n"))
	}))
	defer server.Close()
	scopePATH(t, binDir)

	var out strings.Builder
	install := providerInstall{provider: "codex", url: server.URL, shell: "sh"}
	if err := runInstall(install, strings.NewReader(""), &out, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "vendor installer ran") {
		t.Fatalf("the installer did not run:\n%s", out.String())
	}
	if !strings.Contains(out.String(), filepath.Join(binDir, "codex")) {
		t.Fatalf("output does not name where the CLI landed:\n%s", out.String())
	}
}

// A vendor script that installs into a directory the shell cannot reach is the
// common "command not found afterwards" case, and it is not an install failure.
func TestRunInstallSaysWhenTheCLIIsNotOnPathAfterwards(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("#!/bin/sh\nexit 0\n"))
	}))
	defer server.Close()
	scopePATH(t, t.TempDir())

	var out strings.Builder
	install := providerInstall{provider: "opencode", url: server.URL, shell: "sh"}
	if err := runInstall(install, strings.NewReader(""), &out, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "not on PATH") {
		t.Fatalf("output does not explain the missing binary:\n%s", out.String())
	}
}

func TestRunInstallReportsAFailingInstaller(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("#!/bin/sh\nexit 3\n"))
	}))
	defer server.Close()

	var out strings.Builder
	install := providerInstall{provider: "claude", url: server.URL, shell: "sh"}
	err := runInstall(install, strings.NewReader(""), &out, &out)
	if err == nil || !strings.Contains(err.Error(), "claude installer failed") {
		t.Fatalf("err = %v, want the failure named with its provider", err)
	}
}

func TestCommandPathAnswersOnlyForAnInstalledCommand(t *testing.T) {
	binDir := t.TempDir()
	installed := filepath.Join(binDir, "codex")
	if err := os.WriteFile(installed, []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatal(err)
	}
	scopePATH(t, binDir)

	if got := commandPath("codex"); got != installed {
		t.Fatalf("commandPath(installed) = %q, want %q", got, installed)
	}
	if got := commandPath("opencode"); got != "" {
		t.Fatalf("commandPath(missing) = %q, want empty", got)
	}
	if got := commandPath(""); got != "" {
		t.Fatalf("commandPath(\"\") = %q, want empty", got)
	}
}
