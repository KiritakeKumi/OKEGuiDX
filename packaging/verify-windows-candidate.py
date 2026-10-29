"""Verify an extracted Windows candidate, including real encoding and muxing."""
import argparse
import hashlib
import json
from pathlib import Path
import socket
import subprocess
import tempfile
import time
import urllib.request
import zipfile


def run(args, **kw):
    result = subprocess.run(list(map(str, args)), capture_output=True, text=True,
                            encoding='utf-8', errors='replace', timeout=90, **kw)
    if result.returncode not in (0,):
        raise RuntimeError(str(args) + '\n' + result.stdout + '\n' + result.stderr)
    return result.stdout


def verify(archive):
    with tempfile.TemporaryDirectory(prefix='OKEGui candidate ') as temp:
        base = Path(temp)
        with zipfile.ZipFile(archive) as zf:
            if zf.testzip():
                raise RuntimeError('Corrupt ZIP')
            for name in zf.namelist():
                if not (base / name).resolve().is_relative_to(base.resolve()):
                    raise RuntimeError('Unsafe ZIP path')
            zf.extractall(base)
        root = next(base.glob('OKEGuiDX-*'))
        for line in (root / 'SHA256SUMS').read_text().splitlines():
            digest, name = line.split('  ', 1)
            if hashlib.sha256((root / name).read_bytes()).hexdigest() != digest:
                raise RuntimeError('Checksum mismatch: ' + name)
        exe = root / 'okegui.exe'
        common = ['--config', base / 'empty-config.json', '--queue', base / 'queue.json',
                  '--log-dir', base / 'logs']
        report = json.loads(run([exe, 'status', '--json', *common], cwd=base))
        tools = report['capabilities']['tools']
        for name in ['x264', 'x265', 'svtav1', 'ffmpeg', 'ffprobe', 'mkvmerge', 'mkvextract', 'muxer', 'flac', 'tchapter']:
            if name not in tools or not Path(tools[name]['path']).is_relative_to(root):
                raise RuntimeError('Packaged tool not discovered: ' + name)
        if tools['x265']['variant'] != 'kyouko':
            raise RuntimeError('Windows x64 must select Kyouko by default')
        run([exe, '--help'])
        chapters = base / 'chapters.txt'
        chapters.write_text('CHAPTER01=00:00:00.000\nCHAPTER01NAME=Candidate test\n')
        parsed = json.loads(run([root / 'tools/tchapter/tchapter.exe', 'info', chapters, 'ogm']))
        if 'error' in parsed or not parsed.get('entries'):
            raise RuntimeError('Chapter parsing failed')
        ffmpeg = root / 'tools/ffmpeg/ffmpeg.exe'
        y4m, hevc, mkv = (base / name for name in ('clip.y4m', 'clip.hevc', 'clip.mkv'))
        run([ffmpeg, '-y', '-f', 'lavfi', '-i', 'testsrc2=size=128x72:rate=24',
             '-frames:v', '24', '-pix_fmt', 'yuv420p', '-f', 'yuv4mpegpipe', y4m])
        run([root / 'tools/x26x/x265-kyouko.exe', '--input', y4m, '--frames', '24',
             '--preset', 'ultrafast', '--no-info', '--output', hevc])
        run([root / 'tools/mkvtoolnix/mkvmerge.exe', '-o', mkv, hevc])
        probe = json.loads(run([root / 'tools/ffmpeg/ffprobe.exe', '-v', 'error',
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
                                      cwd=base, stdout=output, stderr=output)
            try:
                for _ in range(100):
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
                    json.load(response)
            finally:
                daemon.terminate()
                daemon.wait(timeout=15)
    print('PASS: ZIP/checksums, tool discovery, Kyouko default, chapters, 24-frame encode/mux/decode, Web UI/API')


if __name__ == '__main__':
    parser = argparse.ArgumentParser()
    parser.add_argument('archive', type=Path)
    verify(parser.parse_args().archive)
