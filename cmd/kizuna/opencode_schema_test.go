package main

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// openCodeV2Schema is the part of a fresh OpenCode 2.x store the readers and
// the merge touch: session_v2 (whose title may be NULL) and session_message,
// one row a turn, with no legacy session, message or part table at all.
const openCodeV2Schema = `
CREATE TABLE session_v2 (id text PRIMARY KEY, project_id text NOT NULL, parent_id text, title text, directory text NOT NULL, permission text, time_created integer NOT NULL, time_updated integer NOT NULL);
CREATE TABLE session_message (id text PRIMARY KEY, session_id text NOT NULL, type text NOT NULL, seq integer NOT NULL, time_created integer NOT NULL, time_updated integer NOT NULL, data text NOT NULL);
`

// openCodeV2MigratedSchema is the same session_v2 as a store 2.x migrated
// from 1.x carries it: the columns a migration added come last, so the
// order differs from a fresh store's while the set is the same.
const openCodeV2MigratedSchema = `
CREATE TABLE session_v2 (id text PRIMARY KEY, project_id text NOT NULL, title text, directory text NOT NULL, time_created integer NOT NULL, time_updated integer NOT NULL, parent_id text, permission text);
CREATE TABLE session_message (id text PRIMARY KEY, session_id text NOT NULL, type text NOT NULL, seq integer NOT NULL, time_created integer NOT NULL, time_updated integer NOT NULL, data text NOT NULL);
`

func writeSchemaDB(t *testing.T, path, schema string, stmts ...string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(schema); err != nil {
		t.Fatal(err)
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
}

func TestOpenCodeSessionTablePrefersV2(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name, schema, want string
	}{
		{"fresh 2.x", openCodeV2Schema, openCodeSessionTableV2},
		{"1.x", openCodeReadSchema, openCodeSessionTableLegacy},
		{"migrated: both", openCodeReadSchema + openCodeV2Schema, openCodeSessionTableV2},
		{"neither", `CREATE TABLE kv (k text)`, ""},
	}
	for index, c := range cases {
		path := filepath.Join(dir, string(rune('a'+index))+".db")
		writeSchemaDB(t, path, c.schema)
		if got := openCodeStoreSessionTable(path); got != c.want {
			t.Errorf("%s: session table = %q, want %q", c.name, got, c.want)
		}
	}
	if got := openCodeStoreSessionTable(filepath.Join(dir, "missing", "x.db")); got != "" {
		t.Errorf("a store that cannot open names %q", got)
	}
}

func TestQueryOpenCodeSessionsReadsAFresh2xStore(t *testing.T) {
	db := filepath.Join(t.TempDir(), "opencode.db")
	writeSchemaDB(t, db, openCodeV2Schema,
		`INSERT INTO session_v2 VALUES ('s1','p','', 'older','/work',NULL,1000,1500)`,
		`INSERT INTO session_v2 VALUES ('s2','p','', NULL,'/work','[{"permission":"question","pattern":"*","action":"deny"}]',2000,3000)`,
	)
	records, err := queryOpenCodeSessions(db, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 {
		t.Fatalf("records = %d, want 2", len(records))
	}
	if records[0].session.id != "s2" || records[0].session.title != "" || !records[0].headless {
		t.Fatalf("newest = %+v, want s2, untitled, headless", records[0])
	}
	if records[1].session.id != "s1" || records[1].session.title != "older" || records[1].headless || records[1].folder != "/work" {
		t.Fatalf("second = %+v", records[1])
	}
	if records[1].lastActive.UnixMilli() != 1500 {
		t.Fatalf("lastActive = %d, want time_updated", records[1].lastActive.UnixMilli())
	}
}

func TestQueryOpenCodeSessionsRefusesAStoreWithNoSessions(t *testing.T) {
	db := filepath.Join(t.TempDir(), "opencode.db")
	writeSchemaDB(t, db, `CREATE TABLE kv (k text)`)
	if _, err := queryOpenCodeSessions(db, 0); err == nil {
		t.Fatal("a store with no session table listed without error")
	}
}

func TestReadOpenCodeMessagesReadsSessionMessage(t *testing.T) {
	db := filepath.Join(t.TempDir(), "opencode.db")
	writeSchemaDB(t, db, openCodeV2Schema,
		// Inserted out of order: seq, not the row id or the clock, is the order.
		`INSERT INTO session_message VALUES ('t3','s1','assistant',3,10,10,'{"content":[{"type":"reasoning","text":"hmm"},{"type":"tool","name":"bash"},{"type":"text","text":"done"},{"type":"text","text":"and tested"}]}')`,
		`INSERT INTO session_message VALUES ('t1','s1','user',1,10,10,'{"text":"do the thing","files":[]}')`,
		`INSERT INTO session_message VALUES ('t2','s1','system',2,10,10,'{"text":"catalog changed"}')`,
		`INSERT INTO session_message VALUES ('t4','s1','synthetic',4,10,10,'{"text":"<shell>output</shell>"}')`,
		`INSERT INTO session_message VALUES ('t5','s1','idle',5,10,10,'{"outcome":"succeeded"}')`,
		`INSERT INTO session_message VALUES ('t6','s1','assistant',6,10,10,'{"content":[{"type":"tool","name":"bash"}]}')`,
		`INSERT INTO session_message VALUES ('t7','s1','user',7,10,10,'not json')`,
		`INSERT INTO session_message VALUES ('t9','s2','user',1,10,10,'{"text":"a different session"}')`,
	)
	messages, err := readOpenCodeMessages(db, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 {
		t.Fatalf("messages = %d, want 2 (prose only): %+v", len(messages), messages)
	}
	if !messages[0].fromUser || messages[0].text != "do the thing" {
		t.Fatalf("first = %+v", messages[0])
	}
	if messages[1].fromUser || messages[1].text != "done\n\nand tested" {
		t.Fatalf("second = %+v", messages[1])
	}
}

func TestReadOpenCodeMessagesFallsBackToLegacyTurns(t *testing.T) {
	// A migrated store: s1 has v2 turns, s-old only legacy parts.
	db := filepath.Join(t.TempDir(), "opencode.db")
	writeSchemaDB(t, db, openCodeReadSchema+openCodeV2Schema,
		`INSERT INTO session_message VALUES ('t1','s1','user',1,10,10,'{"text":"from v2"}')`,
		`INSERT INTO message VALUES ('m1','s1',10,'{"role":"user"}')`,
		`INSERT INTO part VALUES ('p1','m1','s1',10,'{"type":"text","text":"from legacy"}')`,
		`INSERT INTO message VALUES ('m2','s-old',10,'{"role":"user"}')`,
		`INSERT INTO part VALUES ('p2','m2','s-old',10,'{"type":"text","text":"only legacy"}')`,
	)
	if messages, err := readOpenCodeMessages(db, "s1"); err != nil || len(messages) != 1 || messages[0].text != "from v2" {
		t.Fatalf("s1 = %+v, %v; want the v2 turn only", messages, err)
	}
	if messages, err := readOpenCodeMessages(db, "s-old"); err != nil || len(messages) != 1 || messages[0].text != "only legacy" {
		t.Fatalf("s-old = %+v, %v; want the legacy part", messages, err)
	}
}

func TestReadOpenCodeMessagesOnAFresh2xStoreWithNoTurns(t *testing.T) {
	db := filepath.Join(t.TempDir(), "opencode.db")
	writeSchemaDB(t, db, openCodeV2Schema)
	messages, err := readOpenCodeMessages(db, "nobody")
	if err != nil || len(messages) != 0 {
		t.Fatalf("messages = %+v, %v; want none and no error (there is no part table to fall back to)", messages, err)
	}
}

func TestOpenCodeResumeLookupsRead2x(t *testing.T) {
	db := filepath.Join(t.TempDir(), "opencode.db")
	writeSchemaDB(t, db, openCodeV2Schema,
		`INSERT INTO session_v2 VALUES ('top-old','p',NULL,'a','/work',NULL,1,10)`,
		`INSERT INTO session_v2 VALUES ('top-new','p',NULL,'b','/work',NULL,2,20)`,
		`INSERT INTO session_v2 VALUES ('child','p','top-new','c','/work',NULL,3,30)`,
		`INSERT INTO session_v2 VALUES ('elsewhere','p',NULL,'d','/other',NULL,4,40)`,
	)
	if got := newestOpenCodeSessionIn(db, "/work"); got != "top-new" {
		t.Fatalf("newest in /work = %q, want top-new (a child session is never resumed on its own)", got)
	}
	if directory, ok := openCodeSessionDirectory(db, "elsewhere"); !ok || directory != "/other" {
		t.Fatalf("directory = %q, %v", directory, ok)
	}
	if updated, ok := opencodeSessionUpdated(db, "child"); !ok || updated != 30 {
		t.Fatalf("updated = %d, %v", updated, ok)
	}

	empty := filepath.Join(t.TempDir(), "opencode.db")
	writeSchemaDB(t, empty, `CREATE TABLE kv (k text)`)
	if got := newestOpenCodeSessionIn(empty, "/work"); got != "" {
		t.Fatalf("a store with no sessions resumed %q", got)
	}
	if _, ok := openCodeSessionDirectory(empty, "x"); ok {
		t.Fatal("a store with no sessions knew a directory")
	}
	if _, ok := opencodeSessionUpdated(empty, "x"); ok {
		t.Fatal("a store with no sessions knew a time")
	}
}

func TestOpenCodeSessionInProfileDBReads2x(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	profile := Profile{Name: "oc", Provider: "opencode", Command: "opencode"}
	workdir := profileWorkdir(t, root, profile.Name)
	writeSchemaDB(t, filepath.Join(workdir, "data", "opencode", "opencode.db"), openCodeV2Schema,
		`INSERT INTO session_v2 VALUES ('s1','p',NULL,'a','/work',NULL,1,10)`)
	if !opencodeSessionInProfileDB(profile, "s1") {
		t.Fatal("a 2.x session in the profile store was not found")
	}
	if opencodeSessionInProfileDB(profile, "s2") {
		t.Fatal("a session the store does not hold was found")
	}
}

func TestTranscriptCommandFollowsTheStoreSchema(t *testing.T) {
	dir := t.TempDir()
	v2 := filepath.Join(dir, "v2.db")
	writeSchemaDB(t, v2, openCodeV2Schema)
	legacy := filepath.Join(dir, "legacy.db")
	writeSchemaDB(t, legacy, openCodeReadSchema)

	got := transcriptRef{store: v2, sessionID: "s1"}.command()
	if !strings.Contains(got, "FROM session_message") || !strings.Contains(got, "ORDER BY seq") || !strings.Contains(got, "s1") {
		t.Fatalf("2.x command = %s", got)
	}
	got = transcriptRef{store: legacy, sessionID: "s1"}.command()
	if !strings.Contains(got, "FROM part p JOIN message m") {
		t.Fatalf("1.x command = %s", got)
	}
}

func TestMergeCarries2xSessionsAcrossColumnOrder(t *testing.T) {
	dir := t.TempDir()
	profileDB := filepath.Join(dir, "profile.db")
	instanceDB := filepath.Join(dir, "instance.db")
	// The archive was migrated (columns in migration order); the instance is a
	// fresh 2.x store (columns in creation order).
	writeSchemaDB(t, profileDB, openCodeV2MigratedSchema,
		`INSERT INTO session_v2 (id, project_id, title, directory, time_created, time_updated) VALUES ('kept','p','old title','/work',1,10)`)
	writeSchemaDB(t, instanceDB, openCodeV2Schema,
		`INSERT INTO session_v2 VALUES ('kept','p',NULL,'continued','/work',NULL,1,20)`,
		`INSERT INTO session_v2 VALUES ('new','p',NULL,'fresh','/work',NULL,2,30)`,
		`INSERT INTO session_message VALUES ('t1','new','user',1,30,30,'{"text":"hello"}')`)

	sessions, warnings, err := mergeInstanceDatabase(profileDB, instanceDB)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v; column order is not schema drift", warnings)
	}
	if sessions != 1 {
		t.Fatalf("sessions = %d, want the one new session", sessions)
	}
	if got := countRows(t, profileDB, "session_v2"); got != 2 {
		t.Fatalf("archive holds %d sessions, want 2", got)
	}
	if got := queryCell(t, profileDB, `SELECT title FROM session_v2 WHERE id = 'kept'`); got != "continued" {
		t.Fatalf("resumed session kept title %q; the newer row should win", got)
	}
	if got := countRows(t, profileDB, "session_message"); got != 1 {
		t.Fatalf("archive holds %d turns, want 1", got)
	}
}

func TestMergeAnnouncesNothingWhenNoSessionTableIsShared(t *testing.T) {
	// A 1.x archive and a 2.x instance share no session table: nothing to
	// count, and nothing announced.
	dir := t.TempDir()
	profileDB := filepath.Join(dir, "profile.db")
	instanceDB := filepath.Join(dir, "instance.db")
	writeSchemaDB(t, profileDB, openCodeReadSchema)
	writeSchemaDB(t, instanceDB, openCodeV2Schema,
		`INSERT INTO session_v2 VALUES ('new','p',NULL,'fresh','/work',NULL,2,30)`)
	sessions, _, err := mergeInstanceDatabase(profileDB, instanceDB)
	if err != nil {
		t.Fatal(err)
	}
	if sessions != 0 {
		t.Fatalf("sessions = %d, want 0", sessions)
	}
}

func TestCountSessionsInReads2x(t *testing.T) {
	db := filepath.Join(t.TempDir(), "opencode.db")
	writeSchemaDB(t, db, openCodeV2Schema,
		`INSERT INTO session_v2 VALUES ('a','p',NULL,'a','/w',NULL,1,1)`,
		`INSERT INTO session_v2 VALUES ('b','p',NULL,'b','/w',NULL,1,1)`)
	if count, _, _ := countSessionsIn(db); count != 2 {
		t.Fatalf("count = %d, want 2", count)
	}
	empty := filepath.Join(t.TempDir(), "opencode.db")
	writeSchemaDB(t, empty, `CREATE TABLE kv (k text)`)
	if count, _, _ := countSessionsIn(empty); count != 0 {
		t.Fatalf("count = %d in a store with no sessions", count)
	}
}

func TestSameColumnsIgnoresOrderNotContent(t *testing.T) {
	if !sameColumns([]string{"a", "b", "c"}, []string{"c", "a", "b"}) {
		t.Fatal("same set in another order is drift")
	}
	if sameColumns([]string{"a", "b"}, []string{"a", "b", "c"}) {
		t.Fatal("an added column is not drift")
	}
	input := []string{"b", "a"}
	sameColumns(input, []string{"a", "b"})
	if input[0] != "b" {
		t.Fatal("sameColumns reordered its caller's slice")
	}
}
