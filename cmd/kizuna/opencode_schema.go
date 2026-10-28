package main

import (
	"database/sql"
	"encoding/json"
	"strings"
)

// OpenCode 2.x keeps its sessions in session_v2 and their turns in
// session_message, one row a turn, where 1.x kept session, message and a part
// per piece of a message. A store 2.x migrated from 1.x holds both, and the v2
// tables are the superset: on the opencode profile here (2026-10-28) every one
// of the 139 legacy sessions was in session_v2 beside 10 newer ones, and the
// legacy tables had stopped moving on 2026-10-14. A fresh 2.x store has only
// the v2 tables, a 1.x store only the legacy ones. So every reader asks the
// store which it has, and prefers v2. doc/opencode-service.md, "The 2.x
// schema".
const (
	openCodeSessionTableV2     = "session_v2"
	openCodeSessionTableLegacy = "session"
	openCodeTurnTableV2        = "session_message"
)

// openCodeSessionTable names the table a store keeps its sessions in, or ""
// when it has neither (an empty or foreign file).
func openCodeSessionTable(db sqlQuerier, schema string) string {
	if tableExists(db, schema, openCodeSessionTableV2) {
		return openCodeSessionTableV2
	}
	if tableExists(db, schema, openCodeSessionTableLegacy) {
		return openCodeSessionTableLegacy
	}
	return ""
}

// openCodeSharedSessionTable names the session table both sides of a merge
// carry, preferring v2. A 2.x instance merging into an archive 2.x has not
// migrated yet shares nothing, and the merge then reports no sessions rather
// than counting a table it skips.
func openCodeSharedSessionTable(db sqlQuerier) string {
	for _, table := range []string{openCodeSessionTableV2, openCodeSessionTableLegacy} {
		if tableExists(db, "main", table) && tableExists(db, "incoming", table) {
			return table
		}
	}
	return ""
}

// openCodeStoreSessionTable opens a store just long enough to ask which
// session table it has.
func openCodeStoreSessionTable(dbPath string) string {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return ""
	}
	defer db.Close()
	return openCodeSessionTable(db, "main")
}

// opencodeTurnsQuery pulls one session's turns out of a 2.x store in the order
// they were said. seq is the session's own sequence (a unique index with
// session_id), so no timestamp tie can reorder two turns.
const opencodeTurnsQuery = `SELECT type, data FROM session_message
	WHERE session_id = ?
	ORDER BY seq`

// decodeOpenCodeTurn pulls the said thing out of one 2.x turn. A user turn
// carries its text whole; an assistant turn carries a content list whose text
// items are what it said, beside tool calls and reasoning that are dropped the
// way the legacy reader drops non-text parts. system, synthetic (shell output
// fed back in) and idle turns are nobody speaking.
func decodeOpenCodeTurn(kind string, data []byte) (handoffMessage, bool) {
	var turn struct {
		Text    string `json:"text"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if json.Unmarshal(data, &turn) != nil {
		return handoffMessage{}, false
	}
	var text string
	switch kind {
	case "user":
		text = userText(strings.TrimSpace(turn.Text))
	case "assistant":
		var said []string
		for _, item := range turn.Content {
			if item.Type == "text" && strings.TrimSpace(item.Text) != "" {
				said = append(said, strings.TrimSpace(item.Text))
			}
		}
		text = strings.Join(said, "\n\n")
	default:
		return handoffMessage{}, false
	}
	if text == "" {
		return handoffMessage{}, false
	}
	return handoffMessage{fromUser: kind == "user", text: text}, true
}
