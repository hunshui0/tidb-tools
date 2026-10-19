#!/usr/bin/env python3
"""Bundle non-libc dependencies and reject ELF files needing glibc > 2.17."""

import argparse
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys


# Keep the destination's loader and libc together. Other libraries travel with
# the package, including libstdc++, libcrypt and libxml2.
SYSTEM_LIBS = {
    "libc.so.6", "libm.so.6", "libpthread.so.0", "libdl.so.2", "librt.so.1",
    "libresolv.so.2", "libutil.so.1", "ld-linux-x86-64.so.2",
}


def elf_files(root):
    for path in sorted(root.rglob("*")):
        if path.is_file() and not path.is_symlink():
            with path.open("rb") as stream:
                header = stream.read(20)
            if header[:4] == b"\x7fELF":
                if header[4:6] != b"\x02\x01" or header[18:20] != b"\x3e\x00":
                    raise RuntimeError(f"{path}: expected Linux x86_64 ELF")
                yield path


def check_versions(root):
    maximum = (0,)
    count = 0
    for path in elf_files(root):
        output = subprocess.check_output(["readelf", "--version-info", str(path)], text=True)
        # Ignore symbols a library exports; inspect only the versions it needs.
        needs = output.partition("Version needs section")[2]
        versions = [tuple(map(int, v.split("."))) for v in re.findall(r"\bGLIBC_([0-9.]+)\b", needs)]
        version = max(versions, default=(0,))
        if version > (2, 17) or "GLIBC_PRIVATE" in needs:
            raise RuntimeError(f"{path}: needs glibc {'.'.join(map(str, version))}; baseline is 2.17")
        maximum = max(maximum, version)
        count += 1
    if not count:
        raise RuntimeError(f"{root}: no ELF files found")
    print(f"Checked {count} ELF files; maximum required glibc: {'.'.join(map(str, maximum))}", flush=True)


def dependencies(path, env):
    result = subprocess.run(["ldd", str(path)], env=env, text=True, capture_output=True)
    output = result.stdout + result.stderr
    if "not found" in output or (result.returncode and "not a dynamic executable" not in output):
        raise RuntimeError(f"{path}: unresolved dependencies:\n{output}")
    return re.findall(r"^\s*(\S+) => (/[^\n]+?) \(", output, re.MULTILINE)


def bundle_runtime(root, env):
    runtime = root / "runtime"
    runtime.mkdir(exist_ok=True)
    sources = []
    # ldd includes transitive dependencies. Recheck everything after copying.
    for path in list(elf_files(root)):
        for name, source in dependencies(path, env):
            source = Path(source).resolve()
            if name in SYSTEM_LIBS or source.is_relative_to(root):
                continue
            destination = runtime / name
            if not destination.exists():
                shutil.copy2(source, destination)
                sources.append(f"{name}: {source}")
    (root / "runtime-dependencies.txt").write_text("\n".join(sources) + "\n")
    # Preserve the installed runtime packages' license notices where available.
    notices = root / "runtime-licenses"
    for directory in (Path("/usr/share/licenses"), Path("/usr/share/doc")):
        for path in directory.glob("*/COPYING*"):
            if path.is_file():
                destination = notices / path.parent.name / path.name
                destination.parent.mkdir(parents=True, exist_ok=True)
                shutil.copy2(path, destination)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("package", type=Path)
    parser.add_argument("--bundle-runtime", action="store_true")
    args = parser.parse_args()
    root = args.package.resolve()
    env = dict(os.environ, LC_ALL="C", LD_LIBRARY_PATH=f"{root}/runtime:{root}/clidriver/lib:{root}/clidriver/lib/icc")
    os.environ["LC_ALL"] = "C"
    check_versions(root)
    if args.bundle_runtime:
        bundle_runtime(root, env)
        check_versions(root)
    for path in elf_files(root):
        dependencies(path, env)
    # A separate process ensures the loader uses only the packaged search path.
    subprocess.run([str(root / "sync_diff_inspector"), "--version"], env=env, check=True)
    subprocess.run([str(root / "run.sh"), "--help"], env=env, check=True, capture_output=True)
    subprocess.run([sys.executable, "-c", """
import ctypes, sys
driver = ctypes.CDLL(sys.argv[1])
handle = ctypes.c_void_p()
driver.SQLAllocHandle.argtypes = [ctypes.c_short, ctypes.c_void_p, ctypes.POINTER(ctypes.c_void_p)]
driver.SQLAllocHandle.restype = ctypes.c_short
status = driver.SQLAllocHandle(1, None, ctypes.byref(handle))
if status not in (0, 1) or not handle.value:
    raise SystemExit('DB2 SQLAllocHandle failed: ' + str(status))
driver.SQLFreeHandle.argtypes = [ctypes.c_short, ctypes.c_void_p]
driver.SQLFreeHandle(1, handle)
print('Packaged DB2 driver loaded and allocated an ODBC environment successfully.')
""", str(root / "clidriver/lib/libdb2.so.1")], env=env, check=True)


if __name__ == "__main__":
    try:
        main()
    except (RuntimeError, subprocess.CalledProcessError) as error:
        sys.exit(str(error))
