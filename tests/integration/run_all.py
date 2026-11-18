#!/usr/bin/env python3
"""Run every Go integration package in its own UTF-8 PostgreSQL database.

Requires local PostgreSQL server/client tools and Go. Does not use or modify an
existing database. Logs and stopped database files are retained for diagnosis.
Real Codex calls require --real-codex and OFD_REAL_CODEX_VERSION.
"""
import argparse
import concurrent.futures
import json
import os
from pathlib import Path
import subprocess
import tempfile
import time


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--output', type=Path)
    parser.add_argument('--jobs', type=int, default=2, choices=range(1, 9))
    parser.add_argument('--real-codex', action='store_true')
    args = parser.parse_args()
    if args.real_codex and not os.environ.get('OFD_REAL_CODEX_VERSION'):
        parser.error('--real-codex requires OFD_REAL_CODEX_VERSION')
    repo = Path(__file__).resolve().parents[2]
    output = (args.output or repo / 'work' / ('integration-' + time.strftime('%Y%m%d-%H%M%S'))).resolve()
    output.mkdir(parents=True, exist_ok=True)
    # macOS TMPDIR can exceed PostgreSQL's Unix socket path limit (103 bytes).
    root = Path(tempfile.mkdtemp(prefix='openfox-integration-', dir='/tmp')).resolve()
    socket = root / 'socket'
    socket.mkdir()
    env = dict(os.environ, PGHOST=str(socket), PGPORT='55476', PGUSER='postgres')
    started = time.monotonic()
    results = []
    running = False

    def run(command, log, **kwargs):
        return subprocess.run(command, cwd=repo, env=kwargs.pop('env', env),
                              stdout=log, stderr=subprocess.STDOUT, check=True, **kwargs)

    def dsn(database):
        return f'user=postgres host={socket} port=55476 dbname={database} sslmode=disable'

    def test_package(item):
        index, package = item
        name = package.rsplit('/', 1)[-1]
        database = f'functional_{index:02d}_{name}'
        begin = time.monotonic()
        with (output / f'{name}-setup.log').open('w') as log:
            run(['createdb', '-T', 'functional_template', database], log)
            fixture = 'wp02_wp03' if name in ('wp02_wp03', 'wp16') else 'db'
            run(['psql', '-X', '-v', 'ON_ERROR_STOP=1', '-d', database,
                 '-f', str(repo / 'tests/integration' / fixture / 'fixture.sql')], log)
        test_env = dict(env, OPENFOX_TEST_DATABASE_URL=dsn(database),
                        OPENFOX_WP02_WP03_REQUIRE_HTTP='1',
                        OFD_REAL_CODEX='1' if args.real_codex else '0')
        with (output / f'{name}.jsonl').open('w') as log:
            result = subprocess.run(['go', 'test', package, '-count=1', '-json', '-timeout=20m'],
                                    cwd=repo, env=test_env, stdout=log, stderr=subprocess.STDOUT)
        summary = {'package': package, 'database': database, 'exit_code': result.returncode,
                   'seconds': round(time.monotonic() - begin, 2), 'pass': [], 'fail': [], 'skip': []}
        for line in (output / f'{name}.jsonl').read_text().splitlines():
            try:
                event = json.loads(line)
            except json.JSONDecodeError:
                continue
            if event.get('Test') and event.get('Action') in ('pass', 'fail', 'skip'):
                summary[event['Action']].append(event['Test'])
        permitted_skips = set() if args.real_codex else {'TestDaemonExecutesDispatchedRunWithRealCodex'}
        summary['unexpected_skips'] = sorted(set(summary['skip']) - permitted_skips)
        print(json.dumps({k: len(v) if isinstance(v, list) else v for k, v in summary.items()}), flush=True)
        return summary

    try:
        with (output / 'setup.log').open('w') as log:
            run(['go', 'build', '-o', str(root / 'server'), './cmd/openfox-server'], log)
            run(['initdb', '-D', str(root / 'pg'), '-U', 'postgres', '--auth=trust',
                 '--no-locale', '--encoding=UTF8', '-c', 'shared_memory_type=mmap',
                 '-c', 'dynamic_shared_memory_type=mmap'], log)
            run(['pg_ctl', '-D', str(root / 'pg'), '-w', 'start', '-l', str(root / 'postgres.log'),
                 '-o', f"-k {socket} -p 55476 -c listen_addresses='' -c shared_memory_type=mmap -c dynamic_shared_memory_type=mmap"], log)
            running = True
            run(['createdb', 'functional_template'], log)
            url_file = root / 'database-url'
            url_file.write_text(dsn('functional_template'))
            url_file.chmod(0o600)
            run([str(root / 'server'), 'migrate', '--apply', '--database-url-file', str(url_file),
                 '--dir', str(repo / 'db/migrations')], log)
        packages = subprocess.check_output(['go', 'list', './tests/integration/...'], cwd=repo, text=True).splitlines()
        with concurrent.futures.ThreadPoolExecutor(max_workers=args.jobs) as pool:
            futures = {pool.submit(test_package, item): item[1] for item in enumerate(packages)}
            for future in concurrent.futures.as_completed(futures):
                try:
                    results.append(future.result())
                except Exception as error:
                    results.append({'package': futures[future], 'setup_error': str(error)})
                (output / 'results.json').write_text(json.dumps(results, indent=2))
    finally:
        if running:
            with (output / 'setup.log').open('a') as log:
                stop = subprocess.run(['pg_ctl', '-D', str(root / 'pg'), '-w', 'stop', '-m', 'fast'],
                                      env=env, stdout=log, stderr=subprocess.STDOUT)
                if stop.returncode:
                    results.append({'setup_error': 'could not stop temporary PostgreSQL cluster'})
        (output / 'environment.json').write_text(json.dumps({'root': str(root), 'port': 55476,
            'socket': str(socket), 'encoding': 'UTF8', 'real_codex': args.real_codex,
            'seconds': round(time.monotonic() - started, 2)}, indent=2))
        (output / 'results.json').write_text(json.dumps(results, indent=2))
        print(f'Logs: {output}\nDatabase files: {root}', flush=True)
    return int(any(r.get('setup_error') or r.get('exit_code') or r.get('unexpected_skips') for r in results))


if __name__ == '__main__':
    raise SystemExit(main())
