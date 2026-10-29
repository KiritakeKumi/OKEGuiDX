"""Verify an extracted Windows candidate, including real encoding and muxing."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import socket
import subprocess
import tempfile
import tarfile
import time
import urllib.request
import zipfile


def run(args, allowed=(0,), **kw):
    result = subprocess.run(list(map(str, args)), capture_output=True, text=True,
                            encoding='utf-8', errors='replace', timeout=180, **kw)
    if result.returncode not in allowed:
        raise RuntimeError(str(args) + '\n' + result.stdout + '\n' + result.stderr)
    return result.stdout


def verify(archive):
    with tempfile.TemporaryDirectory(prefix='OKEGui candidate ') as temp:
        base = Path(temp)
        if archive.suffix == '.zip':
            with zipfile.ZipFile(archive) as zf:
                if zf.testzip():
                    raise RuntimeError('Corrupt ZIP')
                for name in zf.namelist():
                    if not (base / name).resolve().is_relative_to(base.resolve()):
                        raise RuntimeError('Unsafe ZIP path')
                zf.extractall(base)
        else:
            with tarfile.open(archive) as tf:
                tf.extractall(base, filter='data')
        root = next(base.glob('OKEGuiDX-*'))
        for line in (root / 'SHA256SUMS').read_text().splitlines():
            digest, name = line.split('  ', 1)
            if hashlib.sha256((root / name).read_bytes()).hexdigest() != digest:
                raise RuntimeError('Checksum mismatch: ' + name)
        manifest = json.loads((root / 'build-info.json').read_text())
        target = manifest['target']
        suffix = '.exe' if target.startswith('win-') else ''
        exe = root / ('okegui' + suffix)
        # A poisoned outside profile proves the launcher selects its own state.
        outside = base / 'outside-user'
        (outside / 'OKEGuiDX').mkdir(parents=True)
        old_config = outside / 'OKEGuiDX/OKEGuiConfig.json'
        old_config.write_text('{"logLevel":"WARN"}')
        outside_env = dict(os.environ)
        config_env = 'APPDATA' if suffix else 'XDG_CONFIG_HOME'
        outside_env[config_env] = str(outside)
        launcher = root / ('Start-OKEGuiDX.cmd' if suffix else 'start-okeguidx.sh')
        command = ['cmd.exe', '/d', '/c', launcher] if suffix else [launcher]
        report = json.loads(run([*command, 'status', '--json'], cwd=base, env=outside_env))
        state_root = root / 'test-data'
        if not Path(report['queue_path']).is_relative_to(state_root):
            raise RuntimeError('Launcher reused the outside user queue')
        wanted_arch = {'x64': 'amd64', 'arm64': 'arm64', 'riscv64': 'riscv64'}[target.split('-', 1)[1]]
        if report['capabilities']['arch'] != wanted_arch:
            raise RuntimeError('Incorrect packaged architecture')
        isolated_env = dict(os.environ)
        isolated_env[config_env] = str(state_root)
        common = []
        tools = report['capabilities']['tools']
        for name in ['x264', 'x265', 'svtav1', 'ffmpeg', 'ffprobe', 'mkvmerge', 'mkvextract', 'muxer', 'flac', 'tchapter']:
            if name not in tools or not Path(tools[name]['path']).is_relative_to(root):
                raise RuntimeError('Packaged tool not discovered: ' + name)
        expected = 'kyouko' if target == 'win-x64' else 'upstream'
        if tools['x265']['variant'] != expected:
            raise RuntimeError('Unexpected default x265 variant')
        print('PASS: tool discovery and default ' + expected, flush=True)
        run([exe, '--help'])
        chapters = base / 'chapters.txt'
        chapters.write_text('CHAPTER01=00:00:00.000\nCHAPTER01NAME=Candidate test\n')
        parsed = json.loads(run([root / ('tools/tchapter/tchapter' + suffix), 'info', chapters, 'ogm']))
        if 'error' in parsed or not parsed.get('entries'):
            raise RuntimeError('Chapter parsing failed')
        ffmpeg = root / ('tools/ffmpeg/ffmpeg' + suffix)
        y4m, hevc, mkv = (base / name for name in ('clip.y4m', 'clip.hevc', 'clip.mkv'))
        run([ffmpeg, '-y', '-f', 'lavfi', '-i', 'testsrc2=size=128x72:rate=24',
             '-frames:v', '24', '-pix_fmt', 'yuv420p', '-f', 'yuv4mpegpipe', y4m])
        run([root / ('tools/x26x/' + ('x265-kyouko' if target == 'win-x64' else 'x265') + suffix), '--input', y4m, '--frames', '24',
             '--preset', 'ultrafast', '--no-info', '--pools', '2', '--frame-threads', '1', '--output', hevc])
        run([root / ('tools/mkvtoolnix/mkvmerge' + suffix), '-o', mkv, hevc], allowed=(0, 1))
        probe = json.loads(run([root / ('tools/ffmpeg/ffprobe' + suffix), '-v', 'error',
                               '-count_frames', '-show_streams', '-of', 'json', mkv]))
        if not any(s.get('codec_name') == 'hevc' and s.get('nb_read_frames') == '24'
                   for s in probe['streams']):
            raise RuntimeError('Encoded MKV is not a decodable 24-frame HEVC stream')
        with socket.socket() as sock:
            sock.bind(('127.0.0.1', 0))
            port = sock.getsockname()[1]
        log = base / 'daemon.log'
        with log.open('w') as output:
            daemon = subprocess.Popen(list(map(str, [exe, 'daemon', '--addr', '127.0.0.1:' + str(port), *common])),
                                      cwd=base, stdout=output, stderr=output, env=isolated_env)
            try:
                for _ in range(300):
                    if daemon.poll() is not None:
                        raise RuntimeError('Daemon stopped: ' + log.read_text(errors='replace'))
                    try:
                        with urllib.request.urlopen('http://127.0.0.1:' + str(port) + '/', timeout=1) as response:
                            html = response.read().decode()
                        break
                    except OSError:
                        time.sleep(0.2)
                else:
                    raise RuntimeError('Web UI startup timed out')
                if 'OKEGuiDX' not in html or '/static/' not in html:
                    raise RuntimeError('Embedded Web UI missing')
                with urllib.request.urlopen('http://127.0.0.1:' + str(port) + '/api/v1/tasks') as response:
                    queue = json.load(response)
                if queue['count'] != 0:
                    raise RuntimeError('Candidate started with stale tasks')
                url = 'http://127.0.0.1:' + str(port) + '/api/v1/config'
                with urllib.request.urlopen(url) as response:
                    config = json.load(response)
                if config['config']['logLevel'] == 'WARN':
                    raise RuntimeError('Settings API read the outside profile')
                config['config']['logLevel'] = 'INFO'
                request = urllib.request.Request(url, data=json.dumps(config).encode(),
                                                 headers={'Content-Type': 'application/json'}, method='PUT')
                with urllib.request.urlopen(request) as response:
                    json.load(response)
                saved = state_root / 'OKEGuiDX/OKEGuiConfig.json'
                if json.loads(saved.read_text())['logLevel'] != 'INFO' or old_config.read_text() != '{"logLevel":"WARN"}':
                    raise RuntimeError('Settings writes escaped the test directory')
            finally:
                daemon.terminate()
                daemon.wait(timeout=15)
    print('PASS: archive/checksums, isolated launcher/config/queue, tool discovery, platform default encoder, chapters, 24-frame encode/mux/decode, Web UI/API')


if __name__ == '__main__':
    parser = argparse.ArgumentParser()
    parser.add_argument('archive', type=Path)
    verify(parser.parse_args().archive)
