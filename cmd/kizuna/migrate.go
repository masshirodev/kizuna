package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Until 2026-10 kizuna was ai-session, a command called `ai` that kept its
// files in ~/.config/ai. The first kizuna to start moves that directory to
// ~/.config/kizuna and leaves a link at the old path. The link is for the
// sessions an older binary launched: their CLAUDE_CONFIG_DIR, CODEX_HOME and
// XDG paths all run through ~/.config/ai, and they keep running until they are
// closed. doc/rename.md is the account.

// legacyStatusLineCommand is the statusLine an older binary installed. It
// reads the old variable, which a kizuna launch no longer sets, so the
// migration replaces it wherever it is still exactly ours.
const legacyStatusLineCommand = `printf '[%s] %s' "${AI_PROFILE:-no profile}" "${PWD##*/}"`

// legacyMessagingMCPName is the name `ai integrate messaging` registered its
// server under.
const legacyMessagingMCPName = "ai"

// migrateLegacyConfig moves base/ai to base/kizuna once, and reports what it
// did on w. It does nothing unless the old directory is really this
// program's (it holds profiles.json) and the new one does not exist yet, so
// another tool's ~/.config/ai is never touched.
func migrateLegacyConfig(w io.Writer) {
	base, err := configBase()
	if err != nil {
		return
	}
	moved, err := moveLegacyConfig(base)
	if err != nil {
		fmt.Fprintf(w, "kizuna: could not move %s to %s: %v\n", filepath.Join(base, legacyAppName), filepath.Join(base, appName), err)
		return
	}
	if !moved {
		return
	}
	fmt.Fprintf(w, "kizuna: ai-session is kizuna now. Moved %s to %s; a link stays at the old path for the sessions an older ai started.\n",
		filepath.Join(base, legacyAppName), filepath.Join(base, appName))
	cfg, err := loadConfig(filepath.Join(base, appName, "profiles.json"))
	if err != nil {
		return
	}
	for _, line := range refreshLegacyIntegrations(cfg) {
		fmt.Fprintln(w, "kizuna: "+line)
	}
}

// moveLegacyConfig is the filesystem half of the migration. The rename is
// atomic and the link follows it at once; a process of an older binary that
// opens a path in between gets one failed open, which the window is far too
// short to make likely.
func moveLegacyConfig(base string) (bool, error) {
	legacy, current := filepath.Join(base, legacyAppName), filepath.Join(base, appName)
	if _, err := os.Lstat(current); err == nil || !errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	info, err := os.Lstat(legacy)
	if err != nil || !info.IsDir() {
		return false, nil
	}
	if _, err := os.Stat(filepath.Join(legacy, "profiles.json")); err != nil {
		return false, nil
	}
	if err := os.Rename(legacy, current); err != nil {
		// Another kizuna started at the same moment and won.
		if _, statErr := os.Stat(current); statErr == nil {
			return false, nil
		}
		return false, err
	}
	// Relative, so the pair survives the config directory itself moving.
	if err := os.Symlink(appName, legacy); err != nil && !errors.Is(err, os.ErrExist) {
		return true, fmt.Errorf("moved, but the link at the old path failed: %w", err)
	}
	return true, nil
}

// refreshLegacyIntegrations rewrites what an older binary wrote into the
// profiles and kizuna would now write differently: the statusLine, which
// reads the old variable, and the messaging hooks and MCP server, which name
// the old command and register under the old server name. Anything that is
// not exactly ours is left alone, as `kizuna integrate` itself does.
func refreshLegacyIntegrations(cfg Config) []string {
	var done []string
	for _, profile := range cfg.Profiles {
		if supportsNativeStatusLine(profile.Provider) {
			if changed, err := replaceLegacyStatusLine(profile); err != nil {
				done = append(done, fmt.Sprintf("%s: statusLine left as it was: %v", profile.Name, err))
			} else if changed {
				done = append(done, profile.Name+": statusLine reads KIZUNA_PROFILE now")
			}
		}
		if messagingDelivery(profile) != "" {
			if err := integrateMessaging(profile, false, io.Discard); err != nil {
				done = append(done, fmt.Sprintf("%s: messaging left as it was: %v", profile.Name, err))
			} else {
				done = append(done, profile.Name+": messaging runs "+aiExecutable()+" now")
			}
		}
	}
	return done
}

func replaceLegacyStatusLine(profile Profile) (bool, error) {
	path, err := statusLineSettingsPath(profile)
	if err != nil {
		return false, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	settings := map[string]json.RawMessage{}
	if err := json.Unmarshal(data, &settings); err != nil {
		return false, err
	}
	var line map[string]any
	if json.Unmarshal(settings["statusLine"], &line) != nil || line["command"] != legacyStatusLineCommand {
		return false, nil
	}
	line["command"] = statusLineCommand
	encoded, err := json.Marshal(line)
	if err != nil {
		return false, err
	}
	settings["statusLine"] = encoded
	merged, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return false, err
	}
	return true, os.WriteFile(path, append(merged, '\n'), 0600)
}

// legacyConfigPath is path spelled through the old directory, for matching
// against what a session an older binary launched recorded: its environment
// names ~/.config/ai/..., which is the same directory through the link. Empty
// when path is not under kizuna's own directory.
func legacyConfigPath(path string) string {
	base, err := configBase()
	if err != nil {
		return ""
	}
	current := filepath.Join(base, appName)
	rel, err := filepath.Rel(current, filepath.Clean(path))
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return ""
	}
	return filepath.Join(base, legacyAppName, rel)
}
