package main

import (
	"errors"
	"fmt"
	"io"
	"strings"
)

// `ai mcp` and `ai skill` are the same command twice over: list what one
// profile has, or install some of it into another. They are spelled separately
// rather than as one `ai copy <kind>` because the two things being copied are
// not alike — an MCP server is a handful of settings translated between two
// config syntaxes, a skill is a folder of files moved unchanged — and a single
// command would have to explain that difference in its help text instead of in
// its name.

func mcpCommand(args []string, cfg Config, stdout io.Writer) error {
	args, replace := takeFlag(args, "--replace")
	if len(args) == 0 || args[0] == "list" {
		if len(args) != 2 {
			return errors.New("usage: ai mcp list <profile>")
		}
		return listMCPServers(cfg, args[1], stdout)
	}
	if args[0] != "copy" || len(args) < 3 {
		return errors.New("usage: ai mcp copy <source> <destination> [server...] [--replace]")
	}
	source, destination, err := copyEndpoints(cfg, args[1], args[2])
	if err != nil {
		return err
	}
	copied, err := copyMCPServers(source, destination, args[3:], replace)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "copied %s from %s to %s: %s\n",
		plural(len(copied), "MCP server"), source.Name, destination.Name, strings.Join(copied, ", "))
	return nil
}

func skillCommand(args []string, cfg Config, stdout io.Writer) error {
	args, replace := takeFlag(args, "--replace")
	if len(args) == 0 || args[0] == "list" {
		if len(args) != 2 {
			return errors.New("usage: ai skill list <profile>")
		}
		return listSkills(cfg, args[1], stdout)
	}
	if args[0] != "copy" || len(args) < 3 {
		return errors.New("usage: ai skill copy <source> <destination> [skill...] [--replace]")
	}
	source, destination, err := copyEndpoints(cfg, args[1], args[2])
	if err != nil {
		return err
	}
	copied, err := copySkills(source, destination, args[3:], replace)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "copied %s from %s to %s: %s\n",
		plural(len(copied), "skill"), source.Name, destination.Name, strings.Join(copied, ", "))
	return nil
}

// copyEndpoints resolves both ends of a copy and refuses to write into a
// profile that is running. The destination's CLI reads its config at startup
// and rewrites parts of it as it goes, so editing that file underneath a live
// process is a race whose loser is whichever of the two wrote first.
func copyEndpoints(cfg Config, sourceName, destinationName string) (Profile, Profile, error) {
	source, err := resolveProfile(cfg, sourceName)
	if err != nil {
		return Profile{}, Profile{}, err
	}
	destination, err := resolveProfile(cfg, destinationName)
	if err != nil {
		return Profile{}, Profile{}, err
	}
	if profileIsRunning(destination) {
		return Profile{}, Profile{}, fmt.Errorf("profile %q is running; stop it before writing to its configuration", destination.Name)
	}
	return source, destination, nil
}

func listMCPServers(cfg Config, name string, stdout io.Writer) error {
	profile, err := resolveProfile(cfg, name)
	if err != nil {
		return err
	}
	servers, err := readMCPServers(profile)
	if err != nil {
		return err
	}
	for _, server := range servers {
		fmt.Fprintf(stdout, "%s\t%s\t%s\n", server.Name, server.transport(), server.endpoint())
	}
	return nil
}

func listSkills(cfg Config, name string, stdout io.Writer) error {
	profile, err := resolveProfile(cfg, name)
	if err != nil {
		return err
	}
	skills, err := readSkills(profile)
	if err != nil {
		return err
	}
	for _, item := range skills {
		fmt.Fprintf(stdout, "%s\t%s\n", item.name, item.description)
	}
	return nil
}

// takeFlag pulls one flag out of an argument list wherever it appears, leaving
// the positional arguments in order. Written by hand rather than with the flag
// package because these commands take a variable tail of names, which the flag
// package stops parsing at.
func takeFlag(args []string, flag string) ([]string, bool) {
	kept, found := make([]string, 0, len(args)), false
	for _, arg := range args {
		if arg == flag {
			found = true
			continue
		}
		kept = append(kept, arg)
	}
	return kept, found
}
