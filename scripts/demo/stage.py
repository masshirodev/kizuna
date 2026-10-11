#!/usr/bin/env python3
"""Stage the made-up world doc/assets/demo.gif is recorded in.

Four accounts with quotas, logins and a week of conversations, under a home of
their own, so the recording shows a lived-in cockpit and none of a real
machine's accounts, folders or session titles. Every file here is one kizuna
only reads for its existence or its numbers: the "credentials" are empty, and
no CLI ever runs against them.

    scripts/demo/stage.py DIR      # then: vhs scripts/demo/demo.tape (see README)

Times are relative to now, so the quotas have not reset and "23m ago" stays
23 minutes when the recording is made.
"""

import json
import os
import shutil
import sys
import uuid
from datetime import datetime, timedelta, timezone

now = datetime.now(timezone.utc)


def iso(moment):
    return moment.strftime("%Y-%m-%dT%H:%M:%S.000Z")


def write(path, content):
    os.makedirs(os.path.dirname(path), exist_ok=True)
    with open(path, "w") as file:
        file.write(content)


def touch(path, moment):
    stamp = moment.timestamp()
    os.utime(path, (stamp, stamp))


def stage(root):
    home = os.path.join(root, "home")
    config = os.path.join(home, ".config")
    profiles = os.path.join(config, "kizuna", "profiles")
    projects = {name: os.path.join(home, "projects", name) for name in ("lattice", "wayfarer", "dotfiles", "kizuna")}
    for folder in projects.values():
        os.makedirs(folder, exist_ok=True)
    # The provider CLIs are stubs on the sandbox's own PATH: the glance names
    # where each one is installed, and that must not be a real home.
    for command in ("claude", "codex", "opencode"):
        stub = os.path.join(home, ".local", "bin", command)
        write(stub, "#!/bin/sh\nexit 0\n")
        os.chmod(stub, 0o755)

    accounts = [
        {"name": "claude-max", "provider": "claude", "command": "claude", "notes": "personal max plan"},
        {"name": "claude-work", "provider": "claude", "command": "claude", "notes": "team plan"},
        {"name": "codex-work", "provider": "codex", "command": "codex"},
        {"name": "opencode-go", "provider": "opencode", "command": "opencode"},
    ]
    write(os.path.join(config, "kizuna", "profiles.json"), json.dumps({"profiles": accounts}, indent=2) + "\n")

    # name: (5-hour used %, weekly used %), and when each window resets.
    quotas = {"claude-max": (22, 41), "claude-work": (93, 64), "codex-work": (8, 30)}

    conversations = {
        "claude-max": [
            (timedelta(minutes=23), "lattice", "Add a release tag to every deploy"),
            (timedelta(hours=3), "wayfarer", "Balance the fishing skill's drop table"),
            (timedelta(hours=26), "kizuna", "Package kizuna for the AUR"),
            (timedelta(days=3), "dotfiles", "Theme ranma from the wallpaper"),
        ],
        "claude-work": [
            (timedelta(hours=1, minutes=10), "lattice", "Fix the calendar's week view on mobile"),
            (timedelta(hours=30), "lattice", "Review the ledger import"),
        ],
        "codex-work": [
            (timedelta(hours=5), "wayfarer", "Profile the offline simulation loop"),
            (timedelta(days=2), "kizuna", "Write tests for the handoff brief"),
        ],
    }

    for account in accounts:
        name, provider = account["name"], account["provider"]
        base = os.path.join(profiles, name)
        if provider == "claude":
            write(os.path.join(base, "claude", ".credentials.json"), "{}\n")
            five, week = quotas[name]
            state = {
                "cachedUsageUtilization": {
                    "utilization": {
                        "five_hour": {"utilization": five, "resets_at": iso(now + timedelta(hours=2, minutes=40))},
                        "seven_day": {"utilization": week, "resets_at": iso(now + timedelta(days=4))},
                    }
                }
            }
            write(os.path.join(base, "claude", ".claude.json"), json.dumps(state) + "\n")
            status_line = {"type": "command", "command": "printf '[%s] %s' \"${KIZUNA_PROFILE:-no profile}\" \"${PWD##*/}\""}
            write(os.path.join(base, "claude", "settings.json"), json.dumps({"statusLine": status_line}, indent=2) + "\n")
            for ago, project, title in conversations.get(name, []):
                when = now - ago
                folder = projects[project]
                slug = folder.replace("/", "-")
                path = os.path.join(base, "claude", "projects", slug, f"{uuid.uuid4()}.jsonl")
                lines = [
                    {"type": "user", "cwd": folder, "timestamp": iso(when), "message": {"content": title}},
                    {"type": "ai-title", "aiTitle": title},
                ]
                write(path, "".join(json.dumps(line) + "\n" for line in lines))
                touch(path, when + timedelta(minutes=12))
        elif provider == "codex":
            write(os.path.join(base, "codex", "auth.json"), "{}\n")
            five, week = quotas[name]
            for index, (ago, project, title) in enumerate(conversations.get(name, [])):
                when = now - ago
                day = when.strftime("%Y/%m/%d")
                session = str(uuid.uuid4())
                path = os.path.join(base, "codex", "sessions", day, f"rollout-{when.strftime('%Y-%m-%dT%H-%M-%S')}-{session}.jsonl")
                lines = [
                    {"type": "session_meta", "payload": {"session_id": session, "cwd": projects[project], "timestamp": iso(when)}},
                    {"type": "response_item", "payload": {"role": "user", "content": [{"text": title}]}},
                ]
                if index == 0:
                    lines.append({"type": "event_msg", "payload": {"rate_limits": {
                        "primary": {"used_percent": five, "window_minutes": 300, "resets_at": int((now + timedelta(hours=4)).timestamp())},
                        "secondary": {"used_percent": week, "window_minutes": 10080, "resets_at": int((now + timedelta(days=5)).timestamp())},
                    }}})
                write(path, "".join(json.dumps(line) + "\n" for line in lines))
                touch(path, when + timedelta(minutes=20))
        elif provider == "opencode":
            write(os.path.join(base, "data", "opencode", "auth.json"), "{}\n")
    return home


if __name__ == "__main__":
    if len(sys.argv) != 2:
        sys.exit(__doc__)
    root = os.path.abspath(sys.argv[1])
    shutil.rmtree(root, ignore_errors=True)
    print(stage(root))
