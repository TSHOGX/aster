#!/usr/bin/env python3
import importlib.util
import sys
from pathlib import Path
import unittest


def _load_gateway():
    path = Path(__file__).resolve().parent / "gateway.py"
    spec = importlib.util.spec_from_file_location("mcp_gateway", path)
    mod = importlib.util.module_from_spec(spec)
    sys.modules["mcp_gateway"] = mod
    spec.loader.exec_module(mod)
    return mod


gw = _load_gateway()


class JsonRpcIdsTest(unittest.TestCase):
    def test_single_tools_call(self):
        body = b'{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{}}'
        self.assertEqual(gw.jsonrpc_request_ids(body), [7])

    def test_skips_notifications(self):
        body = b'{"jsonrpc":"2.0","method":"notifications/cancelled"}'
        self.assertEqual(gw.jsonrpc_request_ids(body), [])

    def test_batch(self):
        body = b'[{"jsonrpc":"2.0","id":"a","method":"x"},{"jsonrpc":"2.0","method":"n"}]'
        self.assertEqual(gw.jsonrpc_request_ids(body), ["a"])


class SseEarlyCompleteTest(unittest.TestCase):
    def test_result_event_completes(self):
        event = (
            b"event: message\n"
            b'data: {"jsonrpc":"2.0","id":1,"result":{"ok":true}}\n'
            b"\n"
            b": keep-alive after result should not be needed\n"
            b"\n"
        )
        rest, done = gw.completed_rpc_ids_from_sse_chunk(event, [1])
        self.assertEqual(done, [1])

    def test_progress_then_result_across_chunks(self):
        pending = [42]
        first = b"event: message\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\"}\n\n"
        rest, done = gw.completed_rpc_ids_from_sse_chunk(first, pending)
        self.assertEqual(done, [])
        second = rest + (
            b"event: message\n"
            b'data: {"jsonrpc":"2.0","id":42,"result":{"content":[]}}\n'
            b"\n"
        )
        rest, done = gw.completed_rpc_ids_from_sse_chunk(second, pending)
        self.assertEqual(done, [42])

    def test_split_event_waits_for_blank_line(self):
        pending = ["call-1"]
        rest, done = gw.completed_rpc_ids_from_sse_chunk(
            b'event: message\ndata: {"jsonrpc":"2.0","id":"call-1","result":{',
            pending,
        )
        self.assertEqual(done, [])
        rest, done = gw.completed_rpc_ids_from_sse_chunk(
            rest + b"}}\n\n", pending
        )
        self.assertEqual(done, ["call-1"])

    def test_error_counts_as_complete(self):
        event = b'data: {"jsonrpc":"2.0","id":3,"error":{"code":-32001,"message":"x"}}\n\n'
        _, done = gw.completed_rpc_ids_from_sse_chunk(event, [3])
        self.assertEqual(done, [3])

    def test_string_id_matches_int_wire_form(self):
        event = b'data: {"jsonrpc":"2.0","id":"9","result":{}}\n\n'
        _, done = gw.completed_rpc_ids_from_sse_chunk(event, [9])
        self.assertEqual(done, [9])

    def test_result_without_trailing_blank_line(self):
        event = (
            b"event: message\n"
            b'data: {"jsonrpc":"2.0","id":3,"result":{"ok":true}}\n'
        )
        _, done = gw.completed_rpc_ids_from_sse_chunk(event, [3])
        self.assertEqual(done, [3])


class ReadUpstreamChunkTest(unittest.TestCase):
    def test_prefers_http_response_read1_not_raw_fp(self):
        class RawFp:
            def read1(self, n=-1):
                return b"1075\r\nraw\r\n0\r\n\r\n"

        class Resp:
            def __init__(self):
                self.fp = RawFp()

            def read1(self, n=-1):
                return b"decoded-sse"

        self.assertEqual(gw.read_upstream_stream_chunk(Resp()), b"decoded-sse")


if __name__ == "__main__":
    unittest.main()
