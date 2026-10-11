package main

import (
	"database/sql"
	"errors"
	"net/url"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

// Antigravity keeps one SQLite store per conversation under
// ~/.gemini/antigravity-cli/conversations, and a conversation_summaries.db
// beside them. The summaries carry what a list needs as plain columns --
// title, workspace, last modified -- so listing costs no decoding at all. What
// is protobuf is each conversation's steps, and that is only read for the
// turns (a handoff, a preview) and for when the conversation began.
//
// There is no published schema. The field paths below were read off the
// workstation's own conversations (2026-09-28) and hold across every one of
// them; doc/session-store.md records how they were found. A path that stops
// matching reads as nothing said, never as the wrong thing said.
const (
	// antigravityUserStep is a step the user typed; its text is at 19.2.
	antigravityUserStep = 14
	// antigravityReplyStep is a step the model answered; its prose is at
	// 20.1. The same step carries its thinking at 20.3 and tool-call
	// arguments at 20.7.3, which are not something anyone said.
	antigravityReplyStep = 15
)

var (
	antigravityUserText  = []int{19, 2}
	antigravityReplyText = []int{20, 1}
	// antigravityStartedAt is the first step's creation time, a
	// google.protobuf.Timestamp whose seconds are field 1 of field 1.
	antigravityStartedAt = []int{1, 1}
)

// antigravityHome is where an Antigravity profile's CLI keeps its state: the
// profile's private HOME, then the CLI's own directory under it.
func antigravityHome(profile Profile) (string, error) {
	root, err := profileRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, profile.Name, "home", ".gemini", "antigravity-cli"), nil
}

// antigravitySessions lists a profile's conversations from its summaries,
// newest activity first. A conversation with no workspace was a prompt handed
// to the CLI by a program (the workstation's six are all a reading tool's
// calls), so it is headless.
func antigravitySessions(profile Profile) ([]recordedSession, error) {
	home, err := antigravityHome(profile)
	if err != nil {
		return nil, err
	}
	summaries := filepath.Join(home, "conversation_summaries.db")
	if !fileExists(summaries) {
		return nil, nil
	}
	db, err := sql.Open("sqlite", "file:"+summaries+"?mode=ro")
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := db.Query(`SELECT conversation_id, title, workspace_uris, last_modified_time
		FROM conversation_summaries WHERE nesting_depth = 0 ORDER BY last_modified_time DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var records []recordedSession
	for rows.Next() {
		var id, title, workspaces, modified string
		if rows.Scan(&id, &title, &workspaces, &modified) != nil {
			continue
		}
		folder := firstWorkspaceFolder(workspaces)
		record := recordedSession{
			session:    instanceSession{id: id, title: summariseTitle(title)},
			folder:     folder,
			lastActive: parseAntigravityTime(modified),
			headless:   folder == "",
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

// antigravityStartTime reads when a conversation began off its first step.
// The summaries only say when it was last touched.
func antigravityStartTime(profile Profile, id string) time.Time {
	db, ok := openAntigravityConversation(profile, id)
	if !ok {
		return time.Time{}
	}
	defer db.Close()
	var metadata []byte
	if db.QueryRow(`SELECT metadata FROM steps ORDER BY idx LIMIT 1`).Scan(&metadata) != nil {
		return time.Time{}
	}
	if seconds, ok := protoVarint(metadata, antigravityStartedAt...); ok && seconds > 0 {
		return time.Unix(int64(seconds), 0)
	}
	return time.Time{}
}

// readAntigravityMessages reads what was said in one conversation, in order:
// the user's inputs and the model's replies, nothing else.
func readAntigravityMessages(profile Profile, id string) ([]handoffMessage, error) {
	db, ok := openAntigravityConversation(profile, id)
	if !ok {
		return nil, errors.New("this conversation is not in the Antigravity store on disk")
	}
	defer db.Close()
	rows, err := db.Query(`SELECT step_type, step_payload FROM steps
		WHERE step_type IN (?, ?) ORDER BY idx`, antigravityUserStep, antigravityReplyStep)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	messages := []handoffMessage{}
	for rows.Next() {
		var stepType int
		var payload []byte
		if rows.Scan(&stepType, &payload) != nil {
			continue
		}
		path, fromUser := antigravityReplyText, false
		if stepType == antigravityUserStep {
			path, fromUser = antigravityUserText, true
		}
		if text, ok := protoString(payload, path...); ok && strings.TrimSpace(text) != "" {
			if fromUser {
				text = userText(text)
			}
			messages = append(messages, handoffMessage{fromUser: fromUser, text: text})
		}
	}
	return messages, rows.Err()
}

// antigravityConversationPath is one conversation's own store.
func antigravityConversationPath(profile Profile, id string) (string, bool) {
	home, err := antigravityHome(profile)
	if err != nil || id == "" || strings.ContainsAny(id, `/\`) {
		return "", false
	}
	path := filepath.Join(home, "conversations", id+".db")
	return path, fileExists(path)
}

func openAntigravityConversation(profile Profile, id string) (*sql.DB, bool) {
	path, ok := antigravityConversationPath(profile, id)
	if !ok {
		return nil, false
	}
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		return nil, false
	}
	return db, true
}

// firstWorkspaceFolder turns the summaries' JSON list of file:// URIs into
// the first folder, which is where the conversation is resumed.
func firstWorkspaceFolder(workspaces string) string {
	workspaces = strings.Trim(strings.TrimSpace(workspaces), "[]")
	if workspaces == "" {
		return ""
	}
	first := strings.Trim(strings.SplitN(workspaces, ",", 2)[0], ` "`)
	parsed, err := url.Parse(first)
	if err != nil || parsed.Scheme != "file" {
		return ""
	}
	return parsed.Path
}

// parseAntigravityTime reads the summaries' timestamps, which are Go's own
// time.String layout with nanoseconds.
func parseAntigravityTime(value string) time.Time {
	for _, layout := range []string{"2006-01-02 15:04:05.999999999-07:00", "2006-01-02 15:04:05.999999999Z07:00", time.RFC3339Nano} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed
		}
	}
	return time.Time{}
}

// ---- protobuf wire format ---------------------------------------------------
//
// Enough of the wire format to follow a field path without a schema: varints,
// fixed32/64, and length-delimited fields, which are how nested messages and
// strings are both encoded. Anything malformed ends the walk with nothing
// found rather than a panic.

// protoString returns the first string at a field path.
func protoString(message []byte, path ...int) (string, bool) {
	value, ok := protoBytes(message, path...)
	if !ok || !utf8.Valid(value) {
		return "", false
	}
	return string(value), true
}

// protoBytes returns the first length-delimited value at a field path.
func protoBytes(message []byte, path ...int) ([]byte, bool) {
	for depth, field := range path {
		value, wireType, ok := protoFirstField(message, field)
		if !ok || wireType != 2 {
			return nil, false
		}
		if depth == len(path)-1 {
			return value, true
		}
		message = value
	}
	return nil, false
}

// protoVarint returns the first varint at a field path.
func protoVarint(message []byte, path ...int) (uint64, bool) {
	if len(path) == 0 {
		return 0, false
	}
	if len(path) > 1 {
		parent, ok := protoBytes(message, path[:len(path)-1]...)
		if !ok {
			return 0, false
		}
		message = parent
	}
	value, wireType, ok := protoFirstField(message, path[len(path)-1])
	if !ok || wireType != 0 {
		return 0, false
	}
	number, _, ok := protoReadVarint(value, 0)
	return number, ok
}

// protoFirstField finds the first occurrence of field in one message. A
// varint is returned as its encoded bytes.
func protoFirstField(message []byte, field int) ([]byte, int, bool) {
	for index := 0; index < len(message); {
		key, next, ok := protoReadVarint(message, index)
		if !ok {
			return nil, 0, false
		}
		number, wireType := int(key>>3), int(key&7)
		index = next
		var value []byte
		switch wireType {
		case 0:
			_, end, ok := protoReadVarint(message, index)
			if !ok {
				return nil, 0, false
			}
			value, index = message[index:end], end
		case 1:
			if index+8 > len(message) {
				return nil, 0, false
			}
			value, index = message[index:index+8], index+8
		case 5:
			if index+4 > len(message) {
				return nil, 0, false
			}
			value, index = message[index:index+4], index+4
		case 2:
			length, start, ok := protoReadVarint(message, index)
			if !ok || length > uint64(len(message)-start) {
				return nil, 0, false
			}
			end := start + int(length)
			value, index = message[start:end], end
		default:
			return nil, 0, false
		}
		if number == field {
			return value, wireType, true
		}
	}
	return nil, 0, false
}

func protoReadVarint(data []byte, index int) (uint64, int, bool) {
	var value uint64
	for shift := uint(0); shift < 64; shift += 7 {
		if index >= len(data) {
			return 0, index, false
		}
		b := data[index]
		index++
		value |= uint64(b&0x7f) << shift
		if b < 0x80 {
			return value, index, true
		}
	}
	return 0, index, false
}

// antigravitySummariesPath is the store the index fingerprints Antigravity by.
func antigravitySummariesPath(profile Profile) string {
	home, err := antigravityHome(profile)
	if err != nil {
		return ""
	}
	return filepath.Join(home, "conversation_summaries.db")
}
