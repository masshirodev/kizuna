package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

const (
	// installFetchTimeout bounds only the download. The installer itself is given
	// as long as it needs: it may compile, unpack, or ask a question.
	installFetchTimeout = 30 * time.Second
	// maxInstallScript is a sanity ceiling. Every vendor installer is a few tens
	// of kilobytes of shell; a response far past that is not the script.
	maxInstallScript = 4 << 20
)

// providerInstall is the vendor's own installer for one provider's CLI.
// ai-session deliberately does not maintain a package list of its own: it
// isolates state, and keeping track of how five CLIs are packaged this month is
// not something it can keep correct.
type providerInstall struct {
	provider string
	url      string
	// shell is the interpreter the vendor documents for its script.
	shell string
}

func providerInstaller(provider string) (providerInstall, error) {
	switch provider {
	case "codex":
		return providerInstall{provider: provider, url: "https://chatgpt.com/codex/install.sh", shell: "sh"}, nil
	case "claude":
		return providerInstall{provider: provider, url: "https://claude.ai/install.sh", shell: "bash"}, nil
	case "antigravity":
		return providerInstall{provider: provider, url: "https://antigravity.google/cli/install.sh", shell: "bash"}, nil
	case "opencode", "deepseek":
		// DeepSeek runs through OpenCode, so OpenCode is what gets installed.
		return providerInstall{provider: provider, url: "https://opencode.ai/install", shell: "bash"}, nil
	default:
		return providerInstall{}, fmt.Errorf("provider %q has no known installer; install its CLI yourself", provider)
	}
}

// installTarget accepts a profile name, an app name (resolved to its active
// member), or a bare provider. Installing a CLI is the one thing that is
// useful before a profile exists, so it is the single command that does not
// insist on one.
func installTarget(cfg Config, name string) (providerInstall, error) {
	if profile, err := resolveProfile(cfg, name); err == nil {
		return providerInstaller(profile.Provider)
	}
	install, err := providerInstaller(name)
	if err != nil {
		return providerInstall{}, fmt.Errorf("%q is not a profile, and %w", name, err)
	}
	return install, nil
}

// command is the documented one-liner this install is equivalent to. The TUI
// shows it before asking, because "install claude" is agreement to install a
// CLI, not to run whatever a URL happens to serve.
func (i providerInstall) command() string {
	return "curl -fsSL " + i.url + " | " + i.shell
}

// installProviderCLI runs the provider's installer from the command line.
func installProviderCLI(install providerInstall, stdout, stderr io.Writer) error {
	return runInstall(install, os.Stdin, stdout, stderr)
}

// runInstall downloads the installer and then runs it. Piping curl into a shell
// is what the vendors document and what this ends up doing, but downloading
// first is strictly better in two ways: a truncated response cannot half-execute,
// and the script is on disk to read when an install goes wrong.
//
// It runs in the ordinary user environment rather than a profile's. The CLI
// binary is shared by every profile — only state is isolated — and Antigravity's
// private HOME would otherwise bury the install inside one profile's directory.
func runInstall(install providerInstall, stdin io.Reader, stdout, stderr io.Writer) error {
	dir, err := os.MkdirTemp("", "ai-install-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)

	ctx, cancel := context.WithTimeout(context.Background(), installFetchTimeout)
	defer cancel()
	script, err := fetchInstallScript(ctx, install.url, dir)
	if err != nil {
		return err
	}

	fmt.Fprintln(stdout, "› "+install.command())
	cmd := exec.Command(install.shell, script)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("the %s installer failed: %w", install.provider, err)
	}
	if path := commandPath(defaultCommand(install.provider)); path != "" {
		fmt.Fprintln(stdout, "installed "+path)
		return nil
	}
	// The installer succeeded but its target is not reachable, which is almost
	// always ~/.local/bin missing from PATH rather than a failed install.
	fmt.Fprintf(stdout, "%s installed, but %s is not on PATH yet; open a new shell or add its install directory to PATH\n",
		install.provider, defaultCommand(install.provider))
	return nil
}

func fetchInstallScript(ctx context.Context, url, dir string) (string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return "", fmt.Errorf("%s could not be reached", url)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s answered %s", url, response.Status)
	}
	script, err := io.ReadAll(io.LimitReader(response.Body, maxInstallScript+1))
	if err != nil {
		return "", fmt.Errorf("%s could not be read", url)
	}
	switch {
	case len(script) == 0:
		return "", fmt.Errorf("%s served an empty script", url)
	case len(script) > maxInstallScript:
		return "", fmt.Errorf("%s served more than %d bytes; refusing to run it", url, maxInstallScript)
	}
	path := filepath.Join(dir, "install")
	return path, os.WriteFile(path, script, 0600)
}

// commandPath reports where a profile's command resolves, or an empty string
// when it is not installed. A missing CLI is the one failure a launch cannot
// explain for itself: exec reports "executable file not found" and says nothing
// about how to get it.
func commandPath(command string) string {
	if command == "" {
		return ""
	}
	path, err := exec.LookPath(command)
	if err != nil {
		return ""
	}
	return path
}
