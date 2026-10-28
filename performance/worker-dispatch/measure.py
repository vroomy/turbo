"""Sequential task-4 measurements. Run from the repository root.

GOCACHE=/tmp/turbo-dispatch/go-build go test -c -o /tmp/turbo-dispatch/turbo.test .
python3 performance/worker-dispatch/measure.py matrix
python3 performance/worker-dispatch/measure.py latency
python3 performance/worker-dispatch/measure.py sustained
python3 performance/worker-dispatch/measure.py traces
python3 performance/worker-dispatch/measure.py confirmation
python3 performance/worker-dispatch/measure.py cooldown
python3 performance/worker-dispatch/measure.py summary

No timing, profiling, compilation, or tests should run concurrently.
"""

import argparse
from collections import Counter
import hashlib
import itertools
import json
import os
from pathlib import Path
import platform
import re
import shlex
import shutil
import statistics
import subprocess
import time

ROOT = Path(__file__).resolve().parent
SCRATCH = Path("/tmp/turbo-dispatch")
BINARY = SCRATCH / "turbo.test"
ENV = dict(os.environ, GOMAXPROCS="8")


def run(path, args, env=ENV, binary_output=False):
    print(path.relative_to(ROOT) if path.is_relative_to(ROOT) else path, flush=True)
    path.parent.mkdir(parents=True, exist_ok=True)
    with path.open("wb" if binary_output else "w") as stream:
        if not binary_output:
            stream.write("# GOMAXPROCS=" + env.get("GOMAXPROCS", "") + " " + shlex.join(map(str, args)) + "\n")
            stream.flush()
        result = subprocess.run(list(map(str, args)), env=env, stdout=stream,
                                stderr=subprocess.STDOUT, timeout=180)
    return result.returncode


def output(*args):
    return subprocess.check_output(args, text=True).strip()


def metadata():
    data = {
        "base_commit": output("git", "rev-parse", "HEAD"),
        "go": output("go", "version"), "platform": platform.platform(),
        "cpu": output("sysctl", "-n", "machdep.cpu.brand_string"),
        "binary_sha256": hashlib.sha256(BINARY.read_bytes()).hexdigest(),
        "source_sha256": {name: hashlib.sha256(Path(name).read_bytes()).hexdigest()
                          for name in ("turbo.go", "response_writer.go", "http11_benchmark_test.go", "http11_dispatch_test.go")},
        "GOMAXPROCS": 8, "workers": [1, 8, 32], "queues": [0, 8, 32, 1024],
        "clients": [1, 8, 32, 128], "timing_samples": 6,
        "pipe_benchtime": "100ms", "tcp_benchtime": "256x",
        "latency_requests": 2048, "latency_instrumented": True,
    }
    (ROOT / "metadata.json").write_text(json.dumps(data, indent=2) + "\n")


def matrix():
    metadata()
    # Reverse transport order each round; keep failures in their raw output.
    for sample in range(6):
        for transport in (["Pipe", "TCP"] if sample % 2 == 0 else ["TCP", "Pipe"]):
            args = [BINARY, "-test.run=^$", "-test.bench=^BenchmarkHTTP11Accept$/^" + transport + "$",
                    "-test.benchmem", "-test.benchtime=" + ("100ms" if transport == "Pipe" else "256x"),
                    "-test.timeout=2m"]
            if run(ROOT / f"matrix-{transport.lower()}-{sample+1}.txt", args):
                raise RuntimeError("matrix failed; raw output retained")
    for transport in ("Pipe", "TCP"):
        args = [BINARY, "-test.run=^$",
                f"-test.bench=^BenchmarkHTTP11Accept$/^{transport}$/^Workers(1|8|32)$/^Queue(0|1024)$/^Clients(8|128)$",
                "-test.cpu=1,4,8", "-test.benchmem", "-test.count=6",
                "-test.benchtime=" + ("100ms" if transport == "Pipe" else "256x"), "-test.timeout=3m"]
        if run(ROOT / f"cpu-{transport.lower()}.txt", args):
            raise RuntimeError("CPU matrix failed; raw output retained")


def study(name, transport="Pipe", implementation="Turbo", workers=32, queue=1024,
          clients=8, requests=2048, duration=0, scenario="normal", delay=0, trace=False):
    config = dict(Transport=transport, Implementation=implementation, Workers=workers,
                  Queue=queue, Clients=clients, Requests=requests, Duration=int(duration*1e9),
                  Scenario=scenario, Delay=int(delay*1e9))
    directory = SCRATCH / name
    directory.mkdir(parents=True, exist_ok=True)
    env = dict(ENV, TURBO_DISPATCH_DIR=str(directory), TURBO_DISPATCH_CONFIG=json.dumps(config))
    (directory / "config.json").write_text(json.dumps(config, indent=2) + "\n")
    (directory / "result.json").unlink(missing_ok=True)
    args = [BINARY, "-test.run=^TestHTTP11DispatchStudy$", "-test.v", "-test.timeout=1m"]
    if trace:
        args.append("-test.trace=" + str(SCRATCH / (name.replace("/", "-") + ".trace")))
    code = run(directory / "run.txt", args, env)
    with (ROOT / "study-runs.txt").open("a") as stream:
        stream.write(f"\n# {name} config={json.dumps(config)}\n")
        stream.write((directory / "run.txt").read_text())
    result_path = directory / "result.json"
    with (ROOT / "studies.jsonl").open("a") as stream:
        stream.write(json.dumps(dict(name=name, returncode=code,
                                    result=json.loads(result_path.read_text()) if result_path.exists() else None)) + "\n")
    if code:
        failure_dir = ROOT / "failures" / name
        failure_dir.mkdir(parents=True, exist_ok=True)
        for path in directory.glob("*.txt"):
            if path.name == "failure-sockets.txt":
                lines = path.read_text().splitlines()
                if any(line.startswith("TIME_WAIT rows limited") for line in lines):
                    shutil.copy2(path, failure_dir / path.name)
                    continue
                states = Counter(line.split()[5] for line in lines[3:] if len(line.split()) > 5)
                live = [line for line in lines[3:] if len(line.split()) > 5 and line.split()[5] != "TIME_WAIT"]
                waiting = [line for line in lines[3:] if len(line.split()) > 5 and line.split()[5] == "TIME_WAIT"]
                text = lines[:3] + [f"listener states={dict(states)}", f"TIME_WAIT sample: 8 of {len(waiting)} rows; full snapshot: {path}"] + live + waiting[:8]
                (failure_dir / path.name).write_text("\n".join(text) + "\n")
            else:
                shutil.copy2(path, failure_dir / path.name)
    return code


def latency():
    for transport, workers, queue, clients in itertools.product(
            ("Pipe", "TCP"), (1, 8, 32), (0, 8, 32, 1024), (1, 8, 32, 128)):
        if study(f"latency/{transport}-w{workers}-q{queue}-c{clients}", transport,
                 workers=workers, queue=queue, clients=clients):
            raise RuntimeError("latency study failed; evidence retained")
    # Slow clients hold a connection before sending headers; slow handlers sleep
    # before writing. Delay is the requested sleep, not an observed service time.
    for transport, scenario, workers, queue in itertools.product(
            ("Pipe", "TCP"), ("slow-client", "slow-handler"), (1, 8, 32), (0, 8, 1024)):
        if study(f"slow/{transport}-{scenario}-w{workers}-q{queue}", transport,
                 workers=workers, queue=queue, clients=128, requests=512,
                 scenario=scenario, delay=.001):
            raise RuntimeError("slow study failed; evidence retained")


def sockets(path):
    raw = subprocess.run(["netstat", "-anv", "-p", "tcp"], capture_output=True, text=True)
    states = {}
    for line in raw.stdout.splitlines():
        fields = line.split()
        if len(fields) > 5 and fields[0].startswith("tcp"):
            states[fields[5]] = states.get(fields[5], 0) + 1
    path.write_text(json.dumps(dict(states=states, error=raw.stderr, returncode=raw.returncode), indent=2) + "\n")


def sustained():
    # Reproduce the original profile after serial TCP churn, then reverse order,
    # then repeat after cooldown. Include bounded, 3 s and longer unprofiled runs.
    original = Path("benchmarks/current-2026-10-24/cpu-tcp-parallel-turbo-failed.txt")
    if original.exists():
        (ROOT / "original-failure.txt").write_text(original.read_text())
    for round_, order in enumerate((("Turbo", "Stdlib"), ("Stdlib", "Turbo"), ("Turbo", "Stdlib")), 1):
        if round_ == 3:
            time.sleep(15)
        for implementation in order:
            stem = f"sustained/round{round_}-{implementation}"
            directory = ROOT / stem
            directory.mkdir(parents=True, exist_ok=True)
            sockets(directory / "sockets-before.json")
            args = [BINARY, "-test.run=^$",
                    f"-test.bench=^BenchmarkHTTP11NoKeepAlive$/^TCP$/^SmallGET$/^Serial$/^{implementation}$",
                    "-test.benchtime=3s", "-test.timeout=1m"]
            run(directory / "preceding-serial.txt", args)
            profile = SCRATCH / f"round{round_}-{implementation}-cpu.pprof"
            args[2] = args[2].replace("^Serial$", "^Parallel$")
            args.append("-test.cpuprofile=" + str(profile))
            run(directory / "original-profile-retry.txt", args)
            for name, requests, duration in (("bounded", 10000, 0), ("3s", 0, 3), ("10s", 0, 10)):
                study(stem + "/" + name, "TCP", implementation, requests=requests, duration=duration)
            sockets(directory / "sockets-after.json")


def traces():
    for name, transport, workers, queue, clients, scenario in (
            ("idle-receives", "Pipe", 32, 1024, 8, "normal"),
            ("unbuffered", "Pipe", 8, 0, 128, "normal"),
            ("full-queue", "Pipe", 1, 8, 128, "slow-handler"),
            ("slow-client", "TCP", 8, 8, 128, "slow-client"),
            ("tcp", "TCP", 32, 1024, 8, "normal")):
        stem = "traces/" + name
        study(stem, transport, workers=workers, queue=queue, clients=clients,
              requests=512, scenario=scenario, delay=.001, trace=True)
        trace_path = SCRATCH / (stem.replace("/", "-") + ".trace")
        for kind in ("sync", "sched", "net", "syscall"):
            profile = SCRATCH / f"trace-{name}-{kind}.pprof"
            run(profile, ["go", "tool", "trace", "-pprof=" + kind, trace_path], binary_output=True)
            run(ROOT / stem / (kind + ".txt"), ["go", "tool", "pprof", "-top", "-nodecount=30", BINARY, profile])
            if kind in ("sync", "sched"):
                run(ROOT / stem / (kind + "-dispatch.txt"), ["go", "tool", "pprof", "-list=turbo.*(listen|work)", BINARY, profile])


def confirmation():
    for transport in ("Pipe", "TCP"):
        streams = {label: (ROOT / f"confirmation-{transport.lower()}-{label}.txt").open("w") for label in ("default", "candidate")}
        try:
            for sample in range(6):
                for label in (("default", "candidate") if sample % 2 == 0 else ("candidate", "default")):
                    workers, queue = (32, 1024) if label == "default" else (8, 0)
                    args = [BINARY, "-test.run=^$",
                            f"-test.bench=^BenchmarkHTTP11Accept$/^{transport}$/^Workers{workers}$/^Queue{queue}$/^Clients(8|128)$",
                            "-test.benchmem", "-test.benchtime=" + ("100ms" if transport == "Pipe" else "256x"), "-test.timeout=1m"]
                    path = SCRATCH / "confirmation.txt"
                    code = run(path, args)
                    # Normalize only the compared parameters, preserving clients.
                    streams[label].write(re.sub(r"/Workers\d+/Queue\d+", "", path.read_text()))
                    streams[label].flush()
                    if code:
                        raise RuntimeError("confirmation failed; raw output retained")
        finally:
            for stream in streams.values():
                stream.close()
        run(ROOT / f"confirmation-{transport.lower()}-benchstat.txt", ["benchstat"] + [ROOT / f"confirmation-{transport.lower()}-{label}.txt" for label in ("default", "candidate")])


def cooldown():
    # No serial load after these pauses: unlike round 3 above, this directly
    # tests a cooled socket table. Pauses are bounded so progress stays visible.
    settings = subprocess.run(["sysctl", "kern.osrelease", "net.inet.tcp.msl", "net.inet.ip.portrange.first", "net.inet.ip.portrange.last"], capture_output=True, text=True)
    (ROOT / "socket-settings.txt").write_text(settings.stdout + settings.stderr)
    for implementation in ("Turbo", "Stdlib"):
        print("35 s cooldown before", implementation, flush=True)
        time.sleep(35)
        directory = ROOT / "cooldown" / implementation
        directory.mkdir(parents=True, exist_ok=True)
        sockets(directory / "sockets-before.json")
        study(f"cooldown/{implementation}/3s", "TCP", implementation, requests=0, duration=3)
        study(f"cooldown/{implementation}/10s", "TCP", implementation, requests=0, duration=10)
        sockets(directory / "sockets-after.json")


def summary():
    studies = {d["name"]: d["result"] for d in map(json.loads, (ROOT / "studies.jsonl").read_text().splitlines())}
    samples = {}
    pattern = re.compile(r"^(BenchmarkHTTP11Accept/\S+)\s+\d+\s+(.*)$", re.M)
    for path in sorted(ROOT.glob("matrix-*.txt")):
        for name, metrics in pattern.findall(path.read_text()):
            values = dict((unit, float(value)) for value, unit in re.findall(r"([\d.]+) (\S+)", metrics))
            samples.setdefault(name, []).append(values)
    rows = ["# Configuration medians", "", "Six uninstrumented throughput samples; latency and live heaps are from a separate instrumented 2,048-request run.", "",
            "Idle/warm KiB are forced-GC live heap deltas from the pre-server snapshot, including runtime/pools. Latency is closed-loop and includes dial through EOF/close; it does not model an open-loop arrival rate.", "",
            "| Transport | Workers | Queue | Clients | ns/op | req/s | B/op | objects/op | p50 µs | p95 µs | p99 µs | Idle KiB | Warm KiB |", "| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |"]
    for name, points in sorted(samples.items()):
        if len(points) != 6:
            raise ValueError((name, len(points)))
        transport, workers, queue, clients, cpu = re.fullmatch(
            r"BenchmarkHTTP11Accept/(TCP|Pipe)/Workers(\d+)/Queue(\d+)/Clients(\d+)-(\d+)", name).groups()
        metrics = {key: statistics.median(p[key] for p in points) for key in points[0]}
        d = studies[f"latency/{transport}-w{workers}-q{queue}-c{clients}"]
        cells = [transport, workers, queue, clients, f'{metrics["ns/op"]:.1f}', f'{metrics["req/s"]:.0f}',
                 f'{metrics["B/op"]:.0f}', f'{metrics["allocs/op"]:g}']
        cells += [f'{d[key]/1000:.1f}' for key in ("P50NS", "P95NS", "P99NS")]
        cells += [f'{(d[key]["HeapBytes"]-d["Initial"]["HeapBytes"])/1024:.1f}' for key in ("Idle", "CompletedHeap")]
        rows.append("| " + " | ".join(cells) + " |")
    (ROOT / "configurations.md").write_text("\n".join(rows) + "\n")
    for transport in ("pipe", "tcp"):
        paths = sorted(ROOT.glob(f"matrix-{transport}-*.txt"))
        combined = "\n".join(p.read_text() for p in paths)
        for workers, queue, label in ((32, 1024, "default"), (8, 0, "8-unbuffered"), (8, 8, "8-buffered"), (1, 0, "1-unbuffered")):
            lines = [re.sub(r"/Workers\d+/Queue\d+", "", line) for line in combined.splitlines()
                     if f"/Workers{workers}/Queue{queue}/" in line and line.startswith("Benchmark")]
            (ROOT / f"compare-{transport}-{label}.txt").write_text("\n".join(lines) + "\n")
        run(ROOT / f"{transport}-benchstat.txt", ["benchstat"] + [ROOT / f"compare-{transport}-{label}.txt"
            for label in ("default", "8-unbuffered", "8-buffered", "1-unbuffered")])
    for transport in ("pipe", "tcp"):
        run(ROOT / f"cpu-{transport}-benchstat.txt", ["benchstat", ROOT / f"cpu-{transport}.txt"])
    cpu_rows = ["# Selected CPU-capacity cases", "", "Six samples per case, fixed named clients. Values are medians; ns/op is inverse throughput.", "",
                "| Transport | CPUs | Workers | Queue | Clients | ns/op | B/op | objects/op |", "| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |"]
    for transport in ("pipe", "tcp"):
        cases = {}
        for name, metrics in pattern.findall((ROOT / f"cpu-{transport}.txt").read_text()):
            values = dict((unit, float(value)) for value, unit in re.findall(r"([\d.]+) (\S+)", metrics))
            cases.setdefault(name, []).append(values)
        for name, points in cases.items():
            if len(points) != 6:
                raise ValueError((name, len(points)))
            trans, workers, queue, clients, cpu = re.fullmatch(
                r"BenchmarkHTTP11Accept/(TCP|Pipe)/Workers(\d+)/Queue(\d+)/Clients(\d+)(?:-(\d+))?", name).groups()
            cpu = cpu or "1"
            cells = [trans, cpu, workers, queue, clients] + [f'{statistics.median(p[k] for p in points):g}' for k in ("ns/op", "B/op", "allocs/op")]
            cpu_rows.append("| " + " | ".join(cells) + " |")
    (ROOT / "cpu-configurations.md").write_text("\n".join(cpu_rows) + "\n")
    rows = ["# Slow-client and slow-handler observations", "", "One instrumented 512-request run per case, 128 closed-loop clients, requested delay 1 ms. Max queue is sampled every millisecond and is a lower bound.", "",
            "| Transport | Scenario | Workers | Queue | req/s | p50 ms | p95 ms | p99 ms | Max queue |", "| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |"]
    for name, d in studies.items():
        if not name.startswith("slow/"):
            continue
        c = d["Config"]
        cells = [c["Transport"], c["Scenario"], str(c["Workers"]), str(c["Queue"]), f'{d["RequestsPerSecond"]:.0f}']
        cells += [f'{d[k]/1e6:.2f}' for k in ("P50NS", "P95NS", "P99NS")]
        cells += [str(d["MaxQueue"])]
        rows.append("| " + " | ".join(cells) + " |")
    (ROOT / "slow-clients-handlers.md").write_text("\n".join(rows) + "\n")
    rows = ["# Sustained TCP observations", "", "One preflight connection is included in dial/accept/close counters, and excluded from attempts/completions. Failed runs are diagnostic evidence; their elapsed times include outstanding 10 s deadlines and failure capture. They are not throughput samples.", "",
            "| Round/server/run | Attempts | Completed | Errors | Dialed | Accepted | Closed | Close calls | Seconds | Max queue | Stopped goroutines |", "| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |"]
    for name, d in studies.items():
        if not name.startswith(("sustained/", "cooldown/")):
            continue
        cells = [name.removeprefix("sustained/")] + [str(d[k]) for k in ("Attempts", "Completed")]
        cells += [str(len(d["Errors"] or []))] + [str(d[k]) for k in ("Dialed", "Accepted", "Closed", "CloseCalls")]
        cells += [f'{d["Seconds"]:.3f}', str(d["MaxQueue"]), str(d["Stopped"]["Goroutines"])]
        rows.append("| " + " | ".join(cells) + " |")
    (ROOT / "sustained.md").write_text("\n".join(rows) + "\n")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("phase", choices=("matrix", "latency", "sustained", "traces", "confirmation", "cooldown", "summary"))
    args = parser.parse_args()
    globals()[args.phase]()
