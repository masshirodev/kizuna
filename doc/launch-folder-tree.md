# The launch folder tree

`c` used to be a single-line prompt: you typed a path from memory and pressed
`↵`. It is now a directory tree with the folder under the cursor read out beside
it. The design is the Claude Design handoff **"Launch Folder Tree"** (2026-09-27),
direction **1b — "Tree and read-out, like the pickers"**; 1a and 1c were the two
alternatives not taken.

Back to the [README](../README.md) · [doc/tui.md](tui.md).

## The box

The picker is a modal the width of the share picker (124 cells) and the full
height of the frame, split in two panes joined by the board's hairline:

- **Left — the tree.** Rooted at your home folder and opened down to the current
  launch folder, with the cursor on it. Directories carry a trailing slash, an
  unreadable one a `✗`, an open one `▾`, a closed one `▸`. Files are shown dimmed
  and **skipped by the cursor**, so the tree reads as a tree. The folder that is
  the launch folder is marked `● launch folder` at the right edge. A footer names
  the directory the cursor row sits in and counts its folders, files and hidden
  entries.
- **Right — the read-out.** The folder under the cursor: its path, whether it is
  a git repository and on what branch with how many changed files, what it
  resolves to, and what it contains (directories first, then files).

Below 46 + 34 cells the read-out folds away and the tree keeps the width, the
same way the resume and handoff pickers fold their preview.

## Keys

| Key | Does |
| --- | ---- |
| `↑` `↓` (`k` `j`) | move the cursor, skipping files |
| `↵` (`→` `l`) | open the folder under the cursor, or close it if it is open |
| `←` (`h`) | close the open folder, or step to its parent when it is closed |
| `s` | make the folder under the cursor the launch folder |
| `/` | open the typed-path field in the heading's corner |
| `.` | toggle hidden entries |
| `esc` (`q`) | cancel |

`↵` **opens**, as in every file tree, and `s` sets — the one decision the handoff
called out. The typed path is still there under `/`: typing a relative path,
`~` or an absolute one and pressing `↵` there resolves and sets it exactly as the
old prompt did.

## The design, settled

**Palette: the existing tokens.** Every colour in the mock is an ai-session
`color*` value, so it is implemented with the token and never a literal hex:
`#C4B5FD` accent, `#E5E7EB` text, `#9CA3AF` muted, `#52525B` faint, `#3F3F46`
dim, `#6EE7B7` success (the launch-folder mark), `#FCA5A5` danger (an unreadable
folder), `#181826` the selected row, `#1F1F24` the pane hairline, `#0B0B0E` the
canvas.

**Spacing: hand-set, transcribed.** The mock is drawn in cells and lines, which
is the unit the TUI draws in, so the measurements are taken as drawn: a 124-wide
box, a one-line header row, a blank row under it, a one-line key footer, and a
1-cell border. The mock's 56-cell tree pane is drawn at a 124-wide box; the code
takes the pickers' 48% share clamped to 46–62 cells instead, which is 55 at that
width. That is the one measurement not copied literally.

## Where it departs from the handoff

- **The tree pane width is a share, not a fixed 56 cells.** The pickers already
  size their two halves by share and fold at a minimum; a fixed pane width would
  be the only box in the app that does not track the terminal. At the mock's own
  124-wide box the two agree to within a cell.
- **`→` opens as well as `↵`.** The mock's footer lists only `↵`, but `→` is the
  natural inverse of `←` and costs nothing to accept.
- **`q` cancels the box**, matching every other modal.
- **The footer's `s set <path>` truncates the path at 32 cells.** A long path
  would otherwise push the other keys off a narrow box.
- **The read-out caps its listing** to the rows it has and ends with `+ N more`;
  the mock draws a short listing whole.
- **Hidden children are left out of `CONTAINS` while the hidden toggle is off**,
  so the pane agrees with the tree beside it. The mock does not say.
- **The root row is labelled `~` even when the launch folder is outside home.**
  In that one case the tree roots at the folder itself; it is still drawn as `~`
  for want of a better label.

## What it does not do yet

- **No search inside the tree.** `/` is the typed-path field, exactly as the
  handoff specifies; a filter that narrows the middle column was direction 1c.
- **No recents.** There is no persisted list of recently used folders to fold in.
- **A symlink to a directory is shown as a file** (dimmed, not selectable),
  because a directory entry reached through a symlink is not itself a directory
  to `os.ReadDir`. Following symlinks is the one thing in the mock's "Entries"
  note it did not settle.
- **No expansion of a whole subtree** and no "collapse all"; each folder is
  opened one at a time.
- **No mouse**, like the rest of the TUI.
