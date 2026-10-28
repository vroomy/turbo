"""Reproduce TODO 10/11; subprocesses always run sequentially.

python3 scripts/storage_study.py prepare
python3 scripts/storage_study.py baseline
python3 scripts/storage_study.py timing
python3 scripts/storage_study.py profiles
python3 scripts/storage_study.py validate
python3 scripts/storage_study.py summarize

Requires Go 1.27 and benchstat on PATH. TCP/race suite require loopback access.
Raw profiles and isolated candidate sources are under /tmp/turbo-storage-study.
Local text results contain measurements, commands, hashes and profile summaries.
TODO.md preserves the essential findings; the result directory is ignored.
"""
import hashlib
import json
import os
from pathlib import Path
import platform
import re
import shutil
import statistics
import subprocess
import sys

REPO = Path(__file__).resolve().parents[1]
HERE = REPO / 'studies' / 'request-storage'
TMP = Path(os.environ.get('TURBO_STORAGE_SCRATCH', '/tmp/turbo-storage-study'))
OUT = HERE / 'results'
ENV = dict(os.environ, GOMAXPROCS='8', GOCACHE='/tmp/turbo-storage-go-cache')
VARIANTS = {'baseline': (1024, 256), 'smaller': (512, 128), 'wider': (1024, 512), 'larger': (2048, 1024)}


def run(args, path, cwd=REPO, extra=None, append=False):
    path.parent.mkdir(parents=True, exist_ok=True)
    print(' '.join(map(str, args)), flush=True)
    with path.open('a' if append else 'w') as f:
        f.write('# ' + json.dumps(list(map(str, args))) + '\n'); f.flush()
        subprocess.run(list(map(str, args)), cwd=cwd, env=dict(ENV, **(extra or {})), stdout=f, stderr=subprocess.STDOUT, check=True)


def binary(v, kind='request'): return TMP / f'{v}-{kind}.test'
def source(v): return TMP / v

def prepare():
    TMP.mkdir(exist_ok=True, parents=True)
    files = subprocess.check_output(['git', 'ls-files'], cwd=REPO, text=True).splitlines()
    files += ['internal/storagestudy/study.go', 'request/storage_study_test.go', 'storage_study_test.go']
    files = sorted(set(f for f in files if f.endswith('.go') or f == 'go.mod' or f.startswith('request/testdata/')))
    meta = {'commit': subprocess.check_output(['git', 'rev-parse', 'HEAD'], cwd=REPO, text=True).strip(),
            'go': subprocess.check_output(['go', 'version'], text=True).strip(), 'platform': platform.platform(),
            'GOMAXPROCS': 8, 'variants': VARIANTS, 'source_sha256': {}, 'binary_sha256': {}}
    for v, (block, limit) in VARIANTS.items():
        dst = source(v)
        dst.mkdir(exist_ok=False)
        for name in files:
            (dst/name).parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(REPO/name, dst/name)
        p = dst/'request/request.go'
        s = p.read_text().replace('headerTextBlockSize = 1024', f'headerTextBlockSize = {block}').replace('maxSharedHeaderSize = headerTextBlockSize / 4', f'maxSharedHeaderSize = {limit}')
        p.write_text(s)
        meta['source_sha256'][v] = {f: hashlib.sha256((dst/f).read_bytes()).hexdigest() for f in files}
        meta['binary_sha256'][v] = {}
        for kind, package in [('request','./request'), ('server','.')]:
            run(['go','test','-c','-o',binary(v, kind),package], OUT/f'{v}-build-{kind}.txt', cwd=dst)
            meta['binary_sha256'][v][kind] = hashlib.sha256(binary(v,kind).read_bytes()).hexdigest()
    (OUT/'metadata.json').write_text(json.dumps(meta, indent=2)+'\n')


def bench(v, kind, regex, duration, path, append=False):
    run([binary(v,kind), '-test.run=^$', '-test.bench='+regex, '-test.benchtime='+duration, '-test.benchmem', '-test.count=1'], path, append=append)


def heap(v, workers, mode=None, size=68):
    name = f'{v}-heap-{workers}' + (f'-{size}-{mode}' if mode else '-workers')
    directory = TMP/name
    extra = {'TURBO_STORAGE_DIR':str(directory), 'TURBO_STORAGE_WORKERS':str(workers)}
    if mode: extra.update(TURBO_STORAGE_MODE=mode, TURBO_STORAGE_SIZE=str(size))
    kind, test = ('request','TestStoragePackingHeap') if mode else ('server','TestStorageWorkerHeap')
    run([binary(v,kind), '-test.run=^'+test+'$', '-test.memprofilerate=1', '-test.timeout=60s', '-test.v'], OUT/(name+'.txt'), extra=extra)
    for path in sorted(directory.glob('*.json')): shutil.copyfile(path, OUT/(name+'-'+path.name))
    for path in sorted(directory.glob('*.pprof')):
        run(['go','tool','pprof','-top','-nodecount=40','-inuse_space',binary(v,kind),path], OUT/(name+'-'+path.stem+'-heap.txt'))
        run(['go','tool','pprof','-top','-nodecount=0','-inuse_space','-unit=bytes','-nodefraction=0','-focus=ownHeader',binary(v,kind),path], OUT/(name+'-'+path.stem+'-text.txt'))


def alloc(v, kind, regex, name):
    profile = TMP/f'{v}-{name}.pprof'
    run([binary(v,kind),'-test.run=^$','-test.bench='+regex,'-test.benchtime=20000x','-test.memprofilerate=1','-test.memprofile='+str(profile)], OUT/f'{v}-{name}-profile.txt')
    for sample in ['alloc_space','alloc_objects','inuse_space']:
        run(['go','tool','pprof','-top','-nodecount=40','-sample_index='+sample,binary(v,kind),profile], OUT/f'{v}-{name}-{sample}.txt')
        run(['go','tool','pprof','-top','-nodecount=0','-sample_index='+sample,*([] if sample == 'alloc_objects' else ['-unit=bytes']),'-nodefraction=0','-focus=ownHeader',binary(v,kind),profile], OUT/f'{v}-{name}-{sample}-text.txt')


def baseline():
    for i in range(6):
        bench('baseline','request','^BenchmarkParseFragments$','100ms',OUT/'fragment-parser.txt',i>0)
        bench('baseline','server','^BenchmarkProcessStorage$/^(GET|HEAD|AlternatingBytes|AlternatingFields)$','100ms',OUT/'fragment-process.txt',i>0)
    for workers in (1,8,32):
        heap('baseline', workers)
        heap('baseline', workers, 'alternating')
    for case in ('Complete','Early','Middle','Final','LateFallback'):
        alloc('baseline','request','^BenchmarkParseFragments$/^GET$/^'+case+'$', 'fragment-'+case)


def timing():
    groups = {
        'parser': ('request','^BenchmarkParseStorage$','100ms'),
        'ownheader': ('request','^BenchmarkOwnHeader$','100ms'),
        'process': ('server','^BenchmarkProcessStorage$/^(Bytes(68|128|255|256|257|512|513|704|2048)|AlternatingBytes|AlternatingFields)$','100ms'),
        'escaped': ('request','^BenchmarkParse$/^(BrowserHeaders|EscapedHeaders|EscapedPath)$/^Reused$','100ms'),
        'pipe': ('server','^BenchmarkRoundTripStorage$/^Pipe$','100ms'),
        'tcp-confirmation': ('server','^BenchmarkRoundTripStorage$/^TCP$/^Bytes(68|257)$/^Workers32$','200x'),
        'tcp': ('server','^BenchmarkRoundTripStorage$/^TCP$/^Bytes(68|257|704)$/^Workers32$','200x'),
    }
    selected = sys.argv[2:] or [g for g in groups if g != 'tcp-confirmation']
    count = int(os.environ.get('TURBO_STORAGE_SAMPLES', '6'))
    for group in selected:
        kind, regex, duration = groups[group]
        for i in range(count):
            order = list(VARIANTS)
            if i%2: order.reverse()
            for v in order: bench(v,kind,regex,duration,OUT/f'{v}-{group}.txt',i>0)
        for v in list(VARIANTS)[1:]:
            run(['benchstat',OUT/f'baseline-{group}.txt',OUT/f'{v}-{group}.txt'],OUT/f'{v}-{group}-benchstat.txt')


def profiles():
    for v in VARIANTS:
        for workers in (1,8,32):
            for size in (68,257,704):
                for mode in ('dense','sparse'): heap(v,workers,mode,size)
        for size in (68,257,704):
            alloc(v,'request','^BenchmarkParseStorage$/^Bytes'+str(size)+'$', 'size'+str(size))


def validate():
    for v in VARIANTS:
        run(['go','test','-race','-count=1','./...','-timeout=2m'], OUT/f'{v}-race.txt',cwd=source(v))
        for test,kind in [('TestStorageWorkerHeap','server'),('TestStoragePackingHeap','request')]:
            package = '.' if kind == 'server' else './request'
            run(['go','test','-race','-count=1','-run=^'+test+'$',package,'-timeout=60s'], OUT/f'{v}-heap-race-{kind}.txt',cwd=source(v),extra={'TURBO_STORAGE_DIR':str(TMP/f'{v}-race-{kind}'), 'TURBO_STORAGE_WORKERS':'32', 'TURBO_STORAGE_SIZE':'68', 'TURBO_STORAGE_MODE':'sparse'})
        for fuzz in ('FuzzParse','FuzzParserReuse'):
            run(['go','test','./request','-run=^$','-fuzz=^'+fuzz+'$','-fuzztime=10s','-parallel=8'], OUT/f'{v}-{fuzz}.txt',cwd=source(v))


def summarize():
    # Keep instrumented profile timings out of uninstrumented comparisons.
    rows = ['| File | Benchmark | Median ns/op | Median B/op | Median allocs/op |','| --- | --- | ---: | ---: | ---: |']
    samples = {}
    for path in sorted(OUT.glob('*.txt')):
        if path.name.endswith('-profile.txt'): continue
        data = {}
        for line in path.read_text().splitlines():
            m = re.match(r'(Benchmark\S+)\s+\d+\s+([\d.]+) ns/op.*?([\d.]+) B/op\s+([\d.]+) allocs/op',line)
            if m: data.setdefault(m[1],[]).append(tuple(map(float,m.groups()[1:])))
        for name, values in data.items():
            medians = [statistics.median(v[i] for v in values) for i in range(3)]
            rows.append(f'| {path.name} | {name} | '+ ' | '.join(f'{v:g}' for v in medians)+' |')
        if data: samples[path.name] = data
    (HERE/'medians.md').write_text('\n'.join(rows)+'\n')
    (HERE/'timing-samples.json').write_text(json.dumps(samples, indent=2)+'\n')

    def total(path):
        match = re.search(r'Showing nodes accounting for ([\d.]+)([A-Za-z]*)', path.read_text())
        if not match: raise ValueError(f'missing profile total: {path}')
        return float(match[1]) * {'':1, 'B':1, 'kB':1024, 'MB':1024**2}[match[2]]

    rows = ['| Variant | Owned bytes | Text objects / 20,001 parses | Objects/parse | Text allocated bytes/parse |',
            '| --- | ---: | ---: | ---: | ---: |']
    for v in VARIANTS:
        for size in (68,257,704):
            objects = total(OUT/f'{v}-size{size}-alloc_objects-text.txt')
            space = total(OUT/f'{v}-size{size}-alloc_space-text.txt')
            rows.append(f'| {v} | {size} | {objects:g} | {objects/20001:.6f} | {space/20001:.4f} |')
    (HERE/'allocations.md').write_text('\n'.join(rows)+'\n')

    rows = ['| Variant | Parsers | Owned bytes | Clone pattern | Clones | Parser + map/value bytes | Current block bytes | Live text with parsers | Live text after parsers released | Text bytes/clone after release |',
            '| --- | ---: | ---: | --- | ---: | ---: | ---: | ---: | ---: | ---: |']
    for v in VARIANTS:
        for workers in (1,8,32):
            for size in (68,257,704):
                for mode in ('dense','sparse'):
                    name = f'{v}-heap-{workers}-{size}-{mode}'
                    m = json.loads((OUT/(name+'-clones-idle.json')).read_text())
                    parser_profile = OUT/(name+'-parser-storage.txt')
                    if not parser_profile.exists():
                        run(['go','tool','pprof','-top','-unit=bytes','-nodefraction=0','-focus=parseHeaders',binary(v),TMP/name/'clones-idle.pprof'],parser_profile)
                    parser_bytes = m['parser_bytes'] + total(parser_profile)
                    idle = total(OUT/(name+'-clones-idle-text.txt'))
                    stopped = total(OUT/(name+'-clones-stopped-text.txt'))
                    released = total(OUT/(name+'-released-text.txt'))
                    if released: raise ValueError(f'text still live after clone release: {name}')
                    rows.append(f"| {v} | {workers} | {size} | {mode} | {m['clones']} | {parser_bytes:g} | {m['block_bytes']} | {idle:g} | {stopped:g} | {stopped/m['clones']:g} |")
    (HERE/'retention.md').write_text('\n'.join(rows)+'\n')

    rows = ['| Workers | Stage | Live heap bytes | Stack bytes | Live goroutines | Created since previous stage | Clones |',
            '| ---: | --- | ---: | ---: | ---: | ---: | ---: |']
    for workers in (1,8,32):
        for stage in ('initial','cold','warm-active','large-idle','small-idle','stopped-clones','released'):
            m = json.loads((OUT/f'baseline-heap-{workers}-workers-{stage}.json').read_text())
            rows.append(f"| {workers} | {stage} | {m['HeapBytes']} | {m['StackBytes']} | {m['Goroutines']} | {m['CreatedSincePrevious']} | {m['Clones']} |")
    (HERE/'workers.md').write_text('\n'.join(rows)+'\n')


if __name__ == '__main__':
    OUT.mkdir(exist_ok=True, parents=True)
    globals()[sys.argv[1]]()
