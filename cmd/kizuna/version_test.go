package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestUpdateStatusMessage(t *testing.T) {
	cases := []struct {
		status updateStatus
		want   string
	}{
		{updateStatus{Known: true}, "up to date with main"},
		{updateStatus{Known: true, Behind: 1}, "1 commit behind main · go install ./..."},
		{updateStatus{Known: true, Behind: 4}, "4 commits behind main · go install ./..."},
		{updateStatus{Reason: "github could not be reached"}, "update check unavailable: github could not be reached"},
	}
	for _, testCase := range cases {
		if got := testCase.status.message(); got != testCase.want {
			t.Errorf("message() = %q, want %q", got, testCase.want)
		}
	}
	if (updateStatus{Known: true}).available() {
		t.Error("an up-to-date build should offer no update")
	}
	if (updateStatus{Behind: 3}).available() {
		t.Error("a failed check must not be reported as an available update")
	}
}

func TestFetchUpdateStatusReadsBehindCount(t *testing.T) {
	t.Setenv("GH_TOKEN", "test-token")
	var gotPath, gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth = r.URL.Path, r.Header.Get("Authorization")
		w.Write([]byte(`{"status":"behind","ahead_by":0,"behind_by":3}`))
	}))
	defer server.Close()
	updateAPIBase = server.URL
	defer func() { updateAPIBase = "https://api.github.com" }()

	status := fetchUpdateStatus(context.Background(), "abc123", time.Now())
	if !status.Known || status.Behind != 3 {
		t.Fatalf("status = %+v, want 3 commits behind", status)
	}
	if !strings.HasSuffix(gotPath, "/compare/main...abc123") {
		t.Fatalf("compared the wrong direction: %q", gotPath)
	}
	if gotAuth != "Bearer test-token" {
		t.Fatalf("token was not sent: %q", gotAuth)
	}
}

// Every failure is a status carrying its reason: a launcher must not be blocked
// by an update check, and must never claim an update it could not confirm.
func TestFetchUpdateStatusReportsFailuresWithoutClaimingAnUpdate(t *testing.T) {
	t.Setenv("GH_TOKEN", "test-token")
	cases := []struct {
		code int
		want string
	}{
		{http.StatusNotFound, "not on " + updateRepo},
		{http.StatusUnauthorized, "cannot read " + updateRepo},
		{http.StatusForbidden, "cannot read " + updateRepo},
		{http.StatusInternalServerError, "github answered"},
	}
	for _, testCase := range cases {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(testCase.code)
		}))
		updateAPIBase = server.URL
		status := fetchUpdateStatus(context.Background(), "abc123", time.Now())
		server.Close()
		if status.Known || status.available() {
			t.Errorf("HTTP %d produced a usable status: %+v", testCase.code, status)
		}
		if !strings.Contains(status.Reason, testCase.want) {
			t.Errorf("HTTP %d reason = %q, want it to mention %q", testCase.code, status.Reason, testCase.want)
		}
	}
	updateAPIBase = "https://api.github.com"
}

func TestUpdateCacheExpiresAndFollowsTheBuild(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	now := time.Now()
	storeUpdateStatus(updateStatus{Revision: "abc123", Behind: 2, Known: true, CheckedAt: now})

	if status, ok := cachedUpdateStatus("abc123", now.Add(time.Hour)); !ok || status.Behind != 2 {
		t.Fatalf("fresh entry was not reused: %+v (ok=%v)", status, ok)
	}
	if _, ok := cachedUpdateStatus("abc123", now.Add(updateCacheTTL)); ok {
		t.Error("an entry older than the TTL was reused")
	}
	// A cache entry describing a different build says nothing about this one.
	if _, ok := cachedUpdateStatus("def456", now.Add(time.Hour)); ok {
		t.Error("an entry for another build was reused")
	}
}

func TestCheckForUpdateWithoutABuildRevision(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	// go test binaries carry no vcs stamp, which is exactly the case being
	// covered: the check must explain itself instead of failing silently.
	if currentBuild().revision != "" {
		t.Skip("test binary carries a build revision")
	}
	status := checkForUpdate(false, time.Now())
	if status.Known || !strings.Contains(status.Reason, "go install") {
		t.Fatalf("status = %+v, want an explanation naming the fix", status)
	}
}

func TestBuildStampShort(t *testing.T) {
	if got := (buildStamp{revision: "696fc1c3efb6ac39"}).short(); got != "696fc1c" {
		t.Errorf("short() = %q", got)
	}
	if got := (buildStamp{revision: "696fc1c3efb6ac39", modified: true}).short(); got != "696fc1c+dirty" {
		t.Errorf("short() = %q", got)
	}
	if got := (buildStamp{}).short(); got != "" {
		t.Errorf("short() = %q, want empty for an unstamped build", got)
	}
}
