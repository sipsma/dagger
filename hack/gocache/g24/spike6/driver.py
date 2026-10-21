#!/usr/bin/env python3
"""Functional mode verifies paths only; timing mode is for linux-vm only."""
import base64
import concurrent.futures as futures
import hashlib
import http.client as http_client
import json
import os
from pathlib import Path
import resource
import re
import subprocess
import threading
import time
import urllib.parse

OUT = Path('/out')
TIMING = os.environ['MODE'] == 'timing'
NONCE = os.environ['NONCE']
AUTH = 'Basic ' + base64.b64encode((os.environ['DAGGER_SESSION_TOKEN'] + ':').encode()).decode()
TLS = threading.local()
INPUT = 'x' * 1024


def http(host, port, method, path, body=None, headers=None):
    conns = getattr(TLS, 'connections', None)
    if conns is None:
        conns = TLS.connections = {}
    key = (host, port)
    if key not in conns:
        conns[key] = http_client.HTTPConnection(host, port, timeout=120)
    conn = conns[key]
    conn.request(method, path, body, headers or {})
    response = conn.getresponse()
    data = response.read()
    return response.status, data


def query(q):
    status, data = http('127.0.0.1', int(os.environ['DAGGER_SESSION_PORT']), 'POST', '/query', json.dumps({'query': q}), {'Authorization': AUTH, 'Content-Type': 'application/json'})
    result = json.loads(data)
    if status != 200 or result.get('errors'):
        raise RuntimeError((status, result))
    return result['data']


def leaf(data):
    while isinstance(data, dict) and len(data) == 1:
        data = next(iter(data.values()))
    return data


def debug(op='', token='', method='GET'):
    return http('dagger-engine', 6060, method, '/debug/spike6?' + urllib.parse.urlencode({'op': op, 'token': token}))


def resources():
    status, data = debug('resources')
    if status != 200:
        raise RuntimeError(('cannot charge preparation CPU/memory', status, data.decode()))
    return json.loads(data)


def cpu(r):
    return int(dict(line.split() for line in r['cpu.stat'].splitlines())['usage_usec']) / 1000


def qstr(value):
    return json.dumps(value)


def envelope(value):
    return json.dumps({'Parent': {}, 'ParentName': 'Worker', 'Name': 'Work', 'Args': {'value': value}}) + '\n'


def expected(value):
    return hashlib.sha256((INPUT + value).encode()).hexdigest()


def env(expr, name, value):
    return expr + '{withEnvVariable(name:%s,value:%s)' % (qstr(name), qstr(value))


def select(expr, field):
    # Ignore braces in quoted argument values, including the JSON envelope.
    structural = re.sub(r'"(?:\\.|[^"\\])*"', '""', expr)
    return '{' + expr + '{' + field + '}' + '}' * (structural.count('{') + 1)


def execute(base, kind, prepared, token):
    expr = 'node(id:%s){... on Container' % qstr(base)
    expr = env(expr, 'SPIKE6_NONCE', token)
    if prepared:
        expr = env(expr, '_DAGGER_SPIKE6_TOKEN', token)
    if kind == 'trivial':
        args, stdin = ['true'], ''
    elif kind == 'exec':
        # Detect writable-state bleed. /private-call is outside all mounts.
        args = ['sh', '-ec', 'test ! -e /private-call; test ! -e /var/lib/dagger; touch /private-call; cat input; printf "\\n"']
        stdin = ''
    else:
        args, stdin = ['/runtime'], envelope('same-input')
    expr += '{withExec(args:%s,stdin:%s,disableDaggerInDagger:%s)' % (qstr(args), qstr(stdin), 'true' if kind in ('exec', 'trivial') else 'false')
    out = leaf(query(select(expr, 'stdout')))
    if kind == 'trivial':
        assert out == '', repr(out)
    elif kind == 'exec':
        assert out == INPUT + '\n', repr(out)
    else:
        lines = out.splitlines()
        assert lines[0] == 'ready' and json.loads(lines[1]) == expected('same-input'), lines
    return time.perf_counter_ns()


def one(pool, base, kind, prepared, token):
    start = time.perf_counter_ns()
    if not prepared:
        end = execute(base, kind, False, token)
        return {'total_ms': (end-start)/1e6}
    task = pool.submit(execute, base, kind, True, token)
    deadline = time.monotonic() + 90
    while True:
        if task.done():
            task.result()
            raise RuntimeError('prepared command finished before release')
        status, data = debug(token=token)
        if status == 200:
            break
        if status not in (404, 409) or time.monotonic() > deadline:
            raise RuntimeError(('barrier readiness', status, data.decode()))
        time.sleep(.001)
    ready_memory = int(resources()['memory.current'])
    ready = time.perf_counter_ns()
    before = time.perf_counter_ns()
    status, data = debug(token=token, method='POST')
    released = time.perf_counter_ns()
    assert status == 200, (status, data)
    end = task.result(timeout=90)
    return {'ready_memory_current_bytes':ready_memory, 'prepare_ms': (ready-start)/1e6, 'release_rtt_ms': (released-before)/1e6,
            'ready_to_result_ms': (end-before)/1e6, 'total_ms': (end-start)/1e6}


def emit(record):
    with (OUT / 'results.jsonl').open('a') as f:
        f.write(json.dumps(record, sort_keys=True) + '\n')
    print(json.dumps(record, sort_keys=True), flush=True)


def engine_case(base, kind, prepared, n, c, rnd):
    stop = threading.Event()
    samples = [resources()]
    sample_errors = []
    def sample():
        try:
            while not stop.wait(.02):
                samples.append(resources())
        except Exception as error:
            sample_errors.append(error)
    sampler = threading.Thread(target=sample)
    sampler.start()
    client_cpu_before = time.process_time_ns()
    start = time.perf_counter_ns()
    rows = []
    try:
        with futures.ThreadPoolExecutor(max_workers=c) as outer, futures.ThreadPoolExecutor(max_workers=c) as inner:
            jobs = [outer.submit(one, inner, base, kind, prepared, f'{NONCE}-{rnd}-{kind}-{prepared}-{c}-{i}') for i in range(n)]
            rows = [j.result() for j in jobs]
    finally:
        stop.set(); sampler.join()
    end = time.perf_counter_ns()
    samples.append(resources())
    if sample_errors:
        raise sample_errors[0]
    result = {'experiment': 'engine', 'kind': kind, 'prepared': prepared, 'n': n, 'c': c, 'round': rnd, 'ok': True}
    if TIMING:
        result.update(client_cpu_ms=(time.process_time_ns()-client_cpu_before)/1e6, wall_ms=(end-start)/1e6, cpu_ms=cpu(samples[-1])-cpu(samples[0]),
                      memory_sampled_peak_bytes=max([int(s['memory.current']) for s in samples] + [r.get('ready_memory_current_bytes',0) for r in rows]),
                      memory_before_bytes=int(samples[0]['memory.current']),
                      memory_after_bytes=int(samples[-1]['memory.current']), rows=rows)
        (OUT / f'resources-r{rnd}-{kind}-{prepared}-c{c}.json').write_text(json.dumps(samples))
    emit(result)


def pipe_worker(persistent, trim, count, path):
    path.mkdir(parents=True)
    (path / 'input').write_text(INPUT)
    environ = dict(os.environ, _DAGGER_SPIKE6_ENVELOPE='1', _DAGGER_SPIKE6_TRIM=str(int(trim)), _DAGGER_SPIKE6_PERSIST=str(int(persistent)))
    process = None
    rows = []
    prep = []
    shutdown = []
    rss_samples = []
    stderr = (path / 'stderr.txt').open('wb')
    def spawn():
        before = time.perf_counter_ns()
        p = subprocess.Popen(['/out/runtime'], stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=stderr, cwd=path, env=environ)
        assert p.stdout.readline() == b'ready\n', 'worker failed; see stderr.txt'
        prep.append((time.perf_counter_ns()-before)/1e6)
        status = Path(f'/proc/{p.pid}/status').read_text()
        rss_samples.append(int(next(line.split()[1] for line in status.splitlines() if line.startswith('VmRSS:'))))
        return p
    start = time.perf_counter_ns()
    if persistent:
        process = spawn()
    for i in range(count):
        before = time.perf_counter_ns()
        if not persistent:
            process = spawn()
        value = str(i)
        process.stdin.write(envelope(value).encode()); process.stdin.flush()
        out = process.stdout.readline()
        assert json.loads(out) == expected(value), out
        response = time.perf_counter_ns()
        if not persistent:
            process.stdin.close()
            assert process.wait() == 0
            shutdown.append((time.perf_counter_ns()-response)/1e6)
        rows.append({'response_ms':(response-before)/1e6, 'including_exit_ms':(time.perf_counter_ns()-before)/1e6})
    if persistent:
        before = time.perf_counter_ns()
        process.stdin.close()
        assert process.wait() == 0
        shutdown.append((time.perf_counter_ns()-before)/1e6)
    stderr.close()
    return {'wall_ms': (time.perf_counter_ns()-start)/1e6, 'startup_ms':prep, 'shutdown_ms':shutdown, 'ready_rss_kb':rss_samples, 'rows':rows}


def pipe_case(persistent, trim, n, c, rnd):
    before = resource.getrusage(resource.RUSAGE_CHILDREN)
    start = time.perf_counter_ns()
    with futures.ThreadPoolExecutor(max_workers=c) as pool:
        workers = list(pool.map(lambda i: pipe_worker(persistent, trim, n//c, OUT / f'pipe-r{rnd}-{persistent}-{trim}-{c}-{i}'), range(c)))
    after = resource.getrusage(resource.RUSAGE_CHILDREN)
    result = {'experiment':'module-pipes', 'persistent':persistent, 'trim':trim, 'n':n, 'c':c, 'round':rnd, 'ok':True}
    if TIMING:
        result.update(wall_ms=(time.perf_counter_ns()-start)/1e6,
                      worker_cpu_ms=1000*(after.ru_utime+after.ru_stime-before.ru_utime-before.ru_stime),
                      children_lifetime_maxrss_kb=after.ru_maxrss, workers=workers)
    emit(result)


def main():
    assert leaf(query('{container{from(address:"alpine:3.20"){withExec(args:["true"],disableDaggerInDagger:true){stdout}}}}')) == ''
    runtime = leaf(query('{moduleSource(refString:"/spike/worker"){asModule{runtime{id}}}}'))
    # Export once outside measurements. Same generated SDK binary in both experiments.
    assert isinstance(runtime, str), ('runtime did not return a Container ID', runtime)
    query(select('node(id:%s){... on Container' % qstr(runtime), 'file(path:"/runtime"){export(path:"/out/runtime")}'))
    os.chmod('/out/runtime', 0o755)
    input_id = leaf(query('{directory{withNewFile(path:"input",contents:%s){file(path:"input"){id}}}}' % qstr(INPUT)))
    bases = {}
    for kind in ['trivial', 'exec', 'module-sdk', 'module-trim']:
        expr = 'container{from(address:"alpine:3.20")' if kind in ('exec','trivial') else 'node(id:%s){... on Container' % qstr(runtime)
        if kind == 'trivial':
            bases[kind] = leaf(query(select(expr, 'id')))
            continue
        expr += '{withMountedFile(path:"/work/input",source:%s){withWorkdir(path:"/work")' % qstr(input_id)
        if kind != 'exec':
            expr = env(expr, '_DAGGER_SPIKE6_ENVELOPE', '1')
            expr = env(expr, '_DAGGER_SPIKE6_TRIM', '1' if kind.endswith('trim') else '0')
        bases[kind] = leaf(query(select(expr,'id')))
    meta = {'engine':'fresh dev engine; switch _DAGGER_SPIKE6=1', 'mode':os.environ['MODE'], 'resources':resources(),
            'module_binary_sha256':hashlib.sha256(Path('/out/runtime').read_bytes()).hexdigest(), 'input_bytes':len(INPUT)}
    (OUT/'meta.json').write_text(json.dumps(meta, indent=2)+'\n')
    rounds = range(1,4) if TIMING else [1]
    for rnd in rounds:
        cs = [1,8] if TIMING else [1,2]
        for c in cs:
            n = 256 if TIMING else c * 2
            order = [False, True] if rnd % 2 else [True, False]
            for kind, base in bases.items():
                # Warm image mounts and executables, but never measured results.
                execute(base, kind, False, f'{NONCE}-warm-{rnd}-{c}-{kind}')
                for prepared in order:
                    engine_case(base, kind, prepared, n, c, rnd)
            for trim in [False, True]:
                for persistent in order:
                    pipe_case(persistent, trim, n, c, rnd)
    status, data = debug()
    assert status == 200 and json.loads(data)['active'] == 0, (status,data)
    (OUT/'PASS').write_text('functional paths and resource accounting complete\n')


if __name__ == '__main__':
    main()
