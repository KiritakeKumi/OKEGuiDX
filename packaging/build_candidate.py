"""Build a platform candidate from a verified tools run."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import tarfile
import zipfile


def run(args, **kwargs):
    return subprocess.check_output(args, text=True, **kwargs).strip()


TARGETS = {
    'win-x64': ('windows', 'amd64', 'x86_64-w64-mingw32-gcc'),
    'win-arm64': ('windows', 'arm64', 'aarch64-w64-mingw32-gcc'),
    'linux-x64': ('linux', 'amd64', 'gcc'),
    'linux-arm64': ('linux', 'arm64', 'aarch64-linux-gnu-gcc'),
    'linux-riscv64': ('linux', 'riscv64', 'riscv64-linux-gnu-gcc'),
}


def main(default_target=None):
    parser = argparse.ArgumentParser()
    parser.add_argument('--tools-dir', type=Path, required=True)
    parser.add_argument('--tools-source', type=Path, required=True)
    parser.add_argument('--run-metadata', type=Path, required=True)
    parser.add_argument('--output', type=Path, required=True)
    parser.add_argument('--cc')
    parser.add_argument('--target', choices=TARGETS, default=default_target, required=default_target is None)
    args = parser.parse_args()
    goos, arch, compiler = TARGETS[args.target]
    args.cc = args.cc or compiler
    suffix = '.exe' if goos == 'windows' else ''
    repo = Path(__file__).resolve().parents[1]
    metadata = json.loads(args.run_metadata.read_text())
    tools_sha = run(['git', '-C', str(args.tools_source), 'rev-parse', 'HEAD'])
    if metadata['conclusion'] != 'success' or metadata['head_sha'] != tools_sha:
        raise SystemExit('Tools run must be successful and match the checked-out tools source')
    commit = run(['git', '-C', str(repo), 'rev-parse', 'HEAD'])
    name = 'OKEGuiDX-' + args.target + '-candidate-' + commit[:7]
    stage = args.output.resolve() / name
    stage.mkdir(parents=True, exist_ok=False)
    required = ['x26x/x264.exe', 'x26x/x264-tmod.exe', 'x26x/x265.exe',
                'x26x/x265-asuna.exe', 'x26x/x265-kyouko.exe',
                'svtav1/SvtAv1EncApp.exe', 'ffmpeg/ffmpeg.exe', 'ffmpeg/ffprobe.exe',
                'mkvtoolnix/mkvmerge.exe', 'mkvtoolnix/mkvextract.exe',
                'l-smash/muxer.exe', 'l-smash/remuxer.exe', 'flac/flac.exe']
    if args.target != 'win-x64':
        required = [p for p in required if not any(v in p for v in ('-tmod', '-asuna', '-kyouko'))]
    required = [p.replace('.exe', suffix) for p in required]
    for item in required:
        if not (args.tools_dir / item).is_file():
            raise SystemExit('Required artifact missing: ' + item)
    shutil.copytree(args.tools_dir, stage / 'tools')
    for item in required:
        p = stage / 'tools' / item
        p.chmod(p.stat().st_mode | 0o111)
    env = dict(os.environ, GOOS=goos, GOARCH=arch, CGO_ENABLED='0')
    subprocess.run(['go', 'build', '-trimpath', '-ldflags',
                    '-X main.version=0.0.0-candidate.' + commit[:7],
                    '-o', str(stage / ('okegui' + suffix)), './cmd/okegui'],
                   cwd=repo, env=env, check=True)
    native = repo / 'native/tchapter'
    sources = sorted(p for p in (native / 'src').glob('*.c') if not p.name.startswith('tc_test_'))
    sources += sorted((native / 'src/parsers').glob('*.c'))
    chapter = stage / ('tools/tchapter/tchapter' + suffix)
    chapter.parent.mkdir()
    subprocess.run([args.cc, '-std=c11', '-O2', '-static', '-static-libgcc',
                    '-I' + str(native / 'include'), '-I' + str(native / 'src'),
                    *map(str, sources), '-o', str(chapter), '-lm'], check=True)
    shutil.copy(repo / 'LICENSE', stage / 'LICENSE')
    template = 'windows-README.txt' if goos == 'windows' else 'linux-README.txt'
    text = (repo / 'packaging' / template).read_text(encoding='utf-8')
    (stage / 'README.txt').write_text(text.replace('Windows x64', args.target), encoding='utf-8')
    if goos == 'windows':
        launcher = ('@echo off\r\ncd /d "%~dp0"\r\n'
                    'set "APPDATA=%~dp0test-data"\r\n'
                    'if "%~1"=="" goto gui\r\n'
                    '"%~dp0okegui.exe" %*\r\ngoto done\r\n'
                    ':gui\r\n"%~dp0okegui.exe" gui\r\n'
                    ':done\r\nset "okegui_exit=%errorlevel%"\r\n'
                    'if not "%okegui_exit%"=="0" if "%~1"=="" pause\r\n'
                    'exit /b %okegui_exit%\r\n')
        (stage / 'Start-OKEGuiDX.cmd').write_bytes(launcher.encode('ascii'))
    else:
        launcher = stage / 'start-okeguidx.sh'
        launcher.write_text('#!/bin/sh\ncd -- "$(dirname -- "$0")" || exit 1\n'
                            'XDG_CONFIG_HOME="$PWD/test-data"\nexport XDG_CONFIG_HOME\n'
                            'if [ "$#" -eq 0 ]; then set -- gui; fi\nexec ./okegui "$@"\n')
        launcher.chmod(0o755)
    # Keep the exact build instructions and patches next to the provenance.
    source = stage / 'sources/tools-build'
    source.mkdir(parents=True)
    for directory in ('recipes', 'patches', 'targets', 'scripts'):
        shutil.copytree(args.tools_source / directory, source / directory)
    shutil.copy(args.tools_source / 'versions.lock', source / 'versions.lock')
    for license_file in args.tools_source.glob('LICENSE*'):
        if license_file.is_file():
            shutil.copy(license_file, source / license_file.name)
    provenance = {'kind': 'candidate', 'target': args.target, 'main_commit': commit,
                  'tools_commit': tools_sha, 'tools_run_id': metadata['id'],
                  'tools_run_url': metadata['html_url'], 'go': run(['go', 'version']),
                  'cc': run([args.cc, '--version']).splitlines()[0],
                  'external_dependencies': ['VapourSynth/vspipe and script plugins'] + (['qaac for AAC', 'eac3to if selected'] if goos == 'windows' else []),
                  'upstream_versions': (args.tools_source / 'versions.lock').read_text()}
    (stage / 'build-info.json').write_text(json.dumps(provenance, indent=2) + '\n', encoding='utf-8')
    sums = []
    for path in sorted(stage.rglob('*')):
        if path.is_file():
            sums.append(hashlib.sha256(path.read_bytes()).hexdigest() + '  ' + path.relative_to(stage).as_posix())
    (stage / 'SHA256SUMS').write_text('\n'.join(sums) + '\n', encoding='utf-8')
    extension = '.zip' if goos == 'windows' else '.tar.gz'
    archive = args.output / (name + extension)
    if goos == 'windows':
        with zipfile.ZipFile(archive, 'x', zipfile.ZIP_DEFLATED) as zf:
            for path in sorted(stage.rglob('*')):
                if path.is_file():
                    zf.write(path, path.relative_to(stage.parent))
    else:
        with tarfile.open(archive, 'x:gz') as tf:
            tf.add(stage, arcname=name)
    (args.output / (archive.name + '.sha256')).write_text(
        hashlib.sha256(archive.read_bytes()).hexdigest() + '  ' + archive.name + '\n')
    print(archive)


if __name__ == '__main__':
    main()
