package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// A skill is a folder with a SKILL.md in it, which is the one thing Claude
// Code, Codex, and OpenCode all agree on — so unlike an MCP server, a skill
// needs no translation to cross between them. Only where the folder lives
// differs, and that is what skillsDir answers.
//
// The consequence is worth stating plainly: copying a skill is copying files,
// and the files are whatever the author put there — scripts included. This
// launcher moves them between two directories that already belong to the same
// person; it does not read them, and it is not a safe way to accept a skill
// from anyone else.
type skill struct {
	name string
	// description is the one line of SKILL.md's frontmatter that says when the
	// skill applies. It is what makes a picker row worth reading, since skill
	// folder names are short and frequently mean nothing on their own.
	description string
	path        string
}

// skillsDirName is the folder every provider that supports skills keeps them
// in, relative to that provider's own config home.
const skillsDirName = "skills"

// skillsDir is where a profile's skills live inside its isolated state
// directory. Antigravity is absent because it has no skill mechanism, and
// deepseek because its profile has no isolated config home of its own — it
// reads the machine's ordinary OpenCode directories, which are not this
// launcher's to write into.
func skillsDir(profile Profile) (string, error) {
	root, err := profileRoot()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(root, profile.Name)
	switch profile.Provider {
	case "claude":
		return filepath.Join(dir, "claude", skillsDirName), nil
	case "codex":
		return filepath.Join(dir, "codex", skillsDirName), nil
	case "opencode":
		return filepath.Join(dir, "config", "opencode", skillsDirName), nil
	default:
		return "", fmt.Errorf("provider %q has no skills directory this launcher knows", profile.Provider)
	}
}

func supportsSkills(profile Profile) bool {
	_, err := skillsDir(profile)
	return err == nil
}

// readSkills lists the skills a profile can lend. A directory without a
// SKILL.md is not a skill and is passed over rather than reported as a broken
// one: Codex keeps its own built-ins under a dotted folder there, and a plugin
// is free to leave anything else beside them.
func readSkills(profile Profile) ([]skill, error) {
	dir, err := skillsDir(profile)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	skills := make([]skill, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		manifest := filepath.Join(path, "SKILL.md")
		if _, err := os.Stat(manifest); err != nil {
			continue
		}
		skills = append(skills, skill{
			name:        entry.Name(),
			description: skillDescription(manifest),
			path:        path,
		})
	}
	sort.Slice(skills, func(i, j int) bool { return skills[i].name < skills[j].name })
	return skills, nil
}

// skillDescription reads the description out of SKILL.md's frontmatter. The
// frontmatter is YAML and this is not a YAML parser: it takes the first
// description key of the leading block and stops, which is the shape every
// skill in this format actually has. A file it cannot read that way is
// described by nothing rather than by a wrong guess.
func skillDescription(path string) string {
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	opened := false
	for scanner.Scan() {
		line := strings.TrimRight(scanner.Text(), "\r")
		if strings.TrimSpace(line) == "---" {
			if opened {
				return ""
			}
			opened = true
			continue
		}
		if !opened {
			return ""
		}
		key, value, found := strings.Cut(line, ":")
		if !found || strings.TrimSpace(key) != "description" {
			continue
		}
		return strings.Trim(strings.TrimSpace(value), `"'`)
	}
	return ""
}

// copySkills installs skills from one profile into another. names selects a
// subset; an empty names copies all of them.
//
// A skill already installed under the same name is refused unless replace says
// otherwise, and a replacement removes the old folder first: merging two skills
// into one directory would leave the destination holding a SKILL.md from one
// and half the scripts of another.
func copySkills(source, destination Profile, names []string, replace bool) ([]string, error) {
	if source.Name == destination.Name {
		return nil, errors.New("source and destination are the same profile")
	}
	target, err := skillsDir(destination)
	if err != nil {
		return nil, fmt.Errorf("profile %q (%s) has no skills directory this launcher knows",
			destination.Name, destination.Provider)
	}
	available, err := readSkills(source)
	if err != nil {
		return nil, err
	}
	if len(available) == 0 {
		return nil, fmt.Errorf("profile %q has no skills installed", source.Name)
	}
	chosen, err := selectByName(available, names, func(item skill) string { return item.name },
		fmt.Sprintf("profile %q has no skill named", source.Name))
	if err != nil {
		return nil, err
	}
	// Every destination is checked before the first one is written, so a call
	// naming one skill that is already there leaves the profile exactly as it
	// was rather than half updated.
	for _, item := range chosen {
		if _, err := os.Stat(filepath.Join(target, item.name)); err == nil {
			if !replace {
				return nil, fmt.Errorf("profile %q already has a skill named %q; pass --replace to overwrite it",
					destination.Name, item.name)
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}
	if err := os.MkdirAll(target, 0700); err != nil {
		return nil, err
	}
	copied := make([]string, 0, len(chosen))
	for _, item := range chosen {
		destinationPath := filepath.Join(target, item.name)
		if err := os.RemoveAll(destinationPath); err != nil {
			return nil, err
		}
		if err := copyTree(item.path, destinationPath, nil); err != nil {
			return nil, err
		}
		copied = append(copied, item.name)
	}
	return copied, nil
}
