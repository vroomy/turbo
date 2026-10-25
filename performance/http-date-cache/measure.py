"""Collect Date-cache evidence without concurrent timing/profiling jobs.

  python3 performance/http-date-cache/measure.py timing BEFORE AFTER
  python3 performance/http-date-cache/measure.py profiles BEFORE AFTER
  python3 performance/http-date-cache/measure.py summary

Use --groups isolated pipe or --groups tcp to separate socket permissions.
The timing order reverses each round. Profiles are separate from timings.
"""

import argparse
import contextlib
import hashlib
import json
import os
from pathlib import Path
import platform
import re
import shlex
import statistics
import subprocess


ROOT = Path(__file__).resolve().parent
SCRATCH = Path("/tmp/turbo-date-cache")
BASE_COMMIT = "45364e7d1c6c0b1cf71211d5f2b8ae4bfb0d8a8d"
ENV = dict(os.environ, GOMAXPROCS="8")
GROUPS = {
    "isolated": ("^(BenchmarkTurboProcess|BenchmarkResponseWriter)$", "200ms"),
    "pipe": ("^BenchmarkHTTP11NoKeepAlive$/^Pipe$/.*/.*/^Turbo$", "200ms"),
    "tcp": ("^BenchmarkHTTP11NoKeepAlive$/^TCP$/.*/.*/^Turbo$", "200x"),
}


def run(path, args):
    print(path.name, flush=True)
    with path.open("w") as stream:
        stream.write("# GOMAXPROCS=8 " + shlex.join(args) + "\n")
        stream.flush()
        subprocess.run(args, env=ENV, stdout=stream,
                       stderr=subprocess.STDOUT, check=True)


def metadata(binaries):
    def output(*args):
        return subprocess.check_output(args, text=True).strip()

    return {
        "base_commit": BASE_COMMIT,
        "worktree_head": output("git", "rev-parse", "HEAD"),
        "go_version": output("go", "version"),
        "platform": platform.platform(),
        "cpu": output("sysctl", "-n", "machdep.cpu.brand_string"),
        "GOMAXPROCS": 8, "workers": 32, "queue": 1024,
        "serial_clients": 1, "parallel_clients": 8,
        "binaries": {label: {"path": binary, "sha256": hashlib.sha256(
            Path(binary).read_bytes()).hexdigest()} for label, binary in binaries.items()},
        "source_sha256": {name: hashlib.sha256(Path(name).read_bytes()).hexdigest()
                          for name in ("response_writer.go", "http11_benchmark_test.go")},
    }


def timings(binaries, groups):
    (ROOT / "metadata.json").write_text(json.dumps(metadata(binaries), indent=2) + "\n")
    for group in groups:
        bench, duration = GROUPS[group]
        with contextlib.ExitStack() as stack:
            streams = {label: stack.enter_context((ROOT / f"{label}-{group}.txt").open("w"))
                       for label in binaries}
            for sample in range(10):
                order = ("before", "after") if sample % 2 == 0 else ("after", "before")
                for label in order:
                    print(group, sample + 1, label, flush=True)
                    args = [binaries[label], "-test.run=^$", "-test.bench=" + bench,
                            "-test.benchmem", "-test.benchtime=" + duration,
                            "-test.count=1", "-test.timeout=1m"]
                    stream = streams[label]
                    stream.write(f"# round {sample + 1}, GOMAXPROCS=8 " + shlex.join(args) + "\n")
                    stream.flush()
                    subprocess.run(args, env=ENV, stdout=stream,
                                   stderr=subprocess.STDOUT, check=True)


def profiles(binaries, groups):
    SCRATCH.mkdir(exist_ok=True)
    cases = []
    if "isolated" in groups:
        cases.append(("process", "^BenchmarkTurboProcess$/^SmallGET$", "3s", 1))
    for group in ("pipe", "tcp"):
        if group in groups:
            transport = {"pipe": "Pipe", "tcp": "TCP"}[group]
            for mode in ("Serial", "Parallel"):
                bench = f"^BenchmarkHTTP11NoKeepAlive$/^{transport}$/^SmallGET$/^{mode}$/^Turbo$"
                # Bound TCP connection churn; sustained timeout diagnosis is task 4.
                duration, count = ("3s", 1) if group == "pipe" else ("2000x", 3)
                cases.append((f"{group}-{mode.lower()}", bench, duration, count))
    for label, binary in binaries.items():
        for name, bench, duration, count in cases:
            profile = SCRATCH / f"{label}-{name}-cpu.pprof"
            run(ROOT / f"{label}-{name}-cpu-run.txt", [binary, "-test.run=^$",
                "-test.bench=" + bench, "-test.benchtime=" + duration,
                "-test.count=" + str(count), "-test.timeout=1m",
                "-test.cpuprofile=" + str(profile)])
            run(ROOT / f"{label}-{name}-cpu-top.txt", ["go", "tool", "pprof",
                "-top", "-nodecount=30", binary, str(profile)])
            if name == "process":
                source = SCRATCH / "before-source" if label == "before" else Path.cwd()
                run(ROOT / f"{label}-date-cpu-list.txt", ["go", "tool", "pprof",
                    "-trim_path=" + str(Path.cwd()), "-source_path=" + str(source),
                    "-list=populateHeaders|dateHeader", binary, str(profile)])
        if "isolated" not in groups:
            continue
        profile = SCRATCH / f"{label}-process-memory.pprof"
        run(ROOT / f"{label}-process-memory-run.txt", [binary, "-test.run=^$",
            "-test.bench=^BenchmarkTurboProcess$/^SmallGET$", "-test.benchtime=20000x",
            "-test.memprofilerate=1", "-test.memprofile=" + str(profile)])
        for kind in ("alloc_space", "alloc_objects", "inuse_space"):
            run(ROOT / f"{label}-process-{kind}.txt", ["go", "tool", "pprof",
                "-top", "-nodecount=30", "-sample_index=" + kind, binary, str(profile)])
        # Source lines are read from the working tree by pprof. The baseline
        # listing uses its source snapshot (see README reproduction commands).
        run(ROOT / f"{label}-date-allocations.txt", ["go", "tool", "pprof",
            "-sample_index=alloc_objects", "-list=time.Time.Format", binary, str(profile)])


def summary():
    rows = ["# All benchmark medians", "",
            "Ten alternating-order samples per case. Parallel ns/op measures inverse aggregate throughput.", "",
            "| Case | Before ns/op | After ns/op | Change | Before B/op | After B/op | Before objects/op | After objects/op |",
            "| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |"]
    pattern = re.compile(r"^(Benchmark\S+)\s+\d+\s+([\d.]+) ns/op\s+.*?([\d.]+) B/op\s+([\d.]+) allocs/op", re.M)
    for group in GROUPS:
        values = {}
        for label in ("before", "after"):
            path = ROOT / f"{label}-{group}.txt"
            samples = {}
            for name, ns, size, objects in pattern.findall(path.read_text()):
                samples.setdefault(name, []).append(tuple(map(float, (ns, size, objects))))
            if not samples or any(len(points) != 10 for points in samples.values()):
                raise ValueError(f"expected ten samples for every case in {path}")
            values[label] = {name: [statistics.median(column) for column in zip(*points)]
                             for name, points in samples.items()}
        if values["before"].keys() != values["after"].keys():
            raise ValueError(f"before/after cases differ for {group}")
        for name, before in values["before"].items():
            after = values["after"][name]
            change = (after[0] / before[0] - 1) * 100
            rows.append(f"| {name} | {before[0]:g} | {after[0]:g} | {change:+.2f}% | "
                        f"{before[1]:g} | {after[1]:g} | {before[2]:g} | {after[2]:g} |")
        run(ROOT / f"{group}-benchstat.txt", ["benchstat",
            str(ROOT / f"before-{group}.txt"), str(ROOT / f"after-{group}.txt")])
    (ROOT / "medians.md").write_text("\n".join(rows) + "\n")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("phase", choices=("timing", "profiles", "summary"))
    parser.add_argument("before", nargs="?")
    parser.add_argument("after", nargs="?")
    parser.add_argument("--groups", nargs="+", choices=GROUPS, default=list(GROUPS))
    args = parser.parse_args()
    if args.phase == "summary":
        summary()
    else:
        if not args.before or not args.after:
            parser.error("before and after binaries are required")
        binaries = {label: str(Path(binary).resolve()) for label, binary in
                    (("before", args.before), ("after", args.after))}
        {"timing": timings, "profiles": profiles}[args.phase](binaries, args.groups)
