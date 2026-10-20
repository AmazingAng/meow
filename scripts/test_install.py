#!/usr/bin/env python3
"""Exercise the real installer in disposable directories; no real credentials/network."""
import hashlib
import io
import os
from pathlib import Path
import pty
import select
import shlex
import subprocess
import tarfile
import tempfile
import time
import unittest

INSTALLER = Path(__file__).resolve().with_name('install.sh')


class InstallTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(prefix='meow-install-test-')
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.tools = self.root / 'tools'
        self.tools.mkdir()
        self.destination = self.root / "computer's tools"
        self.env = dict(os.environ, PATH=str(self.tools) + os.pathsep + os.environ['PATH'],
                        MEOW_TEST_ROOT=str(self.root), MEOW_TEST_OS='Linux', MEOW_TEST_ARCH='x86_64')
        self.executable('uname', '#!/bin/sh\ncase "$1" in -s) echo "$MEOW_TEST_OS";; -m) echo "$MEOW_TEST_ARCH";; esac\n')
        self.executable('curl', '''#!/usr/bin/env python3
import os, pathlib, shutil, sys
args=sys.argv[1:]; root=pathlib.Path(os.environ['MEOW_TEST_ROOT'])
url=next(a for a in args if a.startswith('https://'))
with (root/'downloads').open('a') as out: out.write(url+'\\n')
assert '--proto' in args and '--proto-redir' in args
source=root/('checksums.txt' if url.endswith('/checksums.txt') else url.rsplit('/',1)[1])
if not source.exists(): sys.exit(22)
shutil.copyfile(source,args[args.index('-o')+1])
''')
        self.archives = {}
        for platform in ['linux', 'darwin']:
            for architecture in ['amd64', 'arm64']:
                name = f'meow_0.1.0_{platform}_{architecture}.tar.gz'
                self.archive(name)
        self.manifest()

    def executable(self, name, text):
        path = self.tools / name
        path.write_text(text)
        path.chmod(0o755)

    def archive(self, name, symlink=False):
        data = b'''#!/bin/sh
if [ "$1" = --version ]; then echo 'meow 0.1.0'; exit 0; fi
printf '%s\\n' "$@" > "$MEOW_TEST_ROOT/setup-args"
IFS= read -r key
printf '%s' "$key" > "$MEOW_TEST_ROOT/setup-input"
'''
        path = self.root / name
        with tarfile.open(path, 'w:gz') as tar:
            entry = tarfile.TarInfo('meow')
            entry.mode = 0o755
            if symlink:
                entry.type = tarfile.SYMTYPE
                entry.linkname = '/bin/sh'
                tar.addfile(entry)
            else:
                entry.size = len(data)
                tar.addfile(entry, io.BytesIO(data))
        self.archives[name] = hashlib.sha256(path.read_bytes()).hexdigest()

    def manifest(self):
        (self.root / 'checksums.txt').write_text(''.join(f'{digest}  {name}\n' for name, digest in self.archives.items()))

    def run_installer(self, *args, success=True):
        result = subprocess.run(['/bin/sh', str(INSTALLER), '--install-dir', str(self.destination), *args],
                                env=self.env, capture_output=True, text=True, timeout=15)
        self.assertEqual(result.returncode == 0, success, result.stdout + result.stderr)
        return result

    def test_all_four_platform_architectures(self):
        for platform, architecture, asset in [('Linux', 'x86_64', 'linux_amd64'),
                                              ('Linux', 'aarch64', 'linux_arm64'),
                                              ('Darwin', 'x86_64', 'darwin_amd64'),
                                              ('Darwin', 'arm64', 'darwin_arm64')]:
            with self.subTest(platform=platform, architecture=architecture):
                self.env.update(MEOW_TEST_OS=platform, MEOW_TEST_ARCH=architecture)
                result = self.run_installer()
                self.assertIn('meow 0.1.0', result.stdout)
                self.assertIn(f'/download/v0.1.0/meow_0.1.0_{asset}.tar.gz', (self.root / 'downloads').read_text())
                self.assertTrue(os.access(self.destination / 'meow', os.X_OK))

    def test_pins_requested_version(self):
        self.run_installer('--version', 'v0.1.0')
        self.assertIn('/download/v0.1.0/checksums.txt', (self.root / 'downloads').read_text())
        self.assertNotIn('/latest/', (self.root / 'downloads').read_text())

    def test_checksum_failure_preserves_existing_binary(self):
        self.destination.mkdir()
        original = self.destination / 'meow'
        original.write_text('existing installation')
        (self.root / 'meow_0.1.0_linux_amd64.tar.gz').write_bytes(b'broken download')
        self.assertIn('Checksum mismatch', self.run_installer(success=False).stderr)
        self.assertEqual(original.read_text(), 'existing installation')
        self.assertEqual(list(self.destination.iterdir()), [original])

    def test_failed_download_does_not_install(self):
        (self.root / 'meow_0.1.0_linux_amd64.tar.gz').unlink()
        self.run_installer(success=False)
        self.assertFalse(self.destination.exists())

    def test_rejects_symlink_even_with_matching_checksum(self):
        self.archive('meow_0.1.0_linux_amd64.tar.gz', symlink=True)
        self.manifest()
        self.run_installer(success=False)
        self.assertFalse(self.destination.exists())

    def test_rejects_duplicate_manifest_entries(self):
        p = self.root / 'checksums.txt'
        p.write_text(p.read_text() * 2)
        self.run_installer(success=False)
        self.assertFalse(self.destination.exists())

    def test_unsupported_platform_or_architecture(self):
        for values in [dict(MEOW_TEST_OS='Windows'), dict(MEOW_TEST_OS='Linux', MEOW_TEST_ARCH='armv7l')]:
            self.env.update(values)
            self.run_installer(success=False)
        self.assertFalse((self.root / 'downloads').exists())

    def test_rejects_version_injection_and_missing_values(self):
        for args in [('--version', '../../bad'), ('--version', '$(touch bad)'), ('--server',), ('--unknown',)]:
            self.run_installer(*args, success=False)
        self.assertFalse((self.root / 'downloads').exists())

    def test_piped_install_runs_setup_with_terminal_input_and_exact_server(self):
        server = "https://example.com/o'neil?label=a&value=b"
        args = ['--install-dir', str(self.destination), '--server', server]
        command = f'cat {shlex.quote(str(INSTALLER))} | /bin/sh -s -- ' + shlex.join(args)
        pid, descriptor = pty.fork()
        if pid == 0:
            os.execve('/bin/sh', ['sh', '-c', command], self.env)
        output = b''
        try:
            os.write(descriptor, b'meow_fixture_only\n')
            deadline = time.monotonic() + 15
            status = None
            while time.monotonic() < deadline:
                if select.select([descriptor], [], [], 0.1)[0]:
                    try:
                        output += os.read(descriptor, 8192)
                    except OSError:
                        pass
                finished, value = os.waitpid(pid, os.WNOHANG)
                if finished:
                    status = value
                    break
            if status is None:
                os.kill(pid, 9)
                os.waitpid(pid, 0)
                self.fail('Piped installer timed out: ' + output.decode(errors='replace'))
            self.assertEqual(os.waitstatus_to_exitcode(status), 0, output.decode(errors='replace'))
            self.assertEqual((self.root / 'setup-args').read_text().splitlines(), ['setup', '--server', server])
            self.assertEqual((self.root / 'setup-input').read_text(), 'meow_fixture_only')
        finally:
            os.close(descriptor)


if __name__ == '__main__':
    unittest.main(verbosity=2)
