import argparse
import datetime
import hashlib
import json
import os
import plistlib
import signal
import subprocess
import sys
import uuid
from pathlib import Path

REPO = Path(__file__).resolve().parents[3]
CONTROL = REPO / 'internal/app/connection_native_integration_test.go'
CONTROL_SHA = '730361c4f47456ea8b84adbe1786862272cb87d443c023a9925bf2415125bb74'
SELECTOR = '^TestConnectionNativeSecureControlE2E$'


def sha(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def write_json(path, value):
    path.write_text(json.dumps(value, indent=2) + '\n')


def now():
    return datetime.datetime.now(datetime.timezone.utc).isoformat()


def source_inputs():
    paths = {CONTROL, REPO / 'go.mod', REPO / 'go.sum', REPO / 'scripts/gui-go.py',
             Path(__file__).resolve(), Path(__file__).with_name('probe.swift')}
    paths.update(path for path in REPO.rglob('*.go')
                 if not {'.git', 'node_modules', 'dist'}.intersection(path.relative_to(REPO).parts))
    paths.update((REPO / 'patches').glob('*.patch'))
    paths.update((REPO / 'locales').glob('*.json'))
    return {str(path.relative_to(REPO)): sha(path) for path in sorted(paths)}


def command(output, name, argv, env=None, cwd=None):
    record = {'argv': argv, 'cwd': str(cwd or REPO), 'started_at': now(),
              'pid': None, 'raw_returncode': None, 'signal': None, 'received_signals': []}
    record_path = output / 'logs' / (name + '.command.json')
    with record_path.open('x') as stream:
        stream.write(json.dumps(record, indent=2) + '\n')
    process = None
    handlers = {}

    def forward(signum, frame):
        record['received_signals'].append(signum)
        write_json(record_path, record)
        if process is not None and process.poll() is None:
            process.send_signal(signum)

    try:
        for signum in (signal.SIGINT, signal.SIGTERM, signal.SIGHUP):
            handlers[signum] = signal.signal(signum, forward)
        with (output / 'logs' / (name + '.stdout')).open('xb') as stdout, \
                (output / 'logs' / (name + '.stderr')).open('xb') as stderr:
            process = subprocess.Popen(argv, cwd=cwd or REPO, env=env, stdout=stdout, stderr=stderr)
            record['pid'] = process.pid
            write_json(record_path, record)
            raw = process.wait()
            record.update(raw_returncode=raw, signal=-raw if raw < 0 else None,
                          finished_at=now(), reaped=True)
    except OSError as error:
        record.update(start_error=str(error), finished_at=now(),
                      reaped=process is not None and process.poll() is not None)
        if process is not None:
            record['raw_returncode'] = process.wait()
            record['reaped'] = True
    finally:
        for signum, handler in handlers.items():
            signal.signal(signum, handler)
        write_json(record_path, record)
    raw = record['raw_returncode']
    record['signal'] = -raw if raw is not None and raw < 0 else None
    write_json(record_path, record)
    mapped = 2 if raw is None else (128 - raw if raw < 0 else raw)
    record['helper_exit_code'] = mapped
    write_json(record_path, record)
    return mapped


def load_case(output):
    case = json.loads((output / 'case.json').read_text())
    if case['output'] != str(output) or case['control_sha256'] != sha(CONTROL):
        raise ValueError('case output or frozen control source differs')
    if case['runner_sha256'] != sha(Path(__file__)) or case['probe_source_sha256'] != sha(Path(__file__).with_name('probe.swift')):
        raise ValueError('case harness source changed; preserve it and prepare a new case')
    return case


def prepare(output):
    if sha(CONTROL) != CONTROL_SHA:
        raise ValueError('native control scene differs from the frozen source')
    output.mkdir(mode=0o700)
    identifier = uuid.uuid4().hex
    app = output / ('NagiSecureControlE2E-' + identifier + '.app')
    executable = app / 'Contents/MacOS/NagiSecureControlE2E'
    executable.parent.mkdir(parents=True)
    (app / 'Contents/Resources').mkdir()
    for directory in ('logs', 'tmp', 'go-tmp', 'bin'):
        (output / directory).mkdir(mode=0o700)
    bundle_id = 'org.nagi.secure-control-e2e.' + identifier
    with (app / 'Contents/Info.plist').open('xb') as stream:
        plistlib.dump({'CFBundleIdentifier': bundle_id, 'CFBundleName': 'Nagi Secure Control E2E',
                       'CFBundleExecutable': 'NagiSecureControlE2E', 'CFBundlePackageType': 'APPL',
                       'CFBundleVersion': '1', 'LSMinimumSystemVersion': '13.0',
                       'NSHighResolutionCapable': True}, stream)
    case = {'output': str(output), 'case_id': identifier, 'prepared_at': now(),
            'bundle_id': bundle_id, 'app': str(app), 'executable': str(executable),
            'profile': str(output / 'profile'), 'control_sha256': CONTROL_SHA,
            'runner_sha256': sha(Path(__file__)),
            'probe_source_sha256': sha(Path(__file__).with_name('probe.swift')),
            'native_argv': [str(executable), '-test.run=' + SELECTOR, '-test.count=1',
                            '-test.v', '-test.timeout=20m'],
            'scope': 'synthetic production Connection control E2E; no provider or full-product acceptance'}
    write_json(output / 'case.json', case)


def build(output, case, go_bin):
    with (output / 'build.started').open('x') as stream:
        stream.write(now() + '\n')
    env = os.environ.copy()
    env.update(PATH=str(go_bin) + os.pathsep + env['PATH'], GOTOOLCHAIN='local', GOPROXY='off',
               TMPDIR=str(output / 'tmp'), GOTMPDIR=str(output / 'go-tmp'))
    before = source_inputs()
    write_json(output / 'inputs.before.json', before)
    commands = [
        ('toolchain', [str(go_bin / 'go'), 'version']),
        ('compile-native', [sys.executable, str(REPO / 'scripts/gui-go.py'), 'test',
                            '-mod=readonly', '-c', '-tags=nagi_native_integration',
                            '-o', case['executable'], './internal/app']),
        ('compile-probe', ['/usr/bin/swiftc', str(Path(__file__).with_name('probe.swift')),
                           '-o', str(output / 'bin/ax-probe')]),
        ('sign', ['/usr/bin/codesign', '--force', '--sign', '-', case['app']]),
        ('verify-signature', ['/usr/bin/codesign', '--verify', '--strict', case['app']]),
        ('buildinfo', [str(go_bin / 'go'), 'version', '-m', case['executable']]),
    ]
    result = 0
    for name, argv in commands:
        result = command(output, name, argv, env)
        if result != 0:
            break
        if name == 'toolchain' and 'go1.27.3 darwin/arm64' not in (output / 'logs/toolchain.stdout').read_text():
            result = 2
            break
    after = source_inputs()
    write_json(output / 'inputs.after.json', after)
    record = {'helper_exit_code': result, 'inputs_unchanged': before == after,
              'env': {name: env.get(name) for name in ('GOTOOLCHAIN', 'GOPROXY', 'GOSUMDB',
                                                       'GOCACHE', 'GOMODCACHE', 'TMPDIR', 'GOTMPDIR')}}
    if result == 0 and before == after:
        record.update(executable_sha256=sha(Path(case['executable'])),
                      probe_sha256=sha(output / 'bin/ax-probe'),
                      plist_sha256=sha(Path(case['app']) / 'Contents/Info.plist'))
    write_json(output / 'build.json', record)
    return result or (0 if before == after else 2)


def launch(output, case):
    if (output / 'profile').exists():
        raise ValueError('existing profile is never reused')
    with (output / 'native.started').open('x') as stream:
        stream.write(now() + '\n')
    build_record = json.loads((output / 'build.json').read_text())
    if build_record['helper_exit_code'] != 0 or not build_record['inputs_unchanged'] or sha(Path(case['executable'])) != build_record['executable_sha256']:
        raise ValueError('build failed or the signed executable changed')
    env = os.environ.copy()
    for name in ('NAGI_GUI_STORE', 'NAGI_GUI_WORKDIR', 'NAGI_CONNECTION_API_KEY', 'MYGO_GENERATE'):
        env.pop(name, None)
    env.update(NAGI_NATIVE_CONTROL_DIR=str(output), MYGO_LOCALE='en', TMPDIR=str(output / 'tmp'))
    write_json(output / 'native-env.json', {name: env[name] for name in ('NAGI_NATIVE_CONTROL_DIR', 'MYGO_LOCALE', 'TMPDIR')})
    result = command(output, 'native', case['native_argv'], env, output)
    record = json.loads((output / 'logs/native.command.json').read_text())
    record['control_result'] = json.loads((output / 'result.json').read_text()) if (output / 'result.json').exists() else None
    record['missing_result'] = record['control_result'] is None
    mapped = result or (0 if record['control_result'] and record['control_result'].get('control_e2e') == 'PASS' else 2)
    record['launch_helper_exit_code'] = mapped
    write_json(output / 'native-result.json', record)
    return mapped


def current_scene(output):
    value = json.loads((output / 'scene.json').read_text())
    child = json.loads((output / 'logs/native.command.json').read_text())
    if child['pid'] != value['pid'] or child.get('finished_at') is not None:
        raise ValueError('scene does not belong to the active owned child')
    return value


def observed(output, phase, scene):
    value = json.loads((output / ('ax-' + phase + '.json')).read_text())
    if value['pid'] != scene['pid'] or value['phase'] != phase:
        raise ValueError('observation identity differs')
    if not all(value.get(name) for name in ('all_queried', 'all_reads_succeeded', 'no_exposure', 'secure_subrole', 'enabled_known')):
        raise ValueError('AX reads are failed/incomplete; native coverage remains UNVERIFIED')
    if value['enabled'] != (phase != 'busy'):
        raise ValueError('AX enabled state differs')


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--output', type=Path, required=True)
    actions = parser.add_subparsers(dest='action', required=True)
    actions.add_parser('prepare')
    compiler = actions.add_parser('build')
    compiler.add_argument('--go-bin', type=Path, required=True)
    actions.add_parser('launch')
    probe = actions.add_parser('probe')
    probe.add_argument('phase', choices=['fresh', 'busy', 'edit'])
    for name in ('release-test', 'finish', 'readback'):
        actions.add_parser(name)
    args = parser.parse_args()
    if not args.output.is_absolute():
        parser.error('--output must be an absolute, fresh external directory')
    output = args.output.resolve()
    if output == REPO or REPO in output.parents:
        parser.error('--output must be outside the checkout')
    if args.action == 'prepare':
        prepare(output)
        return 0
    case = load_case(output)
    if args.action == 'build':
        return build(output, case, args.go_bin.resolve())
    if args.action == 'launch':
        return launch(output, case)
    if args.action == 'readback':
        for name in ('case.json', 'scene.json', 'native-result.json', 'result.json'):
            path = output / name
            if path.exists():
                print(json.dumps({name: json.loads(path.read_text())}, indent=2))
        return 0
    scene = current_scene(output)
    if args.action == 'probe':
        if scene['phase'] != args.phase or not scene['draft_exact']:
            raise ValueError('probe phase or synthetic draft differs')
        return command(output, 'probe-' + args.phase,
                       [str(output / 'bin/ax-probe'), '--output', str(output), '--phase', args.phase])
    if args.action == 'release-test':
        if scene['phase'] != 'busy' or scene['test_calls'] != 1 or not scene['callback_exact']:
            raise ValueError('expected one held exact Test callback')
        observed(output, 'fresh', scene)
        observed(output, 'busy', scene)
    else:
        if scene['phase'] != 'edit' or not scene['saved_reopened'] or not scene['draft_exact'] or scene['save_calls'] != 1 or scene['save_in_flight'] or scene['failure']:
            raise ValueError('saved/Edit sequence is incomplete')
        observed(output, 'edit', scene)
    with (output / args.action).open('x') as stream:
        stream.write('observed\n')
    return 0


if __name__ == '__main__':
    raise SystemExit(main())
