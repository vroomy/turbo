"""Collect sequential before/after parser measurements; see README.md."""

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
REPO = ROOT.parents[2]
ENV = dict(os.environ, GOMAXPROCS="8")
BASE_COMMIT = "214bf58eca07af9b18edb6ba314a7d50d82e5c64"
GROUPS = {
    "parser": ("request", "^(BenchmarkParse|BenchmarkOwnHeader)$", "200ms"),
    "process": ("server", "^(BenchmarkTurboProcess|BenchmarkTurboProcessHeaders)$", "200ms"),
    "pipe": ("server", "^BenchmarkHTTP11NoKeepAlive$/^Pipe$/^(SmallGET|HEAD)$/.*/^Turbo$", "200ms"),
    "tcp": ("server", "^BenchmarkHTTP11NoKeepAlive$/^TCP$/^(SmallGET|HEAD)$/.*/^Turbo$", "200x"),
}


def run(path, args):
    print(path.name, flush=True)
    with path.open("w") as stream:
        stream.write("# GOMAXPROCS=8 " + shlex.join(args) + "\n")
        stream.flush()
        subprocess.run(args, env=ENV, stdout=stream, stderr=subprocess.STDOUT, check=True)


def metadata(scratch, binaries):
    def digest(path):
        return hashlib.sha256(path.read_bytes()).hexdigest()

    sources = ("request/request.go", "request/request_test.go", "request/testdata/browser-headers.txt",
               "request_headers_benchmark_test.go", "http11_benchmark_test.go", "response_writer.go", "turbo.go")
    try:
        cpu = subprocess.check_output(["sysctl", "-n", "machdep.cpu.brand_string"],
                                      text=True, stderr=subprocess.DEVNULL).strip()
    except (OSError, subprocess.CalledProcessError):
        cpu = platform.processor()
    return {
        "base_commit": BASE_COMMIT,
        "go_version": subprocess.check_output(["go", "version"], text=True).strip(),
        "platform": platform.platform(),
        "cpu": cpu,
        "GOMAXPROCS": 8, "workers": 32, "queue": 1024,
        "serial_clients": 1, "parallel_clients": 8,
        "binaries": {label: {kind: {"path": str(path), "sha256": digest(path)}
                             for kind, path in pair.items()} for label, pair in binaries.items()},
        "sources": {label: {name: digest(base / name) for name in sources}
                    for label, base in (("before", scratch / "before-source"), ("after", REPO))},
    }


def timing(scratch, binaries, groups):
    (ROOT / "metadata.json").write_text(json.dumps(metadata(scratch, binaries), indent=2) + "\n")
    for group in groups:
        kind, bench, duration = GROUPS[group]
        with contextlib.ExitStack() as stack:
            streams = {label: stack.enter_context((ROOT / f"{label}-{group}.txt").open("w"))
                       for label in binaries}
            for sample in range(10):
                for label in (("before", "after") if sample % 2 == 0 else ("after", "before")):
                    print(group, sample + 1, label, flush=True)
                    args = [str(binaries[label][kind]), "-test.run=^$", "-test.bench=" + bench,
                            "-test.benchmem", "-test.benchtime=" + duration, "-test.timeout=1m"]
                    stream = streams[label]
                    stream.write(f"# round {sample + 1}, GOMAXPROCS=8 " + shlex.join(args) + "\n")
                    stream.flush()
                    subprocess.run(args, env=ENV, stdout=stream, stderr=subprocess.STDOUT, check=True)


def profiles(scratch, binaries):
    cases = (
        ("parse-small", "request", "^BenchmarkParse$/^SmallGET$/^Reused$"),
        ("parse-browser", "request", "^BenchmarkParse$/^BrowserHeaders$/^Reused$"),
        ("process-small", "server", "^BenchmarkTurboProcess$/^SmallGET$"),
        ("process-browser", "server", "^BenchmarkTurboProcessHeaders$/^BrowserHeaders$"),
    )
    for label, pair in binaries.items():
        for name, kind, bench in cases:
            binary = str(pair[kind])
            profile = scratch / f"{label}-{name}-cpu.pprof"
            run(ROOT / f"{label}-{name}-cpu-run.txt", [binary, "-test.run=^$", "-test.bench=" + bench,
                "-test.benchtime=3s", "-test.cpuprofile=" + str(profile)])
            run(ROOT / f"{label}-{name}-cpu-top.txt", ["go", "tool", "pprof", "-top",
                "-nodecount=30", binary, str(profile)])
            if name.startswith("parse-"):
                source = scratch / "before-source" if label == "before" else REPO
                run(ROOT / f"{label}-{name}-cpu-list.txt", ["go", "tool", "pprof",
                    "-trim_path=" + str(REPO), "-source_path=" + str(source),
                    "-list=parseHeaders|canonicalHeaderKey|validToken", binary, str(profile)])
        for name, kind, bench in cases[2:]:
            binary = str(pair[kind])
            profile = scratch / f"{label}-{name}-memory.pprof"
            run(ROOT / f"{label}-{name}-memory-run.txt", [binary, "-test.run=^$", "-test.bench=" + bench,
                "-test.benchtime=20000x", "-test.memprofilerate=1", "-test.memprofile=" + str(profile)])
            for metric in ("alloc_space", "alloc_objects", "inuse_space"):
                run(ROOT / f"{label}-{name}-{metric}.txt", ["go", "tool", "pprof", "-top",
                    "-nodecount=25", "-sample_index=" + metric, binary, str(profile)])


def summary(groups):
    rows = ["# All benchmark medians", "", "Ten alternating-order samples per case.", "",
            "| Case | Before ns/op | After ns/op | Change | Before B/op | After B/op | Before allocs/op | After allocs/op |",
            "| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |"]
    pattern = re.compile(r"^(Benchmark\S+)\s+\d+\s+([\d.]+) ns/op\s+.*?([\d.]+) B/op\s+([\d.]+) allocs/op", re.M)
    for group in groups:
        values = {}
        for label in ("before", "after"):
            samples = {}
            for name, ns, size, objects in pattern.findall((ROOT / f"{label}-{group}.txt").read_text()):
                samples.setdefault(name, []).append(tuple(map(float, (ns, size, objects))))
            if not samples or any(len(points) != 10 for points in samples.values()):
                raise ValueError(f"expected ten samples per case: {label}-{group}")
            values[label] = {name: [statistics.median(column) for column in zip(*points)]
                             for name, points in samples.items()}
        if values["before"].keys() != values["after"].keys():
            raise ValueError(f"before/after cases differ: {group}")
        for name, before in values["before"].items():
            after = values["after"][name]
            rows.append(f"| {name} | {before[0]:g} | {after[0]:g} | {(after[0]/before[0]-1)*100:+.2f}% | "
                        f"{before[1]:g} | {after[1]:g} | {before[2]:g} | {after[2]:g} |")
        run(ROOT / f"{group}-benchstat.txt", ["benchstat", str(ROOT / f"before-{group}.txt"),
                                             str(ROOT / f"after-{group}.txt")])
    (ROOT / "medians.md").write_text("\n".join(rows) + "\n")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("phase", choices=("timing", "profiles", "summary"))
    parser.add_argument("--scratch", type=Path, default=Path("/tmp/turbo-get-head-parser"))
    parser.add_argument("--groups", nargs="+", choices=GROUPS, default=list(GROUPS))
    args = parser.parse_args()
    binaries = {label: {"request": args.scratch / f"{label}-request.test", "server": args.scratch / f"{label}.test"}
                for label in ("before", "after")}
    if args.phase == "summary":
        summary(args.groups)
    elif args.phase == "timing":
        timing(args.scratch, binaries, args.groups)
    else:
        profiles(args.scratch, binaries)
