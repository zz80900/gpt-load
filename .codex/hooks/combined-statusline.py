#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""Combine the project Trellis statusline with the global claude-hud output."""
from __future__ import annotations

import os
import re
import shutil
import subprocess
import sys
from pathlib import Path


_stdout_reconfigure = getattr(sys.stdout, "reconfigure", None)
if callable(_stdout_reconfigure):
    try:
        _stdout_reconfigure(encoding="utf-8", errors="replace")
    except (OSError, ValueError):
        pass


_VERSION_RE = re.compile(r"^\d+(?:\.\d+)+$")


def _claude_dir() -> Path:
    configured = os.environ.get("CLAUDE_CONFIG_DIR")
    return Path(configured).expanduser() if configured else Path.home() / ".claude"


def _version_key(path: Path) -> tuple[int, ...]:
    return tuple(int(part) for part in path.name.split("."))


def _find_hud_entrypoint() -> Path | None:
    cache_dir = _claude_dir() / "plugins" / "cache" / "claude-hud" / "claude-hud"
    versions = [
        path
        for path in cache_dir.iterdir()
        if path.is_dir() and _VERSION_RE.fullmatch(path.name)
    ] if cache_dir.is_dir() else []
    if not versions:
        return None
    entrypoint = max(versions, key=_version_key) / "dist" / "index.js"
    return entrypoint if entrypoint.is_file() else None


def _run(command: list[str], payload: str) -> str:
    try:
        result = subprocess.run(
            command,
            input=payload,
            capture_output=True,
            text=True,
            encoding="utf-8",
            errors="replace",
            timeout=5,
            check=False,
        )
    except (OSError, subprocess.TimeoutExpired):
        return ""
    return result.stdout.strip() if result.returncode == 0 else ""


def main() -> None:
    payload = sys.stdin.read()
    output: list[str] = []

    trellis = _run([sys.executable, str(Path(__file__).with_name("statusline.py"))], payload)
    if trellis:
        output.append(trellis)

    hud_entrypoint = _find_hud_entrypoint()
    node = shutil.which("node")
    if hud_entrypoint and node:
        hud = _run([node, str(hud_entrypoint)], payload)
        if hud:
            output.append(hud)

    if output:
        print("\n".join(output))


if __name__ == "__main__":
    main()
