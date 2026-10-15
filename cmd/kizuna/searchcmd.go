package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// `kizuna search` is the command-line half of searching what was said
// (doc/session-search.md): the same index and the same ranking as the TUI's
// search mode, printed one hit at a time or as JSON for scripts and agents. On
// a terminal with neither --print nor --json it opens the TUI on the query, so
// a hit can be resumed or handed off from there.

const searchUsage = "usage: kizuna search [--profile NAME]... [--repo NAME|PATH] [--since YYYY-MM-DD] [--headless] [--from user] [--best] [--limit N] [--print|--json] <query>"

type searchOptions struct {
	query    searchQuery
	asJSON   bool
	print    bool
	profiles []string
}

func parseSearchArgs(cfg Config, args []string) (searchOptions, error) {
	var options searchOptions
	var words []string
	value := func(index *int, flag string) (string, error) {
		arg := args[*index]
		if cut := strings.IndexByte(arg, '='); cut >= 0 {
			return arg[cut+1:], nil
		}
		if *index+1 >= len(args) {
			return "", fmt.Errorf("%s needs a value\n%s", flag, searchUsage)
		}
		*index++
		return args[*index], nil
	}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		flag, _, _ := strings.Cut(arg, "=")
		switch flag {
		case "--json":
			options.asJSON = true
		case "--print":
			options.print = true
		case "--headless":
			options.query.headless = true
		case "--best":
			options.query.bestMatch = true
		case "--newest":
			// The default since newest first became the order; still accepted
			// so scripts written against the first release keep working.
			options.query.bestMatch = false
		case "--profile", "-p":
			name, err := value(&index, flag)
			if err != nil {
				return options, err
			}
			options.profiles = append(options.profiles, name)
		case "--repo":
			repo, err := value(&index, flag)
			if err != nil {
				return options, err
			}
			options.query.folder = repoFolderFilter(repo)
		case "--since":
			since, err := value(&index, flag)
			if err != nil {
				return options, err
			}
			day, err := time.ParseInLocation("2006-01-02", since, time.Local)
			if err != nil {
				return options, fmt.Errorf("--since takes a date, YYYY-MM-DD: %q", since)
			}
			options.query.since = day
		case "--from":
			from, err := value(&index, flag)
			if err != nil {
				return options, err
			}
			if from != "user" {
				return options, fmt.Errorf("--from takes only \"user\"; without it, both sides are searched")
			}
			options.query.fromUser = true
		case "--limit":
			limit, err := value(&index, flag)
			if err != nil {
				return options, err
			}
			n, err := strconv.Atoi(limit)
			if err != nil || n < 1 {
				return options, fmt.Errorf("--limit takes a positive number: %q", limit)
			}
			options.query.limit = n
		case "--":
			words = append(words, args[index+1:]...)
			index = len(args)
		default:
			if strings.HasPrefix(arg, "--") {
				return options, fmt.Errorf("unknown flag %s\n%s", arg, searchUsage)
			}
			words = append(words, arg)
		}
	}
	options.query.text = strings.Join(words, " ")
	if strings.TrimSpace(options.query.text) == "" {
		return options, errors.New(searchUsage)
	}
	if len(options.profiles) == 0 {
		options.query.profiles = cfg.Profiles
	}
	for _, name := range options.profiles {
		profile, err := resolveProfile(cfg, name)
		if err != nil {
			return options, err
		}
		if !searchableProvider(profile.Provider) {
			return options, fmt.Errorf("%s is a %s profile, whose conversations are not indexed", profile.Name, profile.Provider)
		}
		options.query.profiles = append(options.query.profiles, profile)
	}
	return options, nil
}

// repoFolderFilter accepts the folders of one repository, named either by its
// name ("kumiko") or by a path inside it, the way `kizuna peers [repo]` does.
// A folder's repository is looked up once.
func repoFolderFilter(repo string) func(string) bool {
	byPath := strings.ContainsRune(repo, filepath.Separator) || repo == "." || repo == ".."
	if byPath {
		if abs, err := filepath.Abs(expandHome(repo)); err == nil {
			repo = abs
		}
	}
	cache := map[string]bool{}
	return func(folder string) bool {
		if folder == "" {
			return false
		}
		if byPath {
			return folder == repo || pathInside(folder, repo)
		}
		if accepted, seen := cache[folder]; seen {
			return accepted
		}
		checkout := readGitCheckout(folder)
		accepted := checkout.repo == repo || (checkout.repo == "" && filepath.Base(folder) == repo)
		cache[folder] = accepted
		return accepted
	}
}

func expandHome(path string) string {
	if path == "~" || strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(path, "~"))
		}
	}
	return path
}

func searchCommand(cfg Config, args []string, stdout, stderr io.Writer) error {
	options, err := parseSearchArgs(cfg, args)
	if err != nil {
		return err
	}
	if !options.print && !options.asJSON && stdoutIsTerminal() && stdinIsTerminal() {
		return runTUISearch(cfg, options)
	}
	coverage := backfillWithProgress(options.query.profiles, stderr)
	result := searchSessions(options.query)
	if result.err != nil {
		return result.err
	}
	result.coverage.failed = coverage.failed
	if options.asJSON {
		return printSearchJSON(result, stdout)
	}
	printSearchHits(result, options.query, stdout)
	return nil
}

func stdinIsTerminal() bool {
	info, err := os.Stdin.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

// backfillWithProgress brings the index up to date before a search, saying how
// far it has got on stderr when there is anything to read. The first search
// reads every conversation; every later one reads only what changed.
func backfillWithProgress(profiles []Profile, stderr io.Writer) indexCoverage {
	interactive := false
	if file, ok := stderr.(*os.File); ok {
		if info, err := file.Stat(); err == nil {
			interactive = info.Mode()&os.ModeCharDevice != 0
		}
	}
	started := time.Now()
	printed := false
	coverage := backfillSessionTurns(profiles, func(c indexCoverage) {
		if c.complete() || !interactive {
			return
		}
		fmt.Fprintf(stderr, "\rreading conversations into the index: %d of %d", c.searched, c.total)
		printed = true
	})
	if printed {
		fmt.Fprintf(stderr, "\rread %d conversations into the index in %s\n", coverage.total, time.Since(started).Round(time.Second))
	}
	for _, failure := range coverage.failed {
		fmt.Fprintln(stderr, "✗ "+failure.describe())
	}
	return coverage
}

type searchHitJSON struct {
	Profile  string `json:"profile"`
	Provider string `json:"provider"`
	ID       string `json:"id"`
	Title    string `json:"title"`
	Folder   string `json:"folder"`
	Updated  string `json:"updated"`
	Matches  int    `json:"matches"`
	Turns    int    `json:"turns"`
	SaidBy   string `json:"said_by"`
	Snippet  string `json:"snippet"`
	Resume   string `json:"resume"`
	Headless bool   `json:"headless,omitempty"`
}

type searchResultJSON struct {
	Query    string          `json:"query"`
	ReadAs   string          `json:"read_as"`
	Searched int             `json:"searched"`
	Total    int             `json:"total"`
	Failed   []string        `json:"failed,omitempty"`
	Hits     []searchHitJSON `json:"hits"`
}

// searchResultData is the JSON shape of a result, shared by --json and the
// MCP tool. A snippet marks its matched words with square brackets.
func searchResultData(result searchResult) searchResultJSON {
	data := searchResultJSON{
		Query: result.query, ReadAs: result.reading.describe(),
		Searched: result.coverage.searched, Total: result.coverage.total,
		Hits: []searchHitJSON{},
	}
	for _, failure := range result.coverage.failed {
		data.Failed = append(data.Failed, failure.describe())
	}
	for _, hit := range result.hits {
		saidBy := hit.provider
		if hit.bestFromUser {
			saidBy = "you"
		}
		updated := ""
		if !hit.record.lastActive.IsZero() {
			updated = hit.record.lastActive.Format(time.RFC3339)
		}
		data.Hits = append(data.Hits, searchHitJSON{
			Profile: hit.record.profile, Provider: hit.provider, ID: hit.record.session.id,
			Title: hit.record.session.title, Folder: hit.record.folder, Updated: updated,
			Matches: hit.matches, Turns: hit.turns, SaidBy: saidBy,
			Snippet:  bracketMarks(hit.snippet),
			Resume:   resumeCommandLine(hit.record.profile, hit.record.session.id),
			Headless: hit.record.headless,
		})
	}
	return data
}

func printSearchJSON(result searchResult, stdout io.Writer) error {
	data, err := json.MarshalIndent(searchResultData(result), "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(stdout, string(data))
	return err
}

// printSearchHits is --print: per hit, a line naming the conversation and the
// resume command, then the snippet under who said it.
func printSearchHits(result searchResult, query searchQuery, stdout io.Writer) {
	coverage := ""
	if !result.coverage.complete() {
		coverage = fmt.Sprintf(" (searched %d of %d so far)", result.coverage.searched, result.coverage.total)
	}
	if len(result.hits) == 0 {
		fmt.Fprintf(stdout, "Nothing said matches %s%s\n", result.query, coverage)
		for _, count := range countTerms(query, result.reading) {
			fmt.Fprintf(stdout, "  %-14s %s\n", count.term, plural(count.conversations, "conversation"))
		}
		return
	}
	terminal := stdoutIsTerminal()
	now := time.Now()
	for index, hit := range result.hits {
		if index > 0 {
			fmt.Fprintln(stdout)
		}
		title := hit.record.session.title
		if title == "" {
			title = "untitled session"
		}
		fmt.Fprintf(stdout, "%s  %s  %s  %s  %s  (%s)\n", hit.record.profile, hit.provider,
			formatWhen(now, hit.record.activity()), shortenHome(hit.record.folder), title, matchCount(hit.matches))
		saidBy := hit.provider
		if hit.bestFromUser {
			saidBy = "you"
		}
		snippet := bracketMarks(hit.snippet)
		if terminal {
			snippet = renderMarks(hit.snippet, lipgloss.NewStyle(), matchStyle)
		}
		fmt.Fprintf(stdout, "    %s: %s\n", saidBy, strings.Join(strings.Fields(snippet), " "))
		fmt.Fprintf(stdout, "    %s\n", resumeCommandLine(hit.record.profile, hit.record.session.id))
	}
	fmt.Fprintf(stdout, "\n%s · read as %s%s\n", plural(len(result.hits), "conversation"), result.reading.describe(), coverage)
}

func resumeCommandLine(profile, id string) string {
	return appName + " " + profile + " resume " + id
}

// bracketMarks writes the matched words of a snippet between square brackets.
func bracketMarks(text string) string {
	return strings.NewReplacer(matchOpen, "[", matchClose, "]").Replace(text)
}

// renderMarks styles a snippet's plain and matched runs.
func renderMarks(text string, plain, match lipgloss.Style) string {
	var out strings.Builder
	for _, span := range parseMarked(text) {
		if span.match {
			out.WriteString(match.Render(span.text))
		} else {
			out.WriteString(plain.Render(span.text))
		}
	}
	return out.String()
}

func matchCount(n int) string {
	if n == 1 {
		return "1 match"
	}
	return strconv.Itoa(n) + " matches"
}

// runTUISearch opens the resume box in search mode on the query, with the keys
// on the results, so ↵ and H work from the command line too. A search narrowed
// to one profile opens on that account; otherwise on every account.
func runTUISearch(cfg Config, options searchOptions) error {
	configPath, err := configPath()
	if err != nil {
		return err
	}
	workingDir, err := os.Getwd()
	if err != nil {
		return err
	}
	profiles := sortedProfiles(cfg.Profiles)
	m := tuiModel{
		configPath: configPath,
		profiles:   profiles,
		mode:       tuiRecent,
		workingDir: workingDir,
		autoSwap:   cfg.Settings.AutoSwap,
		lineage:    handedOff(readLineage()),
		pickerAll:  len(options.query.profiles) != 1 || len(options.profiles) == 0,
		previews:   map[string]sessionPreview{},
	}
	if !m.pickerAll {
		for index, profile := range profiles {
			if profile.Name == options.query.profiles[0].Name {
				m.cursor = index
			}
		}
	}
	m.search = searchState{query: options.query.text, bestMatch: options.query.bestMatch}
	m.showHeadless = options.query.headless
	opened := m.enterSearch()
	// The query came whole from the command line, so the keys start on the
	// results and it runs as typed rather than as a prefix.
	m.search.focus = searchOnResults
	start := tea.Batch(opened, m.requery(false))
	_, err = tea.NewProgram(searchStartModel{tuiModel: m, start: start}, tea.WithAltScreen()).Run()
	return err
}

// searchStartModel is the TUI with the search's first commands (the backfill
// and the query) added to what it starts with.
type searchStartModel struct {
	tuiModel
	start tea.Cmd
}

func (s searchStartModel) Init() tea.Cmd {
	return tea.Batch(s.tuiModel.Init(), s.start)
}
