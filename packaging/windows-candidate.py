"""Build an internal Windows x64 candidate from a verified tools run."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import zipfile


def run(args, **kwargs):
    return subprocess.check_output(args, text=True, **kwargs).strip()


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--tools-dir', type=Path, required=True)
    parser.add_argument('--tools-source', type=Path, required=True)
    parser.add_argument('--run-metadata', type=Path, required=True)
    parser.add_argument('--output', type=Path, required=True)
    parser.add_argument('--cc', default='x86_64-w64-mingw32-gcc')
    args = parser.parse_args()
    repo = Path(__file__).resolve().parents[1]
    metadata = json.loads(args.run_metadata.read_text())
    tools_sha = run(['git', '-C', str(args.tools_source), 'rev-parse', 'HEAD'])
    if metadata['conclusion'] != 'success' or metadata['head_sha'] != tools_sha:
        raise SystemExit('Tools run must be successful and match the checked-out tools source')
    commit = run(['git', '-C', str(repo), 'rev-parse', 'HEAD'])
    name = 'OKEGuiDX-win-x64-candidate-' + commit[:7]
    stage = args.output.resolve() / name
    stage.mkdir(parents=True, exist_ok=False)
    required = ['x26x/x264.exe', 'x26x/x264-tmod.exe', 'x26x/x265.exe',
                'x26x/x265-asuna.exe', 'x26x/x265-kyouko.exe',
                'svtav1/SvtAv1EncApp.exe', 'ffmpeg/ffmpeg.exe', 'ffmpeg/ffprobe.exe',
                'mkvtoolnix/mkvmerge.exe', 'mkvtoolnix/mkvextract.exe',
                'l-smash/muxer.exe', 'l-smash/remuxer.exe', 'flac/flac.exe']
    for item in required:
        if not (args.tools_dir / item).is_file():
            raise SystemExit('Required artifact missing: ' + item)
    shutil.copytree(args.tools_dir, stage / 'tools')
    env = dict(os.environ, GOOS='windows', GOARCH='amd64', CGO_ENABLED='0')
    subprocess.run(['go', 'build', '-trimpath', '-ldflags',
                    '-X main.version=0.0.0-candidate.' + commit[:7],
                    '-o', str(stage / 'okegui.exe'), './cmd/okegui'],
                   cwd=repo, env=env, check=True)
    native = repo / 'native/tchapter'
    sources = sorted(p for p in (native / 'src').glob('*.c') if not p.name.startswith('tc_test_'))
    sources += sorted((native / 'src/parsers').glob('*.c'))
    chapter = stage / 'tools/tchapter/tchapter.exe'
    chapter.parent.mkdir()
    subprocess.run([args.cc, '-std=c11', '-O2', '-static', '-static-libgcc',
                    '-I' + str(native / 'include'), '-I' + str(native / 'src'),
                    *map(str, sources), '-o', str(chapter), '-lm'], check=True)
    shutil.copy(repo / 'LICENSE', stage / 'LICENSE')
    shutil.copy(repo / 'packaging/windows-README.txt', stage / 'README.txt')
    (stage / 'Start-OKEGuiDX.cmd').write_bytes(
        b'@echo off\r\ncd /d "%~dp0"\r\n"%~dp0okegui.exe" gui\r\nif errorlevel 1 pause\r\n')
    # Keep the exact build instructions and patches next to the provenance.
    source = stage / 'sources/tools-build'
    source.mkdir(parents=True)
    for directory in ('recipes', 'patches', 'targets', 'scripts'):
        shutil.copytree(args.tools_source / directory, source / directory)
    shutil.copy(args.tools_source / 'versions.lock', source / 'versions.lock')
    for license_file in args.tools_source.glob('LICENSE*'):
        if license_file.is_file():
            shutil.copy(license_file, source / license_file.name)
    provenance = {'kind': 'candidate', 'target': 'win-x64', 'main_commit': commit,
                  'tools_commit': tools_sha, 'tools_run_id': metadata['id'],
                  'tools_run_url': metadata['html_url'], 'go': run(['go', 'version']),
                  'cc': run([args.cc, '--version']).splitlines()[0],
                  'external_dependencies': ['VapourSynth/vspipe and script plugins', 'qaac for AAC', 'eac3to if selected'],
                  'upstream_versions': (args.tools_source / 'versions.lock').read_text()}
    (stage / 'build-info.json').write_text(json.dumps(provenance, indent=2) + '\n', encoding='utf-8')
    sums = []
    for path in sorted(stage.rglob('*')):
        if path.is_file():
            sums.append(hashlib.sha256(path.read_bytes()).hexdigest() + '  ' + path.relative_to(stage).as_posix())
    (stage / 'SHA256SUMS').write_text('\n'.join(sums) + '\n', encoding='utf-8')
    archive = args.output / (name + '.zip')
    with zipfile.ZipFile(archive, 'x', zipfile.ZIP_DEFLATED) as zf:
        for path in sorted(stage.rglob('*')):
            if path.is_file():
                zf.write(path, path.relative_to(stage.parent))
    (args.output / (name + '.zip.sha256')).write_text(
        hashlib.sha256(archive.read_bytes()).hexdigest() + '  ' + archive.name + '\n')
    print(archive)


if __name__ == '__main__':
    main()
