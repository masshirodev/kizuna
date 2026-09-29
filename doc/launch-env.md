# Environment variables on a launch

A launch can carry environment variables as well as arguments, and both are
typed on the same line: in the `p` prompt for one launch, or in a profile's
default args for every launch. The line reads the way a shell reads one.
Leading `NAME=value` words are environment, and everything from the first
other word on is arguments:

```
FOO=1 BAR=2 --model opus --verbose
└─ env ───┘ └────── args ────────┘
```

The same rules apply at the boundary as they do in a shell:

| Typed                   | Env        | Args                         |
| ----------------------- | ---------- | ---------------------------- |
| `FOO=1 --x`             | `FOO=1`    | `--x`                        |
| `FOO="a b" --x`         | `FOO=a b`  | `--x`                        |
| `FOO= --x`              | `FOO=` (set, empty) | `--x`               |
| `FOO=1`                 | `FOO=1`    | none, just the defaults      |
| `--model opus FOO=1`    | none       | `--model` `opus` `FOO=1`     |
| `'FOO=1' --x`           | none       | `FOO=1` `--x`                |
| `"x=1 is a prompt"`     | none       | `x=1 is a prompt`            |

So a word is an assignment only if it leads, its name is a valid variable name
(letters, digits and `_`, not starting with a digit), and the name and its `=`
are neither quoted nor escaped. Quote the name to pass such a word as an
argument. Nothing goes through a shell, so `$HOME` stays the literal text
`$HOME`.

This only wraps the environment. A wrapper *command* in front of the CLI
(`nice -n 10 claude`, `firejail claude`) is a different feature and not
supported.

## Who wins

A launch's environment is built in layers, and a later layer wins:

| Layer                          | Where it comes from                                   |
| ------------------------------ | ----------------------------------------------------- |
| 1. profile default env         | `default_env` in `profiles.json`, only if unset       |
| 2. inherited environment       | your shell: `FOO=1 ai max`, `.zshrc` exports, `.env`  |
| 3. ai-session's own variables  | `CLAUDE_CONFIG_DIR`, `CODEX_HOME`, XDG, `AI_PROFILE`… |
| 4. what was typed at `p`       | this launch only                                      |

**A default only fills a name nothing else set.** With `default_env: FOO=2`,
`FOO=1 ai max` runs with `FOO=1`: you typed it on purpose, and the default is
only a default. The launcher cannot tell `FOO=1 ai max` from `export FOO=1` in
`.zshrc`, so an exported variable also beats the default. Anything the
`set -a` in `zshrc/.zshrc_env` exports from `~/.config/myconf/.env` counts as
exported too. The profile editor warns about this. Under `LAUNCHES AS` it lists
every default that the shell the TUI runs in already sets:
`FOO is already set in this shell, which wins over the default`.

**A default cannot set ai-session's own variables.** They are what keeps a
profile isolated, and ai-session sets them after the inherited environment, so
a default for one could never apply. The editor refuses to save one. What
counts as ai-session's own is `CODEX_HOME`, `CLAUDE_CONFIG_DIR`,
`XDG_CONFIG_HOME`, `XDG_DATA_HOME`, `XDG_STATE_HOME`, `AI_PROFILE` and
`AI_PROVIDER`, plus whatever the provider's isolation adds. For Antigravity that
is `HOME`, `DBUS_SESSION_BUS_ADDRESS` and `XDG_CACHE_HOME`.

**What is typed at `p` wins over everything**, ai-session's own variables
included, because it was typed for that one launch. Overriding one of those
takes the session out of the profile's isolation. `CLAUDE_CONFIG_DIR=/elsewhere`
points Claude at another account's state, so sessions, memory and the profile's
run lock no longer line up, and a changed `AI_PROFILE` hides the instance from
the `h` and `k` pickers. The prompt says so under its preview while you type:
`CLAUDE_CONFIG_DIR overrides ai-session's own — this session leaves the profile's isolation`.

**`-p`/`--plain` drops the default env** along with the default args.

## Where it applies

Default env applies wherever default args do: `ai <profile>` / `ai run`, a
resume, and every TUI launch (Enter, `p`, a hijack, a reopen, a handoff).
Logins, `ai update`, `ai integrate openusage` and the internal OpenCode tool
runs take neither.

The CLI has no syntax for typed env, because the shell already does that job:
`FOO=1 ai max` is layer 2 and beats the default. An owned variable set that way
is still replaced by ai-session's own. The `p` prompt is the one place to
override those.

## Where it is stored

- Profile defaults: `default_env` beside `default_args` in
  `~/.config/ai/profiles.json`, as `NAME=value` strings. The editor shows both
  in its one default-args field, env first.
- Typed sets: an `env` list beside `args` in `~/.config/ai/arguments.json`. Two
  sets that differ only in their env are two rows, and a row draws its env words
  dimmed before its arguments.

The code is `cmd/ai/env.go`.
