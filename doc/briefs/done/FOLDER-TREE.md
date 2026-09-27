# Design brief: the launch folder as a tree

Written 2026-09-27, from: *"change the change folder command to have an actual
tree view."*

This is a brief to forward to Claude Design, not a plan. Where the tree sits,
what a row is, and what `↵` does are layout questions, and they are cheaper to
argue over on a canvas than in a diff. Nothing below is built.

## What `c` does today, measured

One command, one text field. Nothing reads a directory.

- `c` (and the palette's **AI-SESSION → `c` "change launch folder"**) opens a
  modal box titled `change launch folder`, in `tuiFolder` mode.
- The box holds a single field labelled `folder`, pre-filled with the current
  launch folder and opening with the caret at the end.
- Footer: `esc` cancel · `↵` set folder · `ctrl-u` clear. Hint: *"Relative paths
  use the current launch folder. ~ is supported."*
- `↵` resolves the typed value: trim, expand `~`, join a relative path onto the
  current launch folder, `filepath.Abs`, `os.Stat`; a missing path or a file
  that is not a directory is refused with a status error; otherwise the folder
  is set and the status reads `launch folder set to <absolute>`.
- The stored value is the folder every launch runs in (`cmd.Dir`) and the base
  for resolving relative paths. It is session-local: it does not `cd` the parent
  shell.
- It is shown in the title bar's right group beside the update notice, and is
  dropped rather than clipped when the terminal is narrow.
- It uses the same line editor as every other field in the app (caret, word
  jump, `Home`/`End`, kill — `doc/tui.md`, "Typing in a field").
- `TestChangeFolderUsesRelativeDirectory` pins the relative-path behaviour.

The whole interaction is a recall test: you have to already know the path. The
launch folder is almost always one of a handful of repositories, and on this
machine they sit one level under `~/projects`.

## The field is not a picker, and a tree is

The command reads as a location chooser and behaves as a path prompt. The
diagnosis is that it offers neither the speed of a good prompt nor the
recognition of a tree: it makes you type a path you would rather point at, while
still being the fastest way to set a path you already have in hand.

## Tensions the design has to resolve

1. **Typing versus browsing.** The field's best property is that
   `~/projects/kumiko` pastes in one keystroke and `../other` resolves against
   the current folder. A tree's best property is that you never need the name.
   The two must coexist; a tree that deletes the fast path is a regression.
2. **`↵` is overloaded.** Today `↵` *sets* the folder. In every file tree `↵`
   *opens* the directory under the cursor. This is the decision the whole shape
   hangs on — pick one and move the other to its own key (e.g. `↵` descends and
   `space`/`s` sets; or `→` descends and `↵` sets).
3. **What the tree is rooted at.** The current launch folder? `~`? `/`?
   `~/projects`? One root that the user starts at, and can climb from, is simpler
   than a sidebar of roots.

## What the design needs to decide

- **One pane or two.** The resume and handoff pickers are already two panes
  (list left, read-out right). Does the folder picker mirror that — tree left,
  what is in / what is at the selected folder on the right — or is it a single
  tree?
- **Directories only, or files too.** Only a directory can be a launch folder.
  Are files (and symlinks to files) shown dimmed and unselectable, so the tree
  looks like a tree, or hidden entirely?
- **Hidden entries.** `.config`, `.git`, `.local` — always shown, toggled, or
  never? Does a hidden toggle have a key in the footer?
- **Symlinks.** Followed, or shown as leaves?
- **Sort and truncation.** Alphabetical, case-insensitive, directories first? How
  does a deep name fit the box's inner width — a mid-name ellipsis, or clipped
  tail?
- **Scrolling.** The tree can be deeper and taller than the box. What stays in
  view as the cursor moves, and is the path a breadcrumb above the tree?
- **How the typed path survives.** A filter that jumps/limits the tree to
  matches, a second input row, or the tree replaces the field and typing still
  resolves a raw path on `↵`? Deciding this is the same as deciding tension 2.
- **The set gesture and the footer keys.** Given `↵`'s new meaning, what is the
  full key bar — descend/ascend, page, hidden toggle, set, cancel?
- **The empty and deep cases.** A directory with no subdirectories; a directory
  that cannot be read (permissions); a folder that is a git repository versus one
  that is not — does the right pane say so?
- **Box size.** Reuse the prompt width the current box has, or take the taller
  args-modal shape? The tree wants height the prompt never needed.

## Constraints the design must hold

- A full-screen Bubble Tea TUI with **no mouse** (`tea.WithAltScreen()`, no
  mouse messages). Keyboard only.
- Modals are drawn over the board; the inside width is the prompt modal's width
  (88 cells) minus the border and padding, and a status line rides inside the
  box rather than on the board.
- Every text field in the app shares one line editor; the tree must not invent a
  second typing model.
- Only a directory is selectable; a file is never a launch folder.
- `~` and relative paths must keep working — they are documented in
  `doc/tui.md` and covered by a test.
- The chosen folder is session-local and never `cd`s the parent shell.

## Adjacent, not part of this

The launch folder is also the base the argument prompt (`p`) resolves against
and the value shown in the title bar; neither needs to move. There is no
persisted list of recent folders to fold in — if one is wanted, it is its own
decision.
