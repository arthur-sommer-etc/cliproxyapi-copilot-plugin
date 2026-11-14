#!/usr/bin/env python3
"""Capture an original Codex app-server image-URL turn through a loopback proxy.

No image preparation or provider request is synthesized here. The caller owns
isolated runtime inputs; the accompanying Go harness captures the actual wire.
"""

import argparse
import asyncio
import json
import os
from pathlib import Path
import shutil
import signal
import subprocess
from urllib.parse import urlparse

def wake_reader_failure(notifications, fatal, reason):
    if not fatal.done():
        fatal.set_result(reason)
    try:
        notifications.put_nowait({'method': 'child/reader_failure', 'reason': reason})
    except asyncio.QueueFull:
        pass


async def read_rpc_stdout(stream, pending, notifications, fatal, record):
    try:
        while line := await stream.readline():
            frame = json.loads(line)
            record('original_app_server_to_driver', frame)
            future = pending.get(frame.get('id'))
            if future is not None and not future.done():
                future.set_result(frame)
            elif 'method' in frame:
                await notifications.put(frame)
        wake_reader_failure(notifications, fatal, 'child_exited')
    except Exception as error:
        wake_reader_failure(notifications, fatal, 'stdout_reader_' + type(error).__name__)


async def read_rpc_stderr(stream, output, notifications, fatal):
    try:
        while chunk := await stream.read(65536):
            output.write(chunk)
            output.flush()
    except Exception as error:
        wake_reader_failure(notifications, fatal, 'stderr_reader_' + type(error).__name__)


async def await_rpc_reply(reply, fatal, timeout=30):
    if not fatal.done():
        done, _ = await asyncio.wait((reply, fatal), timeout=timeout, return_when=asyncio.FIRST_COMPLETED)
        if not done:
            raise RuntimeError('rpc_timeout')
    if fatal.done():
        raise RuntimeError(fatal.result())
    return reply.result()


async def run(args):
    os.umask(0o077)
    root = Path(args.output_root) / 'app-server'
    root.mkdir(mode=0o700)
    codex_home = root / 'codex-home'
    codex_home.mkdir(mode=0o700)
    home = root / 'home'
    home.mkdir(mode=0o700)
    catalog = root / 'models.json'
    shutil.copyfile(os.environ['CPA_LIVE_CODEX_CATALOG'], catalog)
    catalog.chmod(0o600)
    binary = os.environ['CPA_LIVE_CODEX_BINARY']
    version = subprocess.run([binary, '--version'], text=True, capture_output=True, check=True, timeout=15).stdout.strip()
    if version != 'codex-cli ' + os.environ['CPA_LIVE_CODEX_EXPECTED_VERSION']:
        raise RuntimeError('version_mismatch')
    parsed = urlparse(args.base_url)
    if parsed.scheme != 'http' or parsed.hostname not in ('127.0.0.1', 'localhost') or parsed.path != '/v1':
        raise RuntimeError('non_loopback_provider')
    (codex_home / 'config.toml').write_text(f'''model = "gpt-6-luna"
model_provider = "task_proxy"
model_catalog_json = "{catalog}"
approval_policy = "never"
sandbox_mode = "read-only"
web_search = "disabled"
[model_providers.task_proxy]
name = "Task proxy"
base_url = "{args.base_url}"
env_key = "TASK_PROXY_API_KEY"
wire_api = "responses"
supports_websockets = false
request_max_retries = 0
stream_max_retries = 0
''')
    env = {'PATH': '/usr/bin:/bin:/opt/homebrew/bin', 'HOME': str(home), 'CODEX_HOME': str(codex_home), 'TMPDIR': str(root), 'TASK_PROXY_API_KEY': os.environ['TASK_PROXY_API_KEY']}
    frames = (root / 'rpc.jsonl').open('x')
    try:
        stderr = (root / 'stderr.txt').open('xb')
    except BaseException:
        frames.close()
        raise
    try:
        child = await asyncio.create_subprocess_exec('/usr/bin/sandbox-exec', '-f', os.environ['CPA_LIVE_CODEX_SANDBOX'], binary, 'app-server', '--strict-config', '--stdio', cwd=root, env=env, stdin=asyncio.subprocess.PIPE, stdout=asyncio.subprocess.PIPE, stderr=asyncio.subprocess.PIPE, start_new_session=not args.supervised, limit=4<<20)
    except BaseException:
        frames.close()
        stderr.close()
        raise
    pending = {}
    queue = asyncio.Queue(maxsize=64)
    fatal = asyncio.get_running_loop().create_future()
    loop = asyncio.get_running_loop()
    # Leave time to reap the child before the enclosing four-minute deadline.
    operation_deadline = loop.time() + 210
    sequence = 0
    def record(direction, frame):
        frames.write(json.dumps({'direction': direction, 'frame': frame}, separators=(',', ':')) + '\n')
        frames.flush()
        os.fsync(frames.fileno())
    readers = [asyncio.create_task(read_rpc_stdout(child.stdout, pending, queue, fatal, record)),
               asyncio.create_task(read_rpc_stderr(child.stderr, stderr, queue, fatal))]
    async def request(method, params):
        if fatal.done():
            raise RuntimeError(fatal.result())
        nonlocal sequence
        sequence += 1
        frame = {'jsonrpc': '2.0', 'id': sequence, 'method': method, 'params': params}
        future = asyncio.get_running_loop().create_future()
        pending[sequence] = future
        record('driver_to_original_app_server', frame)
        child.stdin.write((json.dumps(frame) + '\n').encode())
        await asyncio.wait_for(child.stdin.drain(), timeout=min(args.rpc_timeout, max(0, operation_deadline - loop.time())))
        try:
            reply = await await_rpc_reply(future, fatal, min(args.rpc_timeout, max(0, operation_deadline - loop.time())))
        finally:
            pending.pop(sequence, None)
            if not future.done():
                future.cancel()
        if 'error' in reply:
            raise RuntimeError(method + '_rejected')
        return reply['result']
    outcome = {'completed': False, 'text': '', 'error': '', 'cli_version': version, 'model': 'gpt-6-luna', 'effort': 'medium'}
    try:
        await request('initialize', {'clientInfo': {'name': 'task-https-image-driver', 'version': '1.0.0'}, 'capabilities': {}})
        frame = {'jsonrpc': '2.0', 'method': 'initialized'}
        record('driver_to_original_app_server', frame)
        child.stdin.write((json.dumps(frame) + '\n').encode())
        await asyncio.wait_for(child.stdin.drain(), timeout=min(args.rpc_timeout, max(0, operation_deadline - loop.time())))
        thread = await request('thread/start', {'model': 'gpt-6-luna', 'modelProvider': 'task_proxy', 'cwd': str(root), 'approvalPolicy': 'never', 'sandbox': 'read-only', 'allowProviderModelFallback': False})
        if thread.get('model') != 'gpt-6-luna':
            raise RuntimeError('thread_model_changed')
        thread_id = thread['thread']['id']
        turn = await request('turn/start', {'threadId': thread_id, 'model': 'gpt-6-luna', 'effort': 'medium', 'input': [{'type': 'text', 'text': 'Inspect the attached image. Reply exactly TOP=<top half color>;BOTTOM=<bottom half color>, using lowercase color names. Do not use tools.'}, {'type': 'image', 'url': args.image_url, 'detail': 'high'}]})
        turn_id = turn['turn']['id']
        turn_deadline = min(operation_deadline, loop.time() + args.turn_timeout)
        while True:
            if fatal.done():
                raise RuntimeError(fatal.result())
            try:
                remaining = turn_deadline - loop.time()
                if remaining <= 0:
                    raise asyncio.TimeoutError
                notice = await asyncio.wait_for(queue.get(), timeout=remaining)
            except asyncio.TimeoutError as error:
                raise RuntimeError('turn_timeout') from error
            if notice['method'] == 'child/reader_failure':
                raise RuntimeError(notice['reason'])
            params = notice.get('params', {})
            if params.get('threadId') != thread_id:
                continue
            if notice['method'] == 'item/completed' and params.get('turnId') == turn_id and params.get('item', {}).get('type') == 'agentMessage':
                outcome['text'] += params['item'].get('text', '')
            if notice['method'] == 'turn/completed' and params.get('turn', {}).get('id') == turn_id:
                outcome['completed'] = params['turn'].get('status') == 'completed'
                if not outcome['completed']:
                    outcome['error'] = 'turn_failed'
                break
    except Exception as error:
        outcome['error'] = str(error)
    finally:
        try:
            (root / 'summary.json').write_text(json.dumps(outcome, indent=2))
        finally:
            try:
                try:
                    if args.supervised:
                        if child.returncode is None:
                            child.kill()
                    else:
                        os.killpg(child.pid, signal.SIGKILL)
                except ProcessLookupError:
                    pass
                try:
                    await asyncio.wait_for(child.wait(), timeout=5)
                except asyncio.TimeoutError:
                    try:
                        if args.supervised:
                            child.kill()
                        else:
                            os.killpg(child.pid, signal.SIGKILL)
                    except ProcessLookupError:
                        pass
                    await child.wait()
                for reader in readers:
                    reader.cancel()
                await asyncio.gather(*readers, return_exceptions=True)
            finally:
                frames.close()
                stderr.close()
    print(json.dumps(outcome), flush=True)
    return 0 if outcome['completed'] and not outcome['error'] else 1

if __name__ == '__main__':
    parser = argparse.ArgumentParser()
    parser.add_argument('--base-url', required=True)
    parser.add_argument('--output-root', required=True)
    parser.add_argument('--image-url', required=True)
    parser.add_argument('--rpc-timeout', type=float, default=30)
    parser.add_argument('--turn-timeout', type=float, default=180)
    parser.add_argument('--supervised', action='store_true', help='inherit the caller process group for tree-wide timeout cleanup')
    args = parser.parse_args()
    raise SystemExit(asyncio.run(run(args)))
