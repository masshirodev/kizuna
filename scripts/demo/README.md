# The demo gif

`doc/assets/demo.gif`, the one at the top of the README, is recorded, not
captured by hand: re-record it whenever the screens it shows change.

```sh
scripts/demo/record.sh
```

- `stage.py` builds a made-up world under a scratch home: four accounts
  (`claude-max`, `claude-work`, `codex-work`, `opencode-go`), their quota
  caches, empty login markers, stub CLIs, and a week of conversations in
  made-up projects. Times are relative to now, so the quotas never read as
  reset. Nothing in it is a credential: kizuna only checks those files exist.
- `record.sh` builds kizuna as a "packaged" build (so the title bar has no
  update banner), stages the world, and runs `demo.tape` with that home and a
  `PATH` of its own. Never run `vhs demo.tape` directly: without the sandbox's
  `HOME` it records your own accounts, and the kizuna it starts migrates and
  rewrites your real config.
- `demo.tape` is the script: the cockpit, the sheet (`tab`), the resume picker
  (`R`), the handoff (`H`), the palette (`space`) and the integrations box
  (`I`).

It needs [vhs](https://github.com/charmbracelet/vhs) and what vhs needs (ttyd,
ffmpeg, a Chromium). On Arch, `pacman -S vhs`. Without root,
`go install github.com/charmbracelet/vhs@v0.10.0` and ttyd's static release
binary on `PATH` work the same. Set `DEMO_DIR` to keep the staged world for a
look afterwards.
