#!/usr/bin/env python3
"""查会话到底卡在哪：读 loop/runs 里的运行记录，把失败的工具调用摊开。

用法：
  python3 tools/runs.py                 # 最近 10 次运行的结局
  python3 tools/runs.py -n 3 --fail     # 最近 3 次里失败的工具调用和参数
  python3 tools/runs.py --id agent-179  # 指定某次（前缀匹配）
"""
import argparse
import json
import os
import sys
from pathlib import Path

RUNS = Path.home() / "Library/Application Support/multi-agent-desktop/loop/runs"


def load(path):
    with open(path, encoding="utf-8") as f:
        return json.load(f)


def head(s, n):
    s = " ".join(str(s or "").split())
    return s[:n] + ("…" if len(s) > n else "")


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("-n", type=int, default=10)
    ap.add_argument("--id", default="")
    ap.add_argument("--fail", action="store_true", help="展开失败的工具调用")
    ap.add_argument("--dir", default=str(RUNS))
    args = ap.parse_args()

    d = Path(args.dir)
    if not d.is_dir():
        sys.exit(f"没有运行记录目录：{d}")
    files = sorted(d.glob("*.json"), key=os.path.getmtime, reverse=True)
    if args.id:
        files = [f for f in files if args.id in f.stem]
    for f in files[: args.n]:
        r = load(f)
        b = r.get("budget", {})
        print(f"\n=== {f.stem}  {r.get('phase')}  "
              f"轮 {b.get('usedIterations')}/{b.get('maxIterations')} "
              f"工具 {b.get('usedToolCalls')}/{b.get('maxToolCalls')}")
        if r.get("stopReason"):
            print("  停止：" + head(r["stopReason"], 200))
        if not args.fail:
            continue
        msgs = r.get("messages", [])
        pending = {}
        for m in msgs:
            for c in m.get("toolCalls") or []:
                pending[c.get("id")] = c
            if m.get("role") != "tool":
                continue
            body = str(m.get("content") or "")
            if 'status="ok"' in body or 'status="truncated"' in body:
                continue
            call = pending.get(m.get("toolId")) or {}
            name = call.get("name") or ""
            if not name and 'name="' in body:
                name = body.split('name="', 1)[1].split('"', 1)[0]
            print(f"  ✗ {name} 参数={head(call.get('input'), 140)}")
            print(f"    {head(body, 220)}")


if __name__ == "__main__":
    main()