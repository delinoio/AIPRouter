#!/usr/bin/env python3
"""Offline Compose maintenance. Never restarts admission after a mutation."""
import argparse
import os
from pathlib import Path
import subprocess
import sys
import tempfile

ROOT = Path(__file__).resolve().parents[2]
COMPOSE = ['docker', 'compose', '--project-directory', str(ROOT / 'packaging/aiprouter'),
           '-f', str(ROOT / 'packaging/aiprouter/compose.yml')]


def run(args, **kwargs):
    # Do not echo environment values, archive contents, or credentials.
    return subprocess.run(COMPOSE + args, check=True, **kwargs)


def backup(destination):
    destination = Path(destination).resolve()
    if destination.exists():
        raise ValueError('Backup destination already exists')
    fd, name = tempfile.mkstemp(prefix='.aiprouter-backup-', dir=destination.parent)
    try:
        with os.fdopen(fd, 'wb') as output:
            run(['exec', '-T', 'postgres', 'pg_dump', '-U', 'aiprouter', '-d', 'aiprouter',
                 '--format=custom', '--exclude-schema=aiprouter_temp', '--no-owner', '--no-acl'], stdout=output)
            output.flush()
            os.fsync(output.fileno())
        # Atomic no-clobber publication. Temporary raw state and master keys are excluded.
        os.link(name, destination)
    finally:
        os.unlink(name)


def offline(command, archive=None):
    run(['stop', 'aiprouter'])
    if command == 'restore':
        # Temporary foreign keys refer to durable tables; discard them before restore.
        run(['exec', '-T', 'postgres', 'psql', '-U', 'aiprouter', '-d', 'aiprouter',
             '-v', 'ON_ERROR_STOP=1', '-c', 'DROP SCHEMA IF EXISTS aiprouter_temp CASCADE'])
        with Path(archive).open('rb') as source:
            run(['exec', '-T', 'postgres', 'pg_restore', '-U', 'aiprouter', '-d', 'aiprouter',
                 '--clean', '--if-exists', '--single-transaction', '--exit-on-error',
                 '--no-owner', '--no-acl'], stdin=source)
        # This must succeed before any serve command. A failure leaves admission stopped.
        run(['run', '--rm', '--no-deps', 'aiprouter', 'migrate'])
        run(['run', '--rm', '--no-deps', 'aiprouter', 'restore-invalidate'])
    elif command == 'rotate':
        run(['run', '--rm', '--no-deps', 'aiprouter', 'reencrypt'])
    elif command == 'upgrade':
        run(['run', '--rm', '--no-deps', 'aiprouter', 'migrate'])
    print('Maintenance completed. Server remains stopped. Review configuration, then explicitly start it.')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('command', choices=['backup', 'restore', 'rotate', 'upgrade'])
    parser.add_argument('archive', nargs='?')
    args = parser.parse_args()
    if args.command in ('backup', 'restore') and not args.archive:
        parser.error('backup/restore requires an archive path')
    if args.command == 'backup':
        backup(args.archive)
    else:
        offline(args.command, args.archive)


if __name__ == '__main__':
    try:
        main()
    except (OSError, ValueError, subprocess.CalledProcessError):
        print('Maintenance failed; do not start admission. Inspect local command diagnostics.', file=sys.stderr)
        sys.exit(1)
