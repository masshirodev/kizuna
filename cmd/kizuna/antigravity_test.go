package main

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// pb builds protobuf wire bytes for fixtures: a field is its number and
// either a string/bytes (length-delimited), a nested message ([]byte from
// pb), or a uint64 (varint).
type pbField struct {
	number int
	value  any
}

func pbVarint(value uint64) []byte {
	var out []byte
	for value >= 0x80 {
		out = append(out, byte(value)|0x80)
		value >>= 7
	}
	return append(out, byte(value))
}

func pb(fields ...pbField) []byte {
	var out []byte
	for _, field := range fields {
		switch value := field.value.(type) {
		case uint64:
			out = append(out, pbVarint(uint64(field.number)<<3)...)
			out = append(out, pbVarint(value)...)
		case string:
			out = append(out, pbVarint(uint64(field.number)<<3|2)...)
			out = append(out, pbVarint(uint64(len(value)))...)
			out = append(out, value...)
		case []byte:
			out = append(out, pbVarint(uint64(field.number)<<3|2)...)
			out = append(out, pbVarint(uint64(len(value)))...)
			out = append(out, value...)
		}
	}
	return out
}

func userStep(text string) []byte {
	// 19.2 is what was typed; 19.3.1 repeats it and must not double it.
	return pb(pbField{5, pb(pbField{12, "trajectory-id"})},
		pbField{19, pb(pbField{2, text}, pbField{3, pb(pbField{1, text})})})
}

func replyStep(text string) []byte {
	// 20.1 is the reply; 20.3 is thinking and 20.7.3 a tool call's arguments.
	return pb(pbField{20, pb(pbField{3, "Thinking about " + text},
		pbField{7, pb(pbField{3, `{"CommandLine":"ls"}`})}, pbField{1, text}, pbField{8, text})})
}

func antigravityFixture(t *testing.T) (Profile, string) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	profile := Profile{Name: "ag", Provider: "antigravity", Command: "agy"}
	home := filepath.Join(root, appName, "profiles", profile.Name, "home", ".gemini", "antigravity-cli")
	writeRawDB(t, filepath.Join(home, "conversation_summaries.db"),
		`CREATE TABLE conversation_summaries (conversation_id text PRIMARY KEY, title text NOT NULL DEFAULT '',
			workspace_uris text NOT NULL, last_modified_time datetime NOT NULL, nesting_depth integer NOT NULL DEFAULT 0)`,
		`INSERT INTO conversation_summaries VALUES ('conv-talk', 'Fix The Screenshot Tool', '["file:///work/dots"]', '2026-09-27 20:11:12.142530531+00:00', 0)`,
		`INSERT INTO conversation_summaries VALUES ('conv-call', 'Chapter Summary', '', '2026-09-01 02:02:57.874348168+00:00', 0)`,
		`INSERT INTO conversation_summaries VALUES ('conv-sub', 'A subagent', '["file:///work/dots"]', '2026-09-27 20:00:00+00:00', 1)`)
	started := uint64(time.Date(2026, 9, 27, 19, 14, 48, 0, time.UTC).Unix())
	conversation := filepath.Join(home, "conversations", "conv-talk.db")
	writeRawDB(t, conversation,
		`CREATE TABLE steps (idx integer PRIMARY KEY, step_type integer NOT NULL, metadata blob, step_payload blob)`)
	db, err := sql.Open("sqlite", conversation)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	metadata := pb(pbField{1, pb(pbField{1, started})})
	for index, step := range []struct {
		kind    int
		payload []byte
	}{
		{antigravityUserStep, userStep("rewrite my screenshot script")},
		{antigravityReplyStep, replyStep("Rewritten to use Flameshot.")},
		{132, pb(pbField{2, "tool output nobody said"})},
		{antigravityUserStep, userStep("hide the cursor")},
		{antigravityReplyStep, replyStep("The cursor is hidden now.")},
	} {
		if _, err := db.Exec(`INSERT INTO steps VALUES (?, ?, ?, ?)`, index, step.kind, metadata, step.payload); err != nil {
			t.Fatal(err)
		}
	}
	return profile, conversation
}

func TestProtoPathWalkFindsFieldsAndSurvivesGarbage(t *testing.T) {
	message := pb(pbField{1, pb(pbField{1, uint64(1790000000)})}, pbField{19, pb(pbField{2, "typed"})})
	if text, ok := protoString(message, 19, 2); !ok || text != "typed" {
		t.Fatalf("19.2 = %q, %v", text, ok)
	}
	if seconds, ok := protoVarint(message, 1, 1); !ok || seconds != 1790000000 {
		t.Fatalf("1.1 = %d, %v", seconds, ok)
	}
	if _, ok := protoString(message, 19, 9); ok {
		t.Fatal("found a field that is not there")
	}
	for _, garbage := range [][]byte{nil, {0xff}, {0x0a, 0x7f, 0x01}, {0x07}, []byte("not protobuf at all")} {
		if _, ok := protoString(garbage, 19, 2); ok {
			t.Fatalf("garbage %x read as a string", garbage)
		}
	}
}

func TestAntigravityConversationsListFromTheirSummaries(t *testing.T) {
	profile, _ := antigravityFixture(t)
	records := recentSessions(profile, 0)
	if len(records) != 2 {
		t.Fatalf("recent = %+v, want the two top-level conversations", records)
	}
	byID := map[string]recordedSession{}
	for _, record := range records {
		byID[record.session.id] = record
	}
	talk := byID["conv-talk"]
	if talk.session.title != "Fix The Screenshot Tool" || talk.folder != "/work/dots" || talk.headless {
		t.Fatalf("interactive conversation = %+v", talk)
	}
	if want := time.Date(2026, 9, 27, 19, 14, 48, 0, time.UTC); !talk.when.Equal(want) {
		t.Fatalf("started %v, want the first step's time %v", talk.when, want)
	}
	if want := time.Date(2026, 9, 27, 20, 11, 12, 142530531, time.UTC); !talk.lastActive.Equal(want) {
		t.Fatalf("last active %v, want %v", talk.lastActive, want)
	}
	// A prompt handed over by a program has no workspace: headless.
	if call := byID["conv-call"]; !call.headless || call.folder != "" {
		t.Fatalf("workspace-less conversation = %+v, want headless", call)
	}
}

func TestAntigravityTurnsAreWhatWasSaidAndNothingElse(t *testing.T) {
	profile, conversation := antigravityFixture(t)
	messages, ref, err := readSessionMessages(profile, recordedSession{session: instanceSession{id: "conv-talk"}})
	if err != nil {
		t.Fatal(err)
	}
	want := []handoffMessage{
		{fromUser: true, text: "rewrite my screenshot script"},
		{fromUser: false, text: "Rewritten to use Flameshot."},
		{fromUser: true, text: "hide the cursor"},
		{fromUser: false, text: "The cursor is hidden now."},
	}
	if len(messages) != len(want) {
		t.Fatalf("turns = %+v, want %d", messages, len(want))
	}
	for index := range want {
		if messages[index] != want[index] {
			t.Fatalf("turn %d = %+v, want %+v", index, messages[index], want[index])
		}
	}
	if !ref.protobuf || ref.path != conversation {
		t.Fatalf("ref = %+v, want the conversation store marked protobuf", ref)
	}
	if preview := readSessionPreview(profile, recordedSession{session: instanceSession{id: "conv-talk"}}); len(preview.messages) != 4 || preview.problem != "" {
		t.Fatalf("preview = %+v", preview)
	}
}

func TestAntigravityTurnsAreCachedOnceRead(t *testing.T) {
	profile, conversation := antigravityFixture(t)
	record := recordedSession{session: instanceSession{id: "conv-talk"}}
	recentSessions(profile, 0) // the index must know the row for a cache to attach to
	if _, _, err := readSessionMessages(profile, record); err != nil {
		t.Fatal(err)
	}
	// The store is gone; a cached conversation still reads.
	if err := os.Remove(conversation); err != nil {
		t.Fatal(err)
	}
	if _, ok := cachedSessionTurns(profile.Name, "conv-talk"); !ok {
		t.Fatal("turns read once were not cached")
	}
}

func TestBriefPointsAtAnAntigravityStoreAsProtobuf(t *testing.T) {
	brief := sessionBrief{transcript: transcriptRef{path: "/x/conv.db", protobuf: true}}
	out := renderBrief(brief, gitState{}, time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC))
	if !strings.Contains(out, "Antigravity's own store at `/x/conv.db`") || strings.Contains(out, "grep it") {
		t.Fatalf("brief tail:\n%s", out[strings.Index(out, "## If you need more"):])
	}
}
