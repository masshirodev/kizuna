package main

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// openCodeReadSchema is the part of the real opencode.db the reader touches,
// including the columns the merge tests' minimal schema leaves out: the message
// and part JSON blobs, and the session's time_updated.
const openCodeReadSchema = `
CREATE TABLE session (id text PRIMARY KEY, title text NOT NULL, directory text NOT NULL, time_created integer NOT NULL, time_updated integer NOT NULL);
CREATE TABLE message (id text PRIMARY KEY, session_id text NOT NULL, time_created integer NOT NULL, data text NOT NULL);
CREATE TABLE part (id text PRIMARY KEY, message_id text NOT NULL, session_id text NOT NULL, time_created integer NOT NULL, data text NOT NULL);
`

func writeReadDB(t *testing.T, path string, stmts ...string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(openCodeReadSchema); err != nil {
		t.Fatal(err)
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
}

func TestReadOpenCodeMessagesKeepsProseInOrder(t *testing.T) {
	db := filepath.Join(t.TempDir(), "opencode.db")
	writeReadDB(t, db,
		`INSERT INTO message VALUES ('m1','s1',10,'{"role":"user"}')`,
		`INSERT INTO message VALUES ('m2','s1',20,'{"role":"assistant"}')`,
		`INSERT INTO part VALUES ('p1','m1','s1',10,'{"type":"text","text":"do the thing"}')`,
		`INSERT INTO part VALUES ('p2','m2','s1',20,'{"type":"reasoning","text":"thinking out loud"}')`,
		`INSERT INTO part VALUES ('p3','m2','s1',21,'{"type":"tool","tool":"bash"}')`,
		`INSERT INTO part VALUES ('p4','m2','s1',22,'{"type":"text","text":"done"}')`,
		`INSERT INTO message VALUES ('m9','s2',30,'{"role":"user"}')`,
		`INSERT INTO part VALUES ('p9','m9','s2',30,'{"type":"text","text":"a different session"}')`,
	)
	messages, err := readOpenCodeMessages(db, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 {
		t.Fatalf("messages = %d, want 2 (prose only): %v", len(messages), messages)
	}
	if !messages[0].fromUser || messages[0].text != "do the thing" {
		t.Fatalf("first = %+v", messages[0])
	}
	if messages[1].fromUser || messages[1].text != "done" {
		t.Fatalf("second = %+v", messages[1])
	}
}

func TestDecodeOpenCodePartDropsWhatWasNotSaid(t *testing.T) {
	cases := []struct {
		name    string
		message string
		part    string
	}{
		{"tool part", `{"role":"assistant"}`, `{"type":"tool"}`},
		{"reasoning", `{"role":"assistant"}`, `{"type":"reasoning","text":"hmm"}`},
		{"system role", `{"role":"system"}`, `{"type":"text","text":"hi"}`},
		{"injected preamble", `{"role":"user"}`, `{"type":"text","text":"<system-reminder>context"}`},
		{"empty text", `{"role":"user"}`, `{"type":"text","text":"   "}`},
	}
	for _, test := range cases {
		if _, ok := decodeOpenCodePart([]byte(test.message), []byte(test.part)); ok {
			t.Fatalf("%s was kept as a message", test.name)
		}
	}
	if message, ok := decodeOpenCodePart([]byte(`{"role":"user"}`), []byte(`{"type":"text","text":"  hello  "}`)); !ok || message.text != "hello" {
		t.Fatalf("a plain prompt = %+v, ok %v", message, ok)
	}
}

func TestFreshestStorePicksTheNewestCopy(t *testing.T) {
	stale := filepath.Join(t.TempDir(), "stale.db")
	fresh := filepath.Join(t.TempDir(), "fresh.db")
	writeReadDB(t, stale, `INSERT INTO session VALUES ('s1','t','/w',1,5)`)
	writeReadDB(t, fresh, `INSERT INTO session VALUES ('s1','t','/w',1,50)`)
	if got, ok := freshestStore([]string{stale, fresh}, "s1"); !ok || got != fresh {
		t.Fatalf("store = %q, ok %v; want the fresher %q", got, ok, fresh)
	}
	if _, ok := freshestStore([]string{stale, fresh}, "nope"); ok {
		t.Fatal("a session no copy holds was found")
	}
}

func TestOpencodeStoreForFindsTheProfileStore(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	workdir := profileWorkdir(t, root, "oc")
	db := filepath.Join(workdir, "data", "opencode", "opencode.db")
	writeReadDB(t, db, `INSERT INTO session VALUES ('s1','t','/w',1,9)`)
	profile := Profile{Name: "oc", Provider: "opencode"}
	if got, ok := opencodeStoreFor(profile, "s1"); !ok || got != db {
		t.Fatalf("store = %q, ok %v; want %q", got, ok, db)
	}
}

// TestHandoffFromOpenCodeReadsItsStore is the end-to-end shape the feature
// exists for: a session that has no transcript file still produces a brief, and
// the brief points at the store rather than at a file.
func TestHandoffFromOpenCodeReadsItsStore(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	workdir := profileWorkdir(t, root, "oc")
	db := filepath.Join(workdir, "data", "opencode", "opencode.db")
	writeReadDB(t, db,
		`INSERT INTO session VALUES ('s1','Build the thing','/work',1,9)`,
		`INSERT INTO message VALUES ('m1','s1',10,'{"role":"user"}')`,
		`INSERT INTO message VALUES ('m2','s1',20,'{"role":"assistant"}')`,
		`INSERT INTO part VALUES ('p1','m1','s1',10,'{"type":"text","text":"build the thing"}')`,
		`INSERT INTO part VALUES ('p2','m2','s1',20,'{"type":"text","text":"finished"}')`,
	)
	profile := Profile{Name: "oc", Provider: "opencode", Command: "opencode"}
	record := recordedSession{session: instanceSession{id: "s1", title: "Build the thing"}, folder: "/work"}
	brief, err := buildBrief(profile, record)
	if err != nil {
		t.Fatalf("buildBrief: %v", err)
	}
	if len(brief.prompts) != 1 || brief.prompts[0] != "build the thing" {
		t.Fatalf("prompts = %v", brief.prompts)
	}
	if brief.transcript.store != db || brief.transcript.sessionID != "s1" {
		t.Fatalf("transcript ref = %+v, want store %q and session s1", brief.transcript, db)
	}
	body := renderBrief(brief, gitState{}, time.Now())
	if !strings.Contains(body, "sqlite3") || !strings.Contains(body, "s1") || !strings.Contains(body, db) {
		t.Fatalf("brief does not point at the store:\n%s", body)
	}
}

func TestOpenCodePreviewReadsTheSession(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	workdir := profileWorkdir(t, root, "oc")
	db := filepath.Join(workdir, "data", "opencode", "opencode.db")
	writeReadDB(t, db,
		`INSERT INTO session VALUES ('s1','t','/w',1,9)`,
		`INSERT INTO message VALUES ('m1','s1',10,'{"role":"user"}')`,
		`INSERT INTO part VALUES ('p1','m1','s1',10,'{"type":"text","text":"hello"}')`,
	)
	profile := Profile{Name: "oc", Provider: "opencode"}
	preview := readSessionPreview(profile, recordedSession{session: instanceSession{id: "s1"}})
	if preview.problem != "" {
		t.Fatalf("preview problem = %q", preview.problem)
	}
	if len(preview.messages) != 1 || preview.messages[0].text != "hello" {
		t.Fatalf("preview messages = %+v", preview.messages)
	}
}
