#!/usr/bin/env python3
"""Run every Go integration package in its own UTF-8 PostgreSQL database.

Requires local PostgreSQL server/client tools and Go. Does not use or modify an
existing database. Logs are retained. Failed clusters are kept for diagnosis.
Real Codex calls require --real-codex and OFD_REAL_CODEX_VERSION.
"""
import argparse
import concurrent.futures
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import time


OPTIONAL_FIXTURES = {
    'TestAsanaBrowserHarness': 'OPENFOX_ASANA_BROWSER_FILE',
    'TestAzureBrowserHarness': 'OPENFOX_AZURE_BROWSER_FILE',
    'TestBitbucketBrowserHarness': 'OPENFOX_BITBUCKET_BROWSER_FILE',
    'TestBuglyBrowserHarness': 'OPENFOX_BUGLY_BROWSER_FILE',
    'TestCNBBrowserHarness': 'OPENFOX_CNB_BROWSER_FILE',
    'TestCodeBuddyNativeContextToProcess': 'OPENFOX_TEST_CODEBUDDY_SANDBOX_WRAPPER',
    'TestCodeCommitBrowserHarness': 'OPENFOX_I13_BROWSER_FILE',
    'TestCodeupBrowserHarness': 'OPENFOX_CODEUP_BROWSER_FILE',
    'TestDiscordBrowserHarness': 'OPENFOX_I17_BROWSER_FILE',
    'TestGitCodeBrowserHarness': 'OPENFOX_GITCODE_BROWSER_FILE',
    'TestGongfengBrowserHarness': 'OPENFOX_GONGFENG_BROWSER_FILE',
    'TestQQBrowserHarness': 'OPENFOX_I14_BROWSER_FILE',
    'TestShortcutPersonalBrowserHarness': 'OPENFOX_SHORTCUT_BROWSER_FILE',
    'TestSiteBrowserHarness': 'OPENFOX_SITE_BROWSER_FILE',
    'TestSlackBrowserHarness': 'OPENFOX_I15_BROWSER_FILE',
    'TestTAPDPersonalBrowserHarness': 'OPENFOX_TAPD_BROWSER_FILE',
    'TestTeambitionBrowserHarness': 'OPENFOX_P02_BROWSER_FILE',
    'TestTeamsBrowserHarness': 'OPENFOX_I16_BROWSER_FILE',
    'TestTelegramBrowserHarness': 'OPENFOX_I18_BROWSER_FILE',
    'TestYunxiaoBrowserHarness': 'OPENFOX_YUNXIAO_BROWSER_FILE',
    'TestZohoBrowserHarness': 'OPENFOX_I06_BROWSER_FILE',
}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--output', type=Path)
    parser.add_argument('--jobs', type=int, default=2, choices=range(1, 9))
    parser.add_argument('--real-codex', action='store_true')
    parser.add_argument('--browser-only', action='store_true', help='在隔离集群运行浏览器回归')
    parser.add_argument('--keep-database', action='store_true', help='成功时也保留临时集群以便检查')
    args = parser.parse_args()
    if args.real_codex and not os.environ.get('OFD_REAL_CODEX_VERSION'):
        parser.error('--real-codex requires OFD_REAL_CODEX_VERSION')
    missing = [name for name in ('go', 'initdb', 'pg_ctl', 'createdb', 'psql') if not shutil.which(name)]
    if missing:
        parser.error('缺少测试工具，请检查 PATH：' + ', '.join(missing))
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
        index, package, suffix, flags = item
        name = package.rsplit('/', 1)[-1]
        if '/internal/' in package:
            name = 'internal-' + name
        database = f'functional_{index:02d}_{name.replace("-", "_")}'
        if suffix:
            database = ('openfox_i07_' if suffix == 'bugly' else 'i13_') + database
            name += '-' + suffix
        begin = time.monotonic()
        extra_env = {}
        with (output / f'{name}-setup.log').open('w') as log:
            if package.endswith('/internal/integrations'):
                # 渠道测试通过库名前缀确认隔离，各自使用一份已迁移的空库。
                for code, prefix in {'I07': 'openfox_i07', 'I10': 'openfox_i10', 'I13': 'i13',
                                     'I14': 'openfox_i14', 'I15': 'openfox_i15', 'I16': 'i16',
                                     'I17': 'openfox_i17', 'I18': 'i18'}.items():
                    channel_db = f'{prefix}_internal'
                    run(['createdb', '-T', 'functional_template', channel_db], log)
                    extra_env[f'OPENFOX_{code}_TEST_DATABASE_URL'] = dsn(channel_db)
            run(['createdb', '-T', 'functional_template', database], log)
            fixture = 'wp02_wp03' if name in ('wp02_wp03', 'wp05', 'wp06', 'wp16') else 'db'
            run(['psql', '-X', '-v', 'ON_ERROR_STOP=1', '-d', database,
                 '-f', str(repo / 'tests/integration' / fixture / 'fixture.sql')], log)
        url = f'postgresql:///{database}?user=postgres&host={socket}&port=55476&sslmode=disable' if suffix else dsn(database)
        test_env = dict(env, OPENFOX_TEST_DATABASE_URL=url, OPENFOX_TEST_DATABASE_ADMIN_URL=dsn('postgres'),
                        OPENFOX_WP02_WP03_REQUIRE_HTTP='1',
                        OFD_REAL_CODEX='1' if args.real_codex else '0', **extra_env)
        with (output / f'{name}.jsonl').open('w') as log:
            result = subprocess.run(['go', 'test', package, '-count=1', '-json', '-timeout=20m', *flags],
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
        if not args.real_codex:
            permitted_skips.add('TestRealCodexArtifactReview')
        if test_env.get('OFD_REAL_OFFICE_STAGE8') != '1':
            permitted_skips.add('TestRealOfficeStage8')
        permitted_skips.update(test for test, variable in OPTIONAL_FIXTURES.items() if not test_env.get(variable))
        summary['unexpected_skips'] = sorted(set(summary['skip']) - permitted_skips)
        print(json.dumps({k: len(v) if isinstance(v, list) else v for k, v in summary.items()}), flush=True)
        return summary

    try:
        with (output / 'setup.log').open('w') as log:
            run(['initdb', '-D', str(root / 'pg'), '-U', 'postgres', '--auth=trust',
                 '--no-locale', '--encoding=UTF8', '-c', 'shared_memory_type=mmap',
                 '-c', 'dynamic_shared_memory_type=mmap'], log)
            run(['pg_ctl', '-D', str(root / 'pg'), '-w', 'start', '-l', str(root / 'postgres.log'),
                 '-o', f"-k {socket} -p 55476 -c listen_addresses='' -c shared_memory_type=mmap -c dynamic_shared_memory_type=mmap"], log)
            running = True
            if not args.browser_only:
                run(['go', 'build', '-o', str(root / 'server'), './cmd/openfox-server'], log)
                run(['createdb', 'functional_template'], log)
                url_file = root / 'database-url'
                url_file.write_text(dsn('functional_template'))
                url_file.chmod(0o600)
                run([str(root / 'server'), 'migrate', '--apply', '--database-url-file', str(url_file),
                     '--dir', str(repo / 'db/migrations')], log)
        if args.browser_only:
            browser_env = dict(env, OPENFOX_WP11_PG_SOCKET=str(socket), OPENFOX_WP11_PG_PORT=env['PGPORT'])
            result = subprocess.run(['bash', 'tests/frontend/run-live.sh'], cwd=repo, env=browser_env)
            results.append({'suite': 'browser', 'exit_code': result.returncode})
        else:
            packages = subprocess.check_output(
                ['go', 'list', './tests/integration/...', './internal/api', './internal/exports', './internal/integrations'],
                cwd=repo, text=True).splitlines()
            suites = []
            for index, package in enumerate(packages):
                flags = []
                if package.endswith('/stage3'):
                    bugly = 'TestBuglyHTTP(CreateAuthorizationAndDuplicate|BoundsAndPrivateSpace)'
                    codecommit = 'TestCodeCommitAuthenticatedHTTPControl'
                    suites.extend([(index, package, 'bugly', ['-run', f'^{bugly}$']),
                                   (index, package, 'codecommit', ['-run', f'^{codecommit}$'])])
                    flags = ['-skip', f'^({bugly}|{codecommit})$']
                suites.append((index, package, '', flags))
            with concurrent.futures.ThreadPoolExecutor(max_workers=args.jobs) as pool:
                futures = {pool.submit(test_package, item): item[1] for item in suites}
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
        failed = sys.exc_info()[0] is not None or not results or any(
            r.get('setup_error') or r.get('exit_code') or r.get('unexpected_skips')
            or ('package' in r and not (r.get('pass') or r.get('skip'))) for r in results)
        retain = failed or args.keep_database
        if not retain:
            shutil.rmtree(root)
        (output / 'environment.json').write_text(json.dumps({'root': str(root), 'port': 55476,
            'socket': str(socket), 'database_retained': retain, 'encoding': 'UTF8', 'real_codex': args.real_codex,
            'seconds': round(time.monotonic() - started, 2)}, indent=2))
        (output / 'results.json').write_text(json.dumps(results, indent=2))
        print(f'Logs: {output}', flush=True)
        if retain:
            print(f'Database files: {root}', flush=True)
    return int(failed)



if __name__ == '__main__':
    raise SystemExit(main())
