#!/usr/bin/env python3
"""Build portable sync_diff_inspector archives using only Go and Python."""

import argparse
import datetime
import os
from pathlib import Path
import re
import shutil
import subprocess
import tarfile
import tempfile
import zipfile


ROOT = Path(__file__).resolve().parents[1]


def git(*args):
    return subprocess.check_output(["git", *args], cwd=ROOT, text=True).strip()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--os", choices=("linux", "windows"), required=True)
    parser.add_argument("--arch", choices=("amd64", "arm64"), required=True)
    parser.add_argument("--version", default=None)
    parser.add_argument("--output", type=Path, default=ROOT / "dist")
    args = parser.parse_args()
    if args.os == "windows" and args.arch != "amd64":
        parser.error("Windows releases currently support amd64 only")
    version = args.version or git("describe", "--tags", "--dirty", "--always")
    if not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9._+-]*", version):
        parser.error("version must contain only letters, digits, dots, _, + or -")
    output = args.output.resolve()
    output.mkdir(parents=True, exist_ok=True)
    metadata = {
        "Version": version,
        "BuildTS": datetime.datetime.now(datetime.timezone.utc).strftime("%Y-%m-%d %H:%M:%S"),
        "GitHash": git("rev-parse", "HEAD"),
        "GitBranch": os.environ.get("GITHUB_REF_NAME") or git("rev-parse", "--abbrev-ref", "HEAD"),
    }
    ldflags = "-s -w " + " ".join(
        f'-X "github.com/pingcap/tidb-tools/pkg/utils.{key}={value}"'
        for key, value in metadata.items()
    )
    env = dict(os.environ, GOOS=args.os, GOARCH=args.arch, CGO_ENABLED="0", GOAMD64="v1")
    with tempfile.TemporaryDirectory(prefix="sync-diff-release-") as tmp:
        package = Path(tmp) / "package"
        package.mkdir()
        binary = package / ("sync_diff_inspector.exe" if args.os == "windows" else "sync_diff_inspector")
        subprocess.run(
            [os.environ.get("GO", "go"), "build", "-mod=readonly", "-trimpath", "-ldflags", ldflags,
             "-o", str(binary), "./sync_diff_inspector"],
            cwd=ROOT, env=env, check=True,
        )
        binary.chmod(0o755)
        for name in ("README.md", "LICENSE"):
            shutil.copy2(ROOT / name, package / name)
        shutil.copy2(ROOT / "sync_diff_inspector" / "README.md", package / "USAGE.md")
        config = package / "config"
        config.mkdir()
        for example in sorted((ROOT / "sync_diff_inspector" / "config").glob("*.toml")):
            # Use a relative output path on Windows as well as Linux.
            text = re.sub(
                r'output-dir = "/tmp/output/[^"]+"', 'output-dir = "./output"',
                example.read_text(encoding="utf-8"),
            )
            (config / example.name).write_text(text, encoding="utf-8")
        # Kylin V10 uses the same static Linux executable; publish explicit names
        # so users can select their system and CPU without a separate build.
        systems = ("linux", "kylin-v10") if args.os == "linux" else ("windows",)
        for system in systems:
            name = f"sync_diff_inspector-{version}-{system}-{args.arch}"
            if args.os == "windows":
                archive = output / f"{name}.zip"
                with zipfile.ZipFile(archive, "w", compression=zipfile.ZIP_DEFLATED) as bundle:
                    for path in sorted(package.rglob("*")):
                        if path.is_file():
                            bundle.write(path, f"{name}/{path.relative_to(package).as_posix()}")
            else:
                archive = output / f"{name}.tar.gz"
                with tarfile.open(archive, "w:gz") as bundle:
                    bundle.add(package, arcname=name)
            print(archive, flush=True)


if __name__ == "__main__":
    main()
