"""Synthetic local reader failures; these tests make no provider requests."""

import asyncio
import unittest

from original_codex_image import await_rpc_reply, read_rpc_stderr, read_rpc_stdout


class OriginalCodexReaderFailureTests(unittest.IsolatedAsyncioTestCase):
    async def test_malformed_rpc_wakes_pending_request_and_turn_wait(self):
        await self.assert_stdout_failure(b"not-json\n", lambda *_: None, "stdout_reader_JSONDecodeError")

    async def test_capture_write_failure_wakes_pending_request_and_turn_wait(self):
        def failed_capture(*_):
            raise OSError("synthetic capture failure")
        await self.assert_stdout_failure(b'{"id":1,"result":{}}\n', failed_capture, "stdout_reader_OSError")

    async def test_eof_wakes_pending_request_and_turn_wait(self):
        await self.assert_stdout_failure(b"", lambda *_: None, "child_exited")

    async def assert_stdout_failure(self, data, recorder, expected):
        stream = asyncio.StreamReader()
        stream.feed_data(data)
        stream.feed_eof()
        future = asyncio.get_running_loop().create_future()
        queue = asyncio.Queue()
        fatal = asyncio.get_running_loop().create_future()
        await read_rpc_stdout(stream, {1: future}, queue, fatal, recorder)
        with self.assertRaisesRegex(RuntimeError, expected):
            await await_rpc_reply(future, fatal)
        self.assertEqual((await queue.get())["reason"], expected)

    async def test_stderr_capture_failure_wakes_both_waiters(self):
        class FailedOutput:
            def write(self, _):
                raise OSError("synthetic stderr failure")
        stream = asyncio.StreamReader()
        stream.feed_data(b"stderr")
        stream.feed_eof()
        future = asyncio.get_running_loop().create_future()
        queue = asyncio.Queue()
        fatal = asyncio.get_running_loop().create_future()
        await read_rpc_stderr(stream, FailedOutput(), queue, fatal)
        with self.assertRaisesRegex(RuntimeError, "stderr_reader_OSError"):
            await await_rpc_reply(future, fatal)
        self.assertEqual((await queue.get())["method"], "child/reader_failure")

    async def test_reader_failure_is_latched_before_a_subsequent_rpc(self):
        stream = asyncio.StreamReader()
        stream.feed_data(b"not-json\n")
        stream.feed_eof()
        queue = asyncio.Queue()
        fatal = asyncio.get_running_loop().create_future()
        await read_rpc_stdout(stream, {}, queue, fatal, lambda *_: None)
        subsequent = asyncio.get_running_loop().create_future()
        with self.assertRaisesRegex(RuntimeError, "stdout_reader_JSONDecodeError"):
            await await_rpc_reply(subsequent, fatal)


if __name__ == "__main__":
    unittest.main()
