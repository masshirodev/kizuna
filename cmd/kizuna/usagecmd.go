package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"text/tabwriter"
	"time"
)

// `kizuna usage` prints what the TUI's gauges show, for scripts: Kantoku asks
// it which profile still has quota before every launch. The JSON is a
// contract (doc/usage.md); the table is for people and may change.

// Exit codes of `kizuna usage <profile>`. A script branches on these without
// parsing anything; the JSON on stdout says the same thing in detail.
const (
	usageExitKnown          = 0 // at least one window has a current figure
	usageExitUnknownProfile = 2 // no profile or app by that name
	usageExitStale          = 3 // a reading exists, but every window in it has rolled over since
	usageExitUnknown        = 4 // no reading: the provider keeps no quota cache, or never wrote one
)

// exitStatus carries a process exit code out of run. err may be nil when the
// output already said everything, as `kizuna usage` does for a stale reading.
type exitStatus struct {
	code int
	err  error
}

func (status exitStatus) Error() string {
	if status.err == nil {
		return fmt.Sprintf("exit status %d", status.code)
	}
	return status.err.Error()
}

func (status exitStatus) Unwrap() error { return status.err }

type usageReportWindow struct {
	// Remaining is the percent left, null when the window has rolled over
	// since the reading (stale) and has probably refilled.
	Remaining *int `json:"remaining"`
	// ResetsAt is when the window rolls over; for a stale window, the rollover
	// that already passed. Null when the provider did not say.
	ResetsAt *string `json:"resets_at"`
}

type usageReport struct {
	Profile   string             `json:"profile"`
	Provider  string             `json:"provider"`
	FetchedAt *string            `json:"fetched_at"`
	FiveHour  *usageReportWindow `json:"five_hour"`
	Weekly    *usageReportWindow `json:"weekly"`
	Stale     bool               `json:"stale"`
}

func reportTime(at time.Time) *string {
	if at.IsZero() {
		return nil
	}
	text := at.UTC().Format(time.RFC3339)
	return &text
}

// reportWindow is null for a window the cache does not report at all, never a
// zero: a script reading 0 would take a full account for an empty one.
func reportWindow(window usageWindow) *usageReportWindow {
	switch {
	case window.Known:
		percent := window.Percent
		return &usageReportWindow{Remaining: &percent, ResetsAt: reportTime(window.Resets)}
	case window.Stale:
		return &usageReportWindow{ResetsAt: reportTime(window.Resets)}
	default:
		return nil
	}
}

func newUsageReport(profile Profile, usage usageRemaining) usageReport {
	report := usageReport{
		Profile:  profile.Name,
		Provider: profile.Provider,
		FiveHour: reportWindow(usage.FiveHour),
		Weekly:   reportWindow(usage.Weekly),
		Stale:    usage.stale(),
	}
	if usage.read() {
		report.FetchedAt = reportTime(usage.FetchedAt)
	}
	return report
}

func usageExitCode(usage usageRemaining) int {
	switch {
	case usage.known():
		return usageExitKnown
	case usage.stale():
		return usageExitStale
	default:
		return usageExitUnknown
	}
}

func usageCommand(cfg Config, args []string, now time.Time, stdout io.Writer) error {
	rest, asJSON := takeFlag(args, "--json")
	if len(rest) > 1 {
		return errors.New("usage: kizuna usage [--json] [profile|app]")
	}
	if len(rest) == 1 {
		profile, err := resolveProfile(cfg, rest[0])
		if err != nil {
			return exitStatus{code: usageExitUnknownProfile, err: err}
		}
		usage := profileUsageRemaining(profile, now)
		report := newUsageReport(profile, usage)
		if asJSON {
			err = writeUsageJSON(stdout, report)
		} else {
			err = writeUsageTable(stdout, []usageReport{report}, now)
		}
		if err != nil {
			return err
		}
		if code := usageExitCode(usage); code != usageExitKnown {
			return exitStatus{code: code}
		}
		return nil
	}

	profiles := append([]Profile(nil), cfg.Profiles...)
	sort.Slice(profiles, func(i, j int) bool { return profiles[i].Name < profiles[j].Name })
	reports := make([]usageReport, 0, len(profiles))
	for _, profile := range profiles {
		reports = append(reports, newUsageReport(profile, profileUsageRemaining(profile, now)))
	}
	if asJSON {
		return writeUsageJSON(stdout, reports)
	}
	return writeUsageTable(stdout, reports, now)
}

func writeUsageJSON(stdout io.Writer, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(stdout, string(data))
	return err
}

func writeUsageTable(stdout io.Writer, reports []usageReport, now time.Time) error {
	writer := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(writer, "PROFILE\tPROVIDER\t5-HOUR\tWEEKLY\tREAD")
	for _, report := range reports {
		read := "never"
		if report.FetchedAt != nil {
			at, _ := time.Parse(time.RFC3339, *report.FetchedAt)
			read = formatAge(now.Sub(at)) + " ago"
		} else if report.FiveHour != nil || report.Weekly != nil {
			read = "unknown"
		}
		fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\n", report.Profile, report.Provider,
			usageCell(report.FiveHour, now), usageCell(report.Weekly, now), read)
	}
	return writer.Flush()
}

func usageCell(window *usageReportWindow, now time.Time) string {
	if window == nil {
		return "unknown"
	}
	var resets time.Time
	if window.ResetsAt != nil {
		resets, _ = time.Parse(time.RFC3339, *window.ResetsAt)
	}
	if window.Remaining == nil {
		if resets.IsZero() {
			return "stale (rolled over)"
		}
		return "stale (reset " + resets.Local().Format("2 Jan") + ")"
	}
	cell := fmt.Sprintf("%d%% left", *window.Remaining)
	if reset := formatReset(now, resets); reset != "" {
		cell += ", " + reset
	}
	return cell
}

// formatAge says how old a reading is in its largest whole unit.
func formatAge(age time.Duration) string {
	switch {
	case age < time.Minute:
		return "just now"
	case age < time.Hour:
		return fmt.Sprintf("%dm", int(age/time.Minute))
	case age < 48*time.Hour:
		return fmt.Sprintf("%dh", int(age/time.Hour))
	default:
		return fmt.Sprintf("%dd", int(age/(24*time.Hour)))
	}
}
