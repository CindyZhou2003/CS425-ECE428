#!/usr/bin/env python3
"""Turns the collected VM logs into the three MP2 measurements.

  ./analyze.py logs [--since 'HH:MM:SS'] [--until 'HH:MM:SS']

bandwidth  per-node bytes/s from the [BW] lines
false-pos  [SUSPECT]/[FAILURE] rate, for runs where nothing was killed
detection  [KILL] on the victim to [FAILURE] at each survivor
"""

import re
import sys
from datetime import datetime, timedelta
from pathlib import Path
from statistics import mean, pstdev

LINE = re.compile(r"^(\d{4}-\d\d-\d\d \d\d:\d\d:\d\d\.\d{3}) (\[[A-Z]+\]) (.*)$")
BW = re.compile(r"sent=(\d+) recv=(\d+)")
JOINED = re.compile(r"Joined as (\S+)")
TS = "%Y-%m-%d %H:%M:%S.%f"


def parse(path):
    events = []
    for line in path.read_text(errors="replace").splitlines():
        m = LINE.match(line)
        if m:
            events.append((datetime.strptime(m.group(1), TS), m.group(2), m.group(3)))
    return events


def clip(events, since, until):
    def hhmmss(ev):
        return ev[0].strftime("%H:%M:%S")

    if since:
        events = [e for e in events if hhmmss(e) >= since]
    if until:
        events = [e for e in events if hhmmss(e) <= until]
    return events


def span(events):
    return (events[-1][0] - events[0][0]).total_seconds() if len(events) > 1 else 0.0


def bandwidth(logs):
    print("== bandwidth ==")
    rates = []
    for vm, events in logs.items():
        bw = [e for e in events if e[1] == "[BW]"]
        if len(bw) < 2:
            continue
        # First sample covers a partial period, so drop it
        totals = [sum(map(int, BW.search(e[2]).groups())) for e in bw[1:]]
        secs = (bw[-1][0] - bw[0][0]).total_seconds()
        if secs <= 0:
            continue
        rate = sum(totals) / secs
        rates.append(rate)
        print(f"  VM {vm:<3} {rate:9.1f} B/s over {secs:.0f}s")
    if rates:
        print(f"  per node: mean {mean(rates):.1f} B/s, group {sum(rates):.1f} B/s, n={len(rates)}")


def false_positives(logs):
    print("== false positives (no-failure run) ==")
    if any(e[1] == "[KILL]" for events in logs.values() for e in events):
        print("  warning: these logs contain [KILL], detections may be real failures")
    suspects = failures = 0
    secs = 0.0
    for events in logs.values():
        suspects += sum(1 for e in events if e[1] == "[SUSPECT]")
        failures += sum(1 for e in events if e[1] == "[FAILURE]")
        secs = max(secs, span(events))
    if secs <= 0:
        return
    nodes = len(logs)
    print(f"  window {secs:.0f}s over {nodes} nodes")
    print(f"  suspicions {suspects}  -> {suspects / secs:.4f}/s group, {suspects / secs / nodes:.4f}/s per node")
    print(f"  failures   {failures}  -> {failures / secs:.4f}/s group, {failures / secs / nodes:.4f}/s per node")


def detection(logs):
    print("== detection time ==")
    all_times = []
    for vm, events in logs.items():
        for kill_ts, _, _ in [e for e in events if e[1] == "[KILL]"]:
            # The victim's ID is whatever it last joined as before being killed
            ids = [JOINED.search(e[2]).group(1) for e in events if e[1] == "[MEMBERSHIP]" and JOINED.search(e[2]) and e[0] <= kill_ts]
            if not ids:
                print(f"  VM {vm}: killed at {kill_ts:%H:%M:%S.%f} but never logged a join")
                continue
            victim = ids[-1]
            times = []
            for other, oevents in logs.items():
                if other == vm:
                    continue
                hit = next((e[0] for e in oevents if e[1] == "[FAILURE]" and victim in e[2] and e[0] >= kill_ts), None)
                if hit:
                    times.append((hit - kill_ts).total_seconds())
            if not times:
                print(f"  VM {vm} killed at {kill_ts:%H:%M:%S.%f}: no detection found")
                continue
            all_times += times
            print(f"  VM {vm} killed at {kill_ts:%H:%M:%S.%f}: {len(times)} detectors, "
                  f"first {min(times):.3f}s, mean {mean(times):.3f}s, last {max(times):.3f}s")
    if all_times:
        print(f"  overall: mean {mean(all_times):.3f}s, stdev {pstdev(all_times):.3f}s, "
              f"max {max(all_times):.3f}s, n={len(all_times)}")


def main():
    args = sys.argv[1:]
    since = until = None
    if "--since" in args:
        since = args.pop(args.index("--since") + 1); args.remove("--since")
    if "--until" in args:
        until = args.pop(args.index("--until") + 1); args.remove("--until")
    logdir = Path(args[0]) if args else Path("logs")

    logs = {}
    for path in sorted(logdir.glob("machine.*.log")):
        vm = path.stem.split(".")[1]
        events = clip(parse(path), since, until)
        if events:
            logs[vm] = events
    if not logs:
        sys.exit(f"no log lines found in {logdir}")

    print(f"{len(logs)} logs from {logdir}\n")
    bandwidth(logs)
    print()
    false_positives(logs)
    print()
    detection(logs)


main()
