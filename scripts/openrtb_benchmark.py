"""Run one continuous 60-second HTTP sample per server, in fresh processes.

python3 scripts/openrtb_benchmark.py
python3 scripts/openrtb_benchmark.py --transport TCP --output /tmp/openrtb-tcp

Requires Go 1.27. TCP and contract tests need loopback access. The default Pipe
transport isolates HTTP/GC from kernel TCP churn. Timing has no profiler or race
instrumentation. Run the race suite separately before collecting results.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import platform
import subprocess

REPO = Path(__file__).resolve().parents[1]
ENV = dict(os.environ, GOMAXPROCS='8', GOGC='100', GOMEMLIMIT='off',
           GOCACHE='/tmp/turbo-storage-go-cache')


def run(args, output):
    print(' '.join(map(str, args)), flush=True)
    with output.open('w') as log:
        log.write('# ' + json.dumps(list(map(str, args))) + '\n')
        log.flush()
        subprocess.run(list(map(str, args)), cwd=REPO, env=ENV, stdout=log,
                       stderr=subprocess.STDOUT, check=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--workload', choices=('OpenRTB', 'SmallGET'), default='OpenRTB')
    parser.add_argument('--transport', choices=('Pipe', 'TCP'), default='Pipe')
    parser.add_argument('--output', type=Path,
                        default=REPO/'research'/'openrtb-2026-10-29')
    args = parser.parse_args()
    output = args.output.resolve()
    output.mkdir(parents=True, exist_ok=True)
    if any(output.glob('*.json')):
        raise FileExistsError('choose a fresh output directory to preserve prior results')
    binary = output/'turbo.test'
    run(['go', 'test', '-c', '-o', binary, '.'], output/'build.txt')
    contract = '^TestOpenRTBContract$' if args.workload == 'OpenRTB' else '^TestHTTP11BenchmarkContract$/^'+args.transport+'$/^SmallGET$'
    run([binary, '-test.run='+contract, '-test.v', '-test.timeout=1m'],
        output/'contract.txt')
    sources = ('turbo.go', 'response_writer.go', 'request/request.go',
               'http11_benchmark_test.go', 'benchmark_goroutines_test.go',
               'openrtb_benchmark_test.go', 'http11_sustained_benchmark_test.go',
               'scripts/openrtb_benchmark.py',
               'internal/benchdata/openrtb/request.json',
               'internal/benchdata/openrtb/response.json')
    metadata = {
        'base_commit': subprocess.check_output(['git', 'rev-parse', 'HEAD'],
                                               cwd=REPO, text=True).strip(),
        'go': subprocess.check_output(['go', 'version'], text=True).strip(),
        'platform': platform.platform(), 'cpu': platform.processor(),
        'workload': args.workload, 'transport': args.transport, 'clients': 1, 'turbo_workers': 32,
        'turbo_queue': 1024, 'samples_per_implementation': 1,
        'target_seconds': 60, 'order': ['Turbo', 'Stdlib'],
        'env': {k: ENV.get(k, '') for k in ('GOMAXPROCS', 'GOGC', 'GOMEMLIMIT', 'GODEBUG')},
        'binary_sha256': hashlib.sha256(binary.read_bytes()).hexdigest(),
        'source_sha256': {p: hashlib.sha256((REPO/p).read_bytes()).hexdigest()
                          for p in sources},
    }
    if platform.system() == 'Darwin':
        metadata['cpu'] = subprocess.check_output(
            ['sysctl', '-n', 'machdep.cpu.brand_string'], text=True).strip()
    (output/'metadata.json').write_text(json.dumps(metadata, indent=2)+'\n')
    results = []
    ENV['TURBO_OPENRTB_RESULT_DIR'] = str(output)
    for implementation in ('Turbo', 'Stdlib'):
        run([binary, '-test.run=^$',
             '-test.bench=^Benchmark'+args.workload+'HTTP11$/^'+args.transport+'$/^'+implementation+'$',
             '-test.benchtime=60s', '-test.benchmem', '-test.count=1', '-test.timeout=3m'],
            output/(implementation.lower()+'-raw.txt'))
        result = json.loads((output/(args.transport+'-'+implementation+'.json')).read_text())
        if result['Seconds'] < 60 or result['GCCycles'] == 0 or result['ForcedGC'] != 0:
            raise RuntimeError('sample must run at least 60s and exercise natural GC only')
        results.append(result)
    rows = [f'# {args.workload}: one minute per server', '',
            f"{args.transport}, one client, Go 1.27, GOMAXPROCS=8, GOGC=100, GOMEMLIMIT=off.",
            'Turbo has 32 workers and a 1,024-slot queue. Fresh process per sample.', '',
            '| Server | Seconds | Requests | Requests/s | µs/op | B/op | Allocs/op | Natural GC cycles | GC/1M requests | Total STW pause (ms) | Pause ns/op |',
            '| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |']
    for r in results:
        n = r['Requests']
        rows.append(f"| {r['Implementation']} | {r['Seconds']:.3f} | {n:,} | {n/r['Seconds']:,.0f} | {r['NsPerOp']/1000:.3f} | {r['BytesPerOp']:.1f} | {r['AllocsPerOp']:.3f} | {r['GCCycles']:,} | {r['GCCycles']*1e6/n:.2f} | {r['GCPauseNs']/1e6:.3f} | {r['GCPauseNs']/n:.2f} |")
    rows += ['', 'GC and allocation totals include the common client, Pipe/watchdog or socket, and both server stacks. No JSON codec or auction logic is timed. Request and response byte contents, framing and EOF are checked before timing.',
             'An explicit baseline GC happens before the measured window; neither sample has a forced collection inside timing. B.Loop runs each sample once and may exceed the 60-second target; actual elapsed time is shown.',
             'These are one sample per server, as requested; they demonstrate sustained behavior and actual GC, without establishing statistical timing significance.',
             '', 'Raw results: turbo-raw.txt, stdlib-raw.txt, Pipe-Turbo.json / Pipe-Stdlib.json (or TCP), contract.txt, metadata.json.', '']
    (output/'RESULTS.md').write_text('\n'.join(rows))
    print('\n'.join(rows), flush=True)


if __name__ == '__main__':
    main()
