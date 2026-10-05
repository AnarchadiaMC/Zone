"""LuaJIT 2.1 syntax validation for Anomaly Lua scripts.

Compiles each script with the LuaJIT 2.1 runtime bundled by lupa. Compilation
does not execute the script, so engine globals are not required.

Usage:
    python check_lua_syntax.py [glob ...]

Exits 0 when every file compiles, 1 otherwise. Requires: pip install lupa
"""
import argparse
import glob

import lupa.luajit21 as lj


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("patterns", nargs="*", default=["gamedata/scripts/*.script"])
    args = ap.parse_args()

    paths = sorted({p for pattern in args.patterns
                    for p in glob.glob(pattern, recursive=True)})
    if not paths:
        print("no files matched")
        return 1

    runtime = lj.LuaRuntime()
    failures = 0
    for path in paths:
        with open(path, encoding="utf-8", errors="surrogateescape") as handle:
            source = handle.read()
        try:
            runtime.compile(source)
        except Exception as exc:
            failures += 1
            print(f"FAIL {path}: {exc}")

    print(f"{len(paths) - failures}/{len(paths)} scripts parse")
    return 1 if failures else 0


if __name__ == "__main__":
    raise SystemExit(main())
