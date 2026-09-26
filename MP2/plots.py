#!/usr/bin/env python3
"""Draws the three report plots (mean with SD error bars) from whatever log directories exist.

  ./plots.py [outdir]

logs-bw-<mode>-<N>       no-failure run with N VMs, for bandwidth vs group size
logs-fp-<mode>-<drop%>   no-failure run with 10 VMs, for false positives vs drop rate
logs-detect-<mode>       ./measure_detection.sh output, for detection time vs failures

<mode> is gossip or suspect.
"""

import re
import sys
from pathlib import Path
from statistics import mean, stdev

import matplotlib

matplotlib.use("Agg")
import matplotlib.pyplot as plt

from analyze import BW, detection_trials, parse

# Log directories and gossip.go sit next to this script, wherever it's run from
HERE = Path(__file__).resolve().parent
# Join churn at startup isn't background traffic
WARMUP = 10
# Readings per false-positive run, each a slice of the run
FP_WINDOWS = 5
MODES = {"gossip": ("Gossip", "#2a78d6", "o", "-"), "suspect": ("Gossip+S", "#eb6834", "s", "--")}


def load(logdir):
    return {p.stem.split(".")[1]: parse(p) for p in sorted(logdir.glob("machine.*.log"))}


# Keeps the span when every node is up and past its join, so all readings see the full group
def steady(logs):
    start = max(ev[0][0] for ev in logs.values())
    end = min(ev[-1][0] for ev in logs.values())
    return start.timestamp() + WARMUP, end.timestamp()


def runs(kind, mode):
    found = []
    for d in HERE.glob(f"logs-{kind}-{mode}-*"):
        try:
            found.append((int(d.name.rsplit("-", 1)[1]), d))
        except ValueError:
            pass
    return sorted(found)


# Readings are per-node per-second sent+recv samples
def bandwidth_point(logs):
    lo, hi = steady(logs)
    samples = [sum(map(int, BW.search(e[2]).groups())) for ev in logs.values() for e in ev
               if e[1] == "[BW]" and lo <= e[0].timestamp() <= hi]
    return mean(samples), stdev(samples)


# Drop rates whose no-failure runs logged no false positives in either mode, run without local logs
ZERO_FP_DROPS = [0]


# Reads the analyze.py summaries pasted into data.txt, since some runs have no local logs
# Returns {mode: {drop: (per-VM B/s means, failures, window seconds)}}
def summaries():
    blocks, cur = [], None
    for line in (HERE / "data.txt").read_text().splitlines():
        if "== bandwidth" in line:
            nums = re.findall(r"\d+", line)
            cur = {"drop": int(nums[-1]) if nums else None, "vms": [], "sus": 0}
            blocks.append(cur)
        elif cur is not None:
            if m := re.match(r"\s+VM \d+\s+([\d.]+) B/s", line):
                cur["vms"].append(float(m.group(1)))
            elif m := re.search(r"(suspicions|failures)\s+(\d+)", line):
                cur[{"suspicions": "sus", "failures": "fail"}[m.group(1)]] = int(m.group(2))
            elif m := re.search(r"window (\d+)s", line):
                cur["win"] = int(m.group(1))
    out = {mode: {} for mode in MODES}
    for b in blocks:
        # Detection runs have no drop rate in their header; headers can mislabel the mode, suspicions can't
        if b["drop"] is not None:
            out["suspect" if b["sus"] else "gossip"][b["drop"]] = (b["vms"], b["fail"], b["win"])
    return out


# Readings are group-wide [FAILURE] rates over equal slices of the run
def fp_point(logs, tag):
    lo, hi = steady(logs)
    width = (hi - lo) / FP_WINDOWS
    counts = [0] * FP_WINDOWS
    for ev in logs.values():
        for e in ev:
            t = e[0].timestamp()
            if e[1] == tag and lo <= t < hi:
                counts[int((t - lo) / width)] += 1
    rates = [c / width for c in counts]
    return mean(rates), stdev(rates), hi - lo


def style(ax, xlabel, ylabel):
    ax.set_xlabel(xlabel)
    ax.set_ylabel(ylabel)
    ax.grid(True, color="#e4e4e0", linewidth=0.6)
    ax.set_axisbelow(True)
    for side in ("top", "right"):
        ax.spines[side].set_visible(False)
    ax.legend(frameon=False, fontsize=8)


def series(ax, xs, ys, es, mode, label=None, **kw):
    name, color, marker, line = MODES[mode]
    ax.errorbar(xs, ys, yerr=es, label=label or name, color=color, marker=marker, linestyle=line,
                linewidth=1.5, markersize=5, capsize=3, elinewidth=1, **kw)


# Reads the protocol timeouts, in seconds, so the timeline follows whatever gossip.go uses
def timeouts():
    units = {"Millisecond": 0.001, "Second": 1}
    src = (HERE / "gossip.go").read_text()
    return {name: int(n) * units[unit] for name, n, unit in re.findall(r"(\w+Timeout)\s*=\s*(\d+) \* time\.(\w+)", src)}


# Entry lifecycle after the last heartbeat
def timeline(out):
    t = timeouts()
    fail, sus, conf, clean = t["failTimeout"], t["suspectTimeout"], t["confirmTimeout"], t["cleanupTimeout"]
    alive, suspect, dead, removed = "#1baf7a", "#eda100", "#d03b3b", "#8a8a85"
    rows = {
        "Gossip": [(0, fail, alive, "ALIVE"), (fail, fail + clean, dead, "DEAD (gossiped)")],
        "Gossip+S": [(0, sus, alive, "ALIVE"), (sus, sus + conf, suspect, "SUSPECT"),
                     (sus + conf, sus + conf + clean, dead, "DEAD (gossiped)")],
    }
    fig, ax = plt.subplots(figsize=(7, 1.5))
    for y, segs in enumerate(rows.values()):
        for start, end, color, label in segs:
            ax.barh(y, end - start, left=start, height=0.55, color=color, edgecolor="white", linewidth=2)
            ax.text((start + end) / 2, y, label, ha="center", va="center", fontsize=8, color="white", weight="bold")
        ax.plot(end, y, marker="X", color=removed, markersize=9)
        ax.text(end + 0.12, y, "deleted", va="center", fontsize=8, color="#555550")
    # Refutation arrow: a newer incarnation sends the entry back to ALIVE
    ax.annotate("refuted (incarnation+1)\n→ back to ALIVE", xy=(sus + conf / 2, 1.3), xytext=(sus + conf / 2, 1.72),
                ha="center", fontsize=7, color="#555550",
                arrowprops=dict(arrowstyle="->", color="#555550", lw=0.8))

    # Merges labels of events that land at the same instant, e.g. both modes detecting together
    ticks = {}
    for x, label in ((0, "0"), (sus, "$T_s$"), (fail, "$T_{fail}$"), (sus + conf, "$T_s+T_c$"),
                     (fail + clean, "+cleanup"), (sus + conf + clean, "+cleanup")):
        ticks.setdefault(round(x, 3), [])
        if label not in ticks[round(x, 3)]:
            ticks[round(x, 3)].append(label)
    for x in ticks:
        if x:
            ax.axvline(x, color="#c3c2b7", linewidth=0.8, linestyle=":", zorder=0)
    ax.set_xticks(list(ticks))
    ax.set_xticklabels([f"{x:g}" if not x else f"{x:g}\n" + "=".join(ls) for x, ls in ticks.items()], fontsize=8)
    ax.set_yticks(range(len(rows)), rows.keys())
    ax.set_ylim(-0.5, 2.1)
    ax.set_xlim(0, max(fail + clean, sus + conf + clean) + 0.9)
    ax.set_xlabel("Seconds since the entry's heartbeat last increased")
    for side in ("top", "right", "left"):
        ax.spines[side].set_visible(False)
    ax.tick_params(axis="y", length=0)
    fig.savefig(out / "timeline.pdf", bbox_inches="tight")
    fig.savefig(out / "timeline.png", bbox_inches="tight", dpi=200)


def main():
    out = Path(sys.argv[1]) if len(sys.argv) > 1 else HERE / "report"
    out.mkdir(exist_ok=True)
    plt.rcParams.update({"font.size": 9, "figure.figsize": (3.4, 2.4)})
    timeline(out)

    # Both plots read the same no-failure runs, one per drop rate
    fp_logs = {mode: dict((d, load(path)) for d, path in runs("fp", mode)) for mode in MODES}
    summary = summaries()
    # Nudges the two modes apart so their error bars don't overlap
    nudge = {"gossip": -0.4, "suspect": 0.4}

    fig, ax = plt.subplots()
    print("== background bandwidth (B/s per node, sent+recv; readings are the 10 per-VM means) ==")
    for mode in MODES:
        pts = [(d, mean(vms), stdev(vms)) for d, (vms, _, _) in sorted(summary[mode].items())]
        for d, m, s in pts:
            print(f"  {mode:8} drop={d:<3}% {m:9.1f} +- {s:.1f}")
        if pts:
            series(ax, [d + nudge[mode] for d, _, _ in pts], [m for _, m, _ in pts], [s for *_, s in pts], mode)
    # Expected sent+recv at N=10: 8 messages/s each way, each a header plus one entry per member
    ax.axhline(16 * (58 + 94 * 10), color="#8a8a85", linewidth=0.8, linestyle=":", label="Model (N=10)")
    ax.set_ylim(0, 22000)
    style(ax, "Receiver drop rate (%)", "Bandwidth per node (KB/s)")
    ax.legend(frameon=False, fontsize=7, loc="lower left")
    ax.yaxis.set_major_formatter(lambda v, _: f"{v / 1000:g}")
    fig.savefig(out / "bandwidth.pdf", bbox_inches="tight")
    fig.savefig(out / "bandwidth.png", bbox_inches="tight", dpi=200)

    fig, ax = plt.subplots()
    print("== false positives (group-wide [FAILURE] lines/s) ==")
    for mode in MODES:
        pts = {d: (0.0, 0.0, "no false positives") for d in ZERO_FP_DROPS}
        # A run without logs can stand in only with zero failures, where every slice is exactly 0
        for d, (_, fails, secs) in summary[mode].items():
            if fails == 0:
                pts[d] = (0.0, 0.0, f"0 over {secs}s, data.txt")
        for d, logs in fp_logs[mode].items():
            m, s, secs = fp_point(logs, "[FAILURE]")
            pts[d] = (m, s, f"over {secs:.0f}s")
        for d, (m, s, note) in sorted(pts.items()):
            print(f"  {mode:8} drop={d:<3}% {m:.4f} +- {s:.4f}  {note}")
        xs = sorted(pts)
        series(ax, [d + nudge[mode] for d in xs], [pts[d][0] for d in xs], [pts[d][1] for d in xs], mode)
    style(ax, "Receiver drop rate (%)", "False positives / s (group)")
    ax.legend(frameon=False, fontsize=7, loc="upper left")
    fig.savefig(out / "false_positives.pdf", bbox_inches="tight")
    fig.savefig(out / "false_positives.png", bbox_inches="tight", dpi=200)

    fig, ax = plt.subplots()
    print("== detection time (s) ==")
    # Nudges the two modes apart so their error bars don't overlap
    shift = {"gossip": -0.06, "suspect": 0.06}
    stats = {}
    for mode in MODES:
        path = HERE / f"logs-detect-{mode}"
        if not path.exists():
            continue
        _, by_k = detection_trials(load(path))
        ks = sorted(by_k)
        first = [[f for f, _ in by_k[k]] for k in ks]
        last = [[l for _, l in by_k[k]] for k in ks]
        for k, f, l in zip(ks, first, last):
            print(f"  {mode:8} k={k} first {mean(f):.3f} +- {stdev(f):.3f}  all {mean(l):.3f} +- {stdev(l):.3f}  n={len(f)}")
            stats.setdefault(k, {})[mode] = (mean(f), stdev(f), mean(l), stdev(l), len(f))
        name = MODES[mode][0]
        xs = [k + shift[mode] for k in ks]
        series(ax, xs, [mean(l) for l in last], [stdev(l) for l in last], mode, f"{name}, all detected")
        series(ax, xs, [mean(f) for f in first], [stdev(f) for f in first], mode, f"{name}, first detected",
               markerfacecolor="white")
    ax.set_xticks(range(1, 6))
    ax.set_xlim(0.6, 5.4)
    ax.set_ylim(0, 4)
    # Marks the failures past the spec's three-at-once guarantee
    ax.axvspan(3.5, 5.4, color="#f2f1ec", zorder=0)
    ax.text(4.45, 0.25, "beyond spec", ha="center", fontsize=7, color="#8a8a85")
    ax.axhline(3, color="#8a8a85", linewidth=0.8, linestyle=":")
    ax.text(0.65, 3.07, "3 s first-detection bound", fontsize=7, color="#8a8a85")
    style(ax, "Simultaneous failures", "Detection time (s)")
    ax.legend(frameon=False, fontsize=7, ncol=2, loc="lower left", bbox_to_anchor=(0, 0.1))
    fig.savefig(out / "detection.pdf", bbox_inches="tight")
    fig.savefig(out / "detection.png", bbox_inches="tight", dpi=200)
    detection_table(stats, out)


# Same numbers as the detection plot, for the report to \\input
def detection_table(stats, out):
    cell = lambda m, s: f"{m:.3f} $\\pm$ {s:.3f}"
    rows = []
    for k in sorted(stats):
        vals = []
        for mode in MODES:
            if mode in stats[k]:
                fm, fs, lm, ls, _ = stats[k][mode]
                vals += [cell(fm, fs), cell(lm, ls)]
            else:
                vals += ["--", "--"]
        rows.append(f"{k} & {stats[k][next(iter(stats[k]))][4]} & " + " & ".join(vals) + r" \\")
    (out / "detection_table.tex").write_text("\n".join([
        r"\begin{tabular}{cc|cc|cc}",
        r"\hline",
        r" & & \multicolumn{2}{c|}{Gossip} & \multicolumn{2}{c}{Gossip+S} \\",
        r"$k$ & trials & first (s) & all (s) & first (s) & all (s) \\",
        r"\hline",
        *rows,
        r"\hline",
        r"\end{tabular}",
        "",
    ]))


main()
