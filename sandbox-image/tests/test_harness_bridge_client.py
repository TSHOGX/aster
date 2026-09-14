import contextlib
import importlib.util
from importlib.machinery import SourceFileLoader
import json
import os
import subprocess
import tempfile
import threading
import time
import unittest
from pathlib import Path
from unittest import mock


SCRIPT = Path(__file__).resolve().parents[1] / "files" / "usr" / "local" / "bin" / "harness-bridge-client"
BUILD_ROOTFS = Path(__file__).resolve().parents[1] / "build-rootfs.sh"
spec = importlib.util.spec_from_loader("harness_bridge_client", SourceFileLoader("harness_bridge_client", str(SCRIPT)))
bridge = importlib.util.module_from_spec(spec)
spec.loader.exec_module(bridge)

MANIFEST_LIB = SCRIPT.parents[1] / "lib" / "harness_manifest.py"
manifest_spec = importlib.util.spec_from_loader("harness_manifest", SourceFileLoader("harness_manifest", str(MANIFEST_LIB)))
manifest = importlib.util.module_from_spec(manifest_spec)
manifest_spec.loader.exec_module(manifest)


def jline(obj):
    return json.dumps(obj, separators=(",", ":")) + "\n"


class ScriptedPi:
    """A fake pi that REACTS to stdin, modelling the real RPC event nesting.

    The pre-existing fakes in this file are fixed line lists that ignore what the
    client writes. That is precisely why the suite stayed green while the stream
    desync shipped: a list cannot express "agent_end arrives after the turn_end
    the client stops on", so the residue that corrupts the next turn never
    existed in the tests.

    Real pi (verified by spiking pi 0.77.0 directly) frames one prompt as

        agent_start … [turn_start … turn_end]* … agent_end

    so `agent_end` is ALWAYS still unread when the client breaks on the last
    turn_end. An abort additionally leaves response{command:"abort"}.
    """

    def __init__(self, session_file, session_id="pi-session-1", abort_on_prompt=None):
        self.session_file = str(session_file)
        self.session_id = session_id
        # Prompt ids that should end aborted instead of completing normally.
        self.abort_on_prompt = set(abort_on_prompt or ())
        self.pending = []
        self.prompts = []
        self.stdin = self
        self.stdout = self
        self.stderr = iter([])
        self.writes = []

    # -- stdin side -------------------------------------------------------
    def write(self, value):
        self.writes.append(value)
        for chunk in value.splitlines():
            chunk = chunk.strip()
            if chunk:
                self._dispatch(json.loads(chunk))

    def flush(self):
        pass

    def _dispatch(self, req):
        rid = req.get("id")
        kind = req.get("type")
        if kind == "prompt":
            self.prompts.append(req.get("message"))
            self.pending.append(jline({"id": rid, "type": "response", "command": "prompt", "success": True}))
            self.pending.append(jline({"type": "agent_start"}))
            self.pending.append(jline({"type": "turn_start"}))
            self.pending.append(jline({
                "type": "message_update",
                "messageId": f"msg-{len(self.prompts)}",
                "assistantMessageEvent": {"type": "text_delta", "delta": f"reply-{len(self.prompts)}"},
            }))
            if rid in self.abort_on_prompt:
                self.pending.append(jline({"type": "turn_end", "message": {"stopReason": "aborted"}}))
                self.pending.append(jline({"type": "agent_end", "message": {"stopReason": "aborted"}}))
                self.pending.append(jline({"id": f"harness-abort-{rid}", "type": "response", "command": "abort", "success": True}))
                return
            # The boundary that matters: the client breaks on turn_end, and
            # agent_end is left in the pipe for whoever reads next.
            self.pending.append(jline({"type": "turn_end", "status": "completed"}))
            self.pending.append(jline({"type": "agent_end", "status": "completed"}))
            return
        if kind == "get_session_stats":
            self.pending.append(jline({
                "id": rid, "type": "response", "command": "get_session_stats", "success": True,
                "data": {"sessionFile": self.session_file, "sessionId": self.session_id},
            }))
            return
        if kind == "switch_session":
            self.pending.append(jline({"id": rid, "type": "response", "command": "switch_session", "success": True}))
            return
        if kind == "abort":
            self.pending.append(jline({"id": rid, "type": "response", "command": "abort", "success": True}))
            return
        self.pending.append(jline({"id": rid, "type": "response", "command": kind, "success": True}))

    # -- stdout side ------------------------------------------------------
    def readline(self):
        if not self.pending:
            return ""
        return self.pending.pop(0)

    # -- process side -----------------------------------------------------
    def poll(self):
        return None

    def terminate(self):
        pass


class BridgeClientTest(unittest.TestCase):
    def test_build_rootfs_reuse_generates_pi_agent_manifest(self):
        with tempfile.TemporaryDirectory() as root:
            rootfs = Path(root)
            bin_dir = rootfs / "usr" / "local" / "bin"
            bin_dir.mkdir(parents=True)
            pi = bin_dir / "pi"
            pi.write_text("#!/bin/sh\n", encoding="utf-8")
            pi.chmod(0o755)

            env = os.environ.copy()
            env.update(
                {
                    "ROOTFS_DIR": str(rootfs),
                    "SANDBOX_AGENT_DRIVERS": "pi",
                    "FORCE": "0",
                }
            )
            subprocess.run(["bash", str(BUILD_ROOTFS)], check=True, env=env, capture_output=True, text=True)

            manifest = json.loads((rootfs / "etc" / "harness-image" / "agents.json").read_text(encoding="utf-8"))
            self.assertEqual(manifest["build_input"]["sandbox_agent_drivers"], ["pi"])
            self.assertEqual(len(manifest["drivers"]), 1)
            pi_entry = manifest["drivers"][0]
            self.assertEqual(pi_entry["driver_id"], "pi")
            self.assertEqual(pi_entry["package_name"], "@earendil-works/pi-coding-agent")
            self.assertEqual(pi_entry["package_version"], "0.77.0")
            self.assertEqual(pi_entry["event_schema_version"], "pi_rpc_events_v1.0")
            self.assertEqual(pi_entry["binary_path"], "/usr/local/bin/pi")
            self.assertTrue(pi_entry["installed_binary_digest"].startswith("sha256:"))
            self.assertIn("/agent-home/.pi/agent/models.json", pi_entry["installed_config_paths"])

    def test_queue_write_orders_numeric_sequence(self):
        with tempfile.TemporaryDirectory() as root:
            queue = bridge.Queue(root, bridge.OUTBOX)
            queue.write({"message_id": "msg2", "type": "heartbeat", "session_id": "s", "generation_id": "g"})
            queue.write({"message_id": "msg3", "type": "heartbeat", "session_id": "s", "generation_id": "g"})
            first = Path(root) / bridge.OUTBOX / "00000000000000000001.json"
            first.rename(Path(root) / bridge.OUTBOX / "00000000000000000010.json")
            queue.write({"message_id": "msg11", "type": "heartbeat", "session_id": "s", "generation_id": "g"})

            files = queue.read_all()
            self.assertEqual([seq for seq, _, _ in files], [2, 10, 11])
            self.assertEqual([envelope["message_id"] for _, _, envelope in files], ["msg3", "msg2", "msg11"])

    def test_read_all_skips_entries_that_vanish_or_are_half_written(self):
        # The host prunes stale inbox responses while the sandbox may be listing
        # them, and the inbox is read-only here, so an entry disappearing (or
        # being only partially visible) between iterdir and open is routine.
        with tempfile.TemporaryDirectory() as root:
            queue = bridge.Queue(root, bridge.INBOX)
            queue.write({"message_id": "ok", "type": "no_work", "session_id": "s", "generation_id": "g"})
            (queue.dir / "00000000000000000002.json").write_text("{\"truncated\": ", encoding="utf-8")
            ghost = queue.dir / "00000000000000000003.json"
            ghost.write_text("{}", encoding="utf-8")
            real_iterdir = Path.iterdir

            def iterdir_then_prune(self_path):
                entries = list(real_iterdir(self_path))
                if ghost.exists():
                    ghost.unlink()  # pruned by the host after the listing was taken
                return iter(entries)

            with mock.patch.object(Path, "iterdir", iterdir_then_prune):
                files = queue.read_all()
            self.assertEqual([envelope["message_id"] for _, _, envelope in files], ["ok"])

    def test_wait_response_never_reopens_consumed_or_stale_responses(self):
        with tempfile.TemporaryDirectory() as root:
            client = bridge.BridgeClient(root, "sess", "gen", "pi", poll_interval=0.001)
            inbox = bridge.Queue(root, bridge.INBOX)
            base = {"type": "no_work", "session_id": "sess", "generation_id": "gen", "payload": {}}
            inbox.write({**base, "message_id": "first", "request_id": "req_1"})
            self.assertEqual(client.wait_response("req_1", timeout=0.1)["message_id"], "first")
            self.assertEqual(client.inbox_seen_seq, 1)

            # A response to a request we timed out on, then ours. The stale one is
            # consumed (high-water mark advances) but never matched.
            inbox.write({**base, "message_id": "stale", "request_id": "req_timed_out"})
            inbox.write({**base, "message_id": "second", "request_id": "req_2"})
            self.assertEqual(client.wait_response("req_2", timeout=0.1)["message_id"], "second")
            self.assertEqual(client.inbox_seen_seq, 3)

            # Consumed files are not opened again even if the host has not pruned
            # them yet — corrupt the first one to prove it is never read.
            (inbox.dir / "00000000000000000001.json").write_text("not json", encoding="utf-8")
            inbox.write({**base, "message_id": "third", "request_id": "req_3"})
            self.assertEqual(client.wait_response("req_3", timeout=0.1)["message_id"], "third")

    def test_claim_loop_survives_claim_timeout(self):
        # A claim that times out (the host's poll worker was busy) is retried; it
        # used to escape run_claim_loop and, as the container's init, end the
        # sandbox. Here the first claim gets no answer and the second gets a grant.
        with tempfile.TemporaryDirectory() as root:
            from types import SimpleNamespace

            args = SimpleNamespace(
                bridge_dir=root,
                session_id="sess",
                generation_id="gen",
                driver_id="sh",
                poll_interval=0.001,
                timeout=0.05,
                base_url="",
                healthz_statuses="200",
                http_timeout=0.1,
                heartbeat_interval=60,
                idle_interval=0.001,
                max_turns=1,
                max_empty_polls=0,
                max_claim_failures=5,
            )
            inbox = bridge.Queue(root, bridge.INBOX)
            original_write = bridge.Queue.write
            claims = []

            def write_and_respond(queue, envelope):
                message_type = envelope.get("type")
                request_id = envelope.get("request_id")
                if queue.name == bridge.OUTBOX and message_type in {"hello", "probe_network", "claim_next_turn"}:
                    respond = True
                    response_type = "no_work"
                    payload = {}
                    if message_type == "hello":
                        response_type = "hello_ack"
                        payload = {"last_output_sequence_by_turn": {}}
                    elif message_type == "claim_next_turn":
                        claims.append(request_id)
                        if len(claims) == 1:
                            respond = False  # host never answers the first claim
                        else:
                            response_type = "grant"
                            payload = {"turn_id": 9, "sequence": 1, "turn_input_schema": "RunTurn", "input": {"content": "echo ok"}}
                    if respond:
                        inbox.write(
                            {
                                "message_id": f"host_{message_type}_{len(claims)}",
                                "request_id": request_id,
                                "type": response_type,
                                "session_id": "sess",
                                "generation_id": "gen",
                                "payload": payload,
                            }
                        )
                return original_write(queue, envelope)

            class FakeRunner:
                def run_turn(self, content, emit):
                    self.content = content
                    return "completed", "", ""

                def close(self):
                    pass

            runner = FakeRunner()
            with mock.patch.object(bridge.Queue, "write", write_and_respond):
                with mock.patch.object(bridge, "sandbox_source_ip", return_value="10.240.0.2"):
                    with mock.patch.object(bridge.time, "sleep", lambda _s: None):
                        bridge.run_claim_loop(args, runner=runner)

            self.assertEqual(len(claims), 2)
            self.assertEqual(runner.content, "echo ok")

    def test_claim_loop_gives_up_after_sustained_failures(self):
        with tempfile.TemporaryDirectory() as root:
            from types import SimpleNamespace

            args = SimpleNamespace(
                bridge_dir=root,
                session_id="sess",
                generation_id="gen",
                driver_id="sh",
                poll_interval=0.001,
                timeout=0.01,
                base_url="",
                healthz_statuses="200",
                http_timeout=0.1,
                heartbeat_interval=60,
                idle_interval=0.001,
                max_turns=1,
                max_empty_polls=0,
                max_claim_failures=3,
            )
            inbox = bridge.Queue(root, bridge.INBOX)
            original_write = bridge.Queue.write
            claims = []

            def write_and_respond(queue, envelope):
                message_type = envelope.get("type")
                if queue.name == bridge.OUTBOX and message_type in {"hello", "probe_network"}:
                    inbox.write(
                        {
                            "message_id": f"host_{message_type}",
                            "request_id": envelope.get("request_id"),
                            "type": "hello_ack" if message_type == "hello" else "no_work",
                            "session_id": "sess",
                            "generation_id": "gen",
                            "payload": {"last_output_sequence_by_turn": {}},
                        }
                    )
                elif queue.name == bridge.OUTBOX and message_type == "claim_next_turn":
                    claims.append(envelope.get("request_id"))
                return original_write(queue, envelope)

            class FakeRunner:
                def close(self):
                    pass

            with mock.patch.object(bridge.Queue, "write", write_and_respond):
                with mock.patch.object(bridge.time, "sleep", lambda _s: None):
                    with self.assertRaises(TimeoutError):
                        bridge.run_claim_loop(args, runner=FakeRunner())
            self.assertEqual(len(claims), 3)

    def test_hello_records_resume_boundary(self):
        with tempfile.TemporaryDirectory() as root:
            client = bridge.BridgeClient(root, "sess", "gen", "pi", poll_interval=0.001)
            response = bridge.Queue(root, bridge.INBOX)

            sent = client.send("hello", request_id="req_hello")
            response.write(
                {
                    "message_id": "host_msg",
                    "request_id": sent["request_id"],
                    "type": "hello_ack",
                    "session_id": "sess",
                    "generation_id": "gen",
                    "payload": {
                        "last_output_sequence_by_turn": {"42": 7},
                        "leased_turn_id": 42,
                        "server_time": "2026-05-25T00:00:00Z",
                    },
                }
            )

            payload = client.wait_response("req_hello", timeout=0.1)["payload"]
            client.last_output_sequence_by_turn = {
                int(turn_id): int(sequence)
                for turn_id, sequence in payload["last_output_sequence_by_turn"].items()
            }
            client.leased_turn_id = payload["leased_turn_id"]

            self.assertEqual(client.last_output_sequence_by_turn, {42: 7})
            self.assertEqual(client.leased_turn_id, 42)

    def test_hello_advances_resume_boundary_over_pending_outbox(self):
        with tempfile.TemporaryDirectory() as root:
            client = bridge.BridgeClient(root, "sess", "gen", "pi", poll_interval=0.001)
            bridge.Queue(root, bridge.OUTBOX).write(
                {
                    "message_id": "pending_output",
                    "type": "emit_output",
                    "session_id": "sess",
                    "generation_id": "gen",
                    "turn_id": 42,
                    "payload": {"output_sequence": 12, "stream": "stdout", "payload": {"line": "pending"}},
                }
            )
            response = bridge.Queue(root, bridge.INBOX)
            sent = client.send("hello", request_id="req_hello")
            response.write(
                {
                    "message_id": "host_msg",
                    "request_id": sent["request_id"],
                    "type": "hello_ack",
                    "session_id": "sess",
                    "generation_id": "gen",
                    "payload": {"last_output_sequence_by_turn": {"42": 7}},
                }
            )

            with mock.patch.object(
                client,
                "wait_response",
                return_value={"payload": {"last_output_sequence_by_turn": {"42": 7}}},
            ):
                client.hello(timeout=0.1)
            # Sequence 12 is still durable in the local outbox even though the
            # host has only committed through sequence 7.
            self.assertEqual(client.last_output_sequence_by_turn, {42: 12})

    def test_producer_lock_rejects_second_claim_loop(self):
        with tempfile.TemporaryDirectory() as root:
            first = bridge.BridgeClient(root, "sess", "gen", "pi")
            second = bridge.BridgeClient(root, "sess", "gen", "pi")
            first.acquire_producer_lock()
            self.addCleanup(first.release_producer_lock)
            with self.assertRaisesRegex(RuntimeError, "another bridge producer"):
                second.acquire_producer_lock()

    def test_hello_sends_bridge_protocol_v2_identity(self):
        with tempfile.TemporaryDirectory() as root:
            client = bridge.BridgeClient(root, "sess", "gen", "pi", poll_interval=0.001)
            inbox = bridge.Queue(root, bridge.INBOX)
            original_write = bridge.Queue.write
            sent = {}

            def write_and_respond(queue, envelope):
                if queue.name == bridge.OUTBOX and envelope.get("type") == "hello":
                    sent.update(envelope)
                    inbox.write(
                        {
                            "message_id": "host_hello",
                            "request_id": envelope["request_id"],
                            "type": "hello_ack",
                            "session_id": "sess",
                            "generation_id": "gen",
                            "payload": {"last_output_sequence_by_turn": {}},
                        }
                    )
                return original_write(queue, envelope)

            with mock.patch.object(bridge.Queue, "write", write_and_respond):
                client.hello(timeout=0.1)

            self.assertEqual(
                sent["payload"],
                {"driver_id": "pi", "protocol_version": 2, "turn_input_schema": "RunTurn"},
            )

    def test_claim_and_lifecycle_messages(self):
        with tempfile.TemporaryDirectory() as root:
            client = bridge.BridgeClient(root, "sess", "gen", "pi", poll_interval=0.001)
            inbox = bridge.Queue(root, bridge.INBOX)

            sent = client.send("claim_next_turn", request_id="req_claim")
            inbox.write(
                {
                    "message_id": "host_grant",
                    "request_id": sent["request_id"],
                    "type": "grant",
                    "session_id": "sess",
                    "generation_id": "gen",
                    "payload": {"turn_id": 9, "sequence": 1, "turn_input_schema": "RunTurn", "input": {"content": "run"}},
                }
            )
            grant = client.wait_response("req_claim", timeout=0.1)["payload"]
            self.assertEqual(grant["turn_id"], 9)

            client.ack_turn_started(9, "10.240.0.2")
            client.emit_output(9, 1, {"line": "ok"})
            client.ack_turn_completed(9)

            outbox = bridge.Queue(root, bridge.OUTBOX)
            messages = [envelope for _, _, envelope in outbox.read_all()]
            self.assertEqual(
                [message["type"] for message in messages[-3:]],
                ["ack_turn_started", "emit_output", "ack_turn_completed"],
            )
            self.assertEqual(messages[-2]["payload"]["output_sequence"], 1)

    def test_resume_turn_returns_grant_payload(self):
        with tempfile.TemporaryDirectory() as root:
            client = bridge.BridgeClient(root, "sess", "gen", "pi", poll_interval=0.001)

            sent_request_id = None
            original_send = client.send

            def record_send(message_type, request_id=None, turn_id=None, payload=None):
                nonlocal sent_request_id
                envelope = original_send(message_type, request_id=request_id, turn_id=turn_id, payload=payload)
                if message_type == "resume_turn":
                    sent_request_id = envelope["request_id"]
                return envelope

            client.send = record_send
            with mock.patch.object(client, "wait_response") as wait_response:
                wait_response.return_value = {
                    "type": "grant",
                    "payload": {"turn_id": 9, "sequence": 1, "turn_input_schema": "RunTurn", "input": {"content": "resume"}, "replayed": True},
                }
                grant = client.resume_turn(9, timeout=0.1)
            self.assertEqual(grant["turn_id"], 9)
            self.assertTrue(grant["replayed"])
            self.assertIsNotNone(sent_request_id)
            wait_response.assert_called_once_with(sent_request_id, timeout=0.1)
            outbox = bridge.Queue(root, bridge.OUTBOX)
            messages = [envelope for _, _, envelope in outbox.read_all()]
            self.assertEqual(messages[-1]["type"], "resume_turn")
            self.assertEqual(messages[-1]["turn_id"], 9)

    def test_heartbeat_writes_bridge_mtime_file_and_message(self):
        with tempfile.TemporaryDirectory() as root:
            client = bridge.BridgeClient(root, "sess", "gen", "pi", poll_interval=0.001)
            client.heartbeat()

            heartbeat = Path(root) / bridge.HEARTBEAT / bridge.BRIDGE_HEARTBEAT
            self.assertTrue(heartbeat.exists())
            self.assertTrue(heartbeat.read_text(encoding="ascii").strip().isdigit())
            outbox = bridge.Queue(root, bridge.OUTBOX)
            messages = [envelope for _, _, envelope in outbox.read_all()]
            self.assertEqual(messages[-1]["type"], "heartbeat")

    def test_checkpoint_ready_marker_is_written_and_cleared(self):
        with tempfile.TemporaryDirectory() as root:
            client = bridge.BridgeClient(root, "sess", "gen", "pi", poll_interval=0.001)
            client.mark_checkpoint_ready()
            ready = Path(root) / bridge.HEARTBEAT / bridge.CHECKPOINT_READY
            self.assertTrue(ready.exists())
            self.assertTrue(ready.read_text(encoding="ascii").strip().isdigit())
            client.clear_checkpoint_ready()
            self.assertFalse(ready.exists())

    def test_sandbox_source_ip_ignores_manifest_env(self):
        env_name = "HARNESS_" + "SANDBOX_SOURCE_IP"
        with mock.patch.dict(os.environ, {env_name: "10.240.0.2"}, clear=False):
            with mock.patch.object(bridge.subprocess, "check_output") as check_output:
                check_output.return_value = json.dumps([
                    {"ifname": "lo", "addr_info": [{"family": "inet", "local": "127.0.0.1"}]},
                    {"ifname": "eth0", "addr_info": [{"family": "inet", "local": "10.241.0.2"}]},
                ])
                self.assertEqual(bridge.sandbox_source_ip(), "10.241.0.2")

        check_output.assert_called_once_with(["ip", "-j", "addr", "show"], text=True)

    def test_heartbeat_loop_uses_configured_interval(self):
        with tempfile.TemporaryDirectory() as root:
            args = argparse_namespace(
                bridge_dir=root,
                session_id="sess",
                generation_id="gen",
                driver_id="pi",
                poll_interval=0.001,
                interval=0.25,
            )
            calls = []

            def heartbeat_once(self):
                calls.append((self.session_id, self.generation_id))
                raise KeyboardInterrupt()

            with mock.patch.object(bridge.BridgeClient, "heartbeat", heartbeat_once):
                with self.assertRaises(KeyboardInterrupt):
                    bridge.run_heartbeat_loop(args)

            self.assertEqual(calls, [("sess", "gen")])

    def test_claim_loop_claims_and_records_lifecycle(self):
        with tempfile.TemporaryDirectory() as root:
            args = argparse_namespace(
                bridge_dir=root,
                session_id="sess",
                generation_id="gen",
                driver_id="sh",
                poll_interval=0.001,
                timeout=0.1,
                base_url="",
                healthz_statuses="200",
                http_timeout=0.1,
                heartbeat_interval=60,
                idle_interval=0.001,
                max_turns=1,
                max_empty_polls=0,
            )
            inbox = bridge.Queue(root, bridge.INBOX)
            original_write = bridge.Queue.write

            def write_and_respond(queue, envelope):
                message_type = envelope.get("type")
                request_id = envelope.get("request_id")
                if queue.name == bridge.OUTBOX and message_type in {"hello", "probe_network", "claim_next_turn"}:
                    response_type = "no_work"
                    payload = {}
                    if message_type == "hello":
                        response_type = "hello_ack"
                        payload = {"last_output_sequence_by_turn": {}}
                    elif message_type == "claim_next_turn":
                        response_type = "grant"
                        payload = {"turn_id": 9, "sequence": 1, "turn_input_schema": "RunTurn", "input": {"content": "echo ok"}}
                    inbox.write(
                        {
                            "message_id": f"host_{message_type}",
                            "request_id": request_id,
                            "type": response_type,
                            "session_id": "sess",
                            "generation_id": "gen",
                            "payload": payload,
                        }
                    )
                return original_write(queue, envelope)

            class FakeRunner:
                def run_turn(self, content, emit):
                    self.content = content
                    emit("stdout", '{"type":"harness.shell_output","text":"ok"}')
                    return "completed", "", ""

                def close(self):
                    self.closed = True

            runner = FakeRunner()
            with mock.patch.object(bridge.Queue, "write", write_and_respond):
                with mock.patch.object(bridge, "sandbox_source_ip", return_value="10.240.0.2"):
                    bridge.run_claim_loop(args, runner=runner)

            self.assertEqual(runner.content, "echo ok")
            outbox = bridge.Queue(root, bridge.OUTBOX)
            messages = [envelope for _, _, envelope in outbox.read_all()]
            types = [message["type"] for message in messages]
            self.assertIn("ack_turn_started", types)
            self.assertIn("emit_output", types)
            self.assertEqual(types[-1], "ack_turn_completed")
            output = next(message for message in messages if message["type"] == "emit_output")
            self.assertEqual(output["turn_id"], 9)
            self.assertEqual(output["payload"]["output_sequence"], 1)
            self.assertEqual(output["payload"]["payload"]["line"], '{"type":"harness.shell_output","text":"ok"}')
            ready = Path(root) / bridge.HEARTBEAT / bridge.CHECKPOINT_READY
            self.assertTrue(ready.exists())

    def test_execute_grant_rejects_non_run_turn_schema(self):
        with tempfile.TemporaryDirectory() as root:
            client = bridge.BridgeClient(root, "sess", "gen", "sh", poll_interval=0.001)

            class FakeRunner:
                def run_turn(self, content, emit):
                    raise AssertionError("runner should not execute invalid schema")

            with self.assertRaisesRegex(RuntimeError, "unsupported turn_input_schema legacy"):
                bridge.execute_grant(
                    client,
                    FakeRunner(),
                    {"turn_id": 9, "sequence": 1, "turn_input_schema": "legacy", "input": {"content": "run"}},
                )

    def test_native_events_probe_runner_emits_schema_tagged_payload(self):
        with tempfile.TemporaryDirectory() as root:
            client = bridge.BridgeClient(root, "sess", "gen", "native_events_probe", poll_interval=0.001)
            runner = bridge.make_turn_runner("native_events_probe")
            with mock.patch.object(bridge, "sandbox_source_ip", return_value="10.240.0.2"):
                bridge.execute_grant(
                    client,
                    runner,
                    {"turn_id": 9, "sequence": 1, "turn_input_schema": "RunTurn", "input": {"content": "native ok"}},
                )

            messages = [envelope for _, _, envelope in bridge.Queue(root, bridge.OUTBOX).read_all()]
            output = next(message for message in messages if message["type"] == "emit_output")
            self.assertEqual(output["payload"]["output_sequence"], 1)
            self.assertEqual(
                output["payload"]["payload"],
                {
                    "schema": "harness_native_events_v1",
                    "event": {"type": "agent.message", "payload": {"content": "native ok"}},
                },
            )
            self.assertEqual(messages[-1]["type"], "ack_turn_completed")

    def test_execute_grant_omits_driver_state_update_for_failed_turns(self):
        with tempfile.TemporaryDirectory() as root:
            client = bridge.BridgeClient(root, "sess", "gen", "pi", poll_interval=0.001)

            class FailedRunner:
                def run_turn(self, content, emit):
                    return "failed", "agent_execution_failed", "boom", {"driver_id": "pi"}

            with mock.patch.object(bridge, "sandbox_source_ip", return_value="10.240.0.2"):
                bridge.execute_grant(
                    client,
                    FailedRunner(),
                    {"turn_id": 9, "sequence": 1, "turn_input_schema": "RunTurn", "input": {"content": "run"}},
                )

            messages = [envelope for _, _, envelope in bridge.Queue(root, bridge.OUTBOX).read_all()]
            completion = messages[-1]
            self.assertEqual(completion["type"], "ack_turn_completed")
            self.assertEqual(completion["payload"]["status"], "failed")
            self.assertNotIn("driver_state_update", completion["payload"])

    def test_pi_rpc_runner_uses_session_rpc_and_returns_driver_state_update(self):
        class FakeStdin:
            def __init__(self):
                self.writes = []

            def write(self, value):
                self.writes.append(value)

            def flush(self):
                pass

        class FakeStdout:
            def __init__(self, lines):
                self.lines = list(lines)

            def readline(self):
                if not self.lines:
                    return ""
                return self.lines.pop(0)

        class FakeProcess:
            def __init__(self, lines):
                self.stdin = FakeStdin()
                self.stdout = FakeStdout(lines)
                self.stderr = iter([])

            def poll(self):
                return None

            def terminate(self):
                pass

        with tempfile.TemporaryDirectory() as root:
            agent_dir = Path(root) / "agent"
            session_dir = agent_dir / "sessions"
            session_dir.mkdir(parents=True)
            session_file = session_dir / "session-1.jsonl"
            session_file.write_text("{}\n", encoding="utf-8")
            models = agent_dir / "models.json"
            settings = agent_dir / "settings.json"
            for path in (models, settings):
                path.write_text("{}\n", encoding="utf-8")
                path.chmod(0o444)

            lines = [
                '{"id":"harness-turn-9","type":"response","command":"prompt","success":true}\n',
                '{"type":"message_update","messageId":"msg-1","assistantMessageEvent":{"type":"text_delta","delta":"ok"}}\n',
                '{"type":"turn_end","status":"completed"}\n',
                json.dumps(
                    {
                        "id": "harness-turn-9-stats",
                        "type": "response",
                        "command": "get_session_stats",
                        "success": True,
                        "data": {"sessionFile": str(session_file), "sessionId": "pi-session-1"},
                    },
                    separators=(",", ":"),
                )
                + "\n",
            ]
            captured = {}

            def popen(command, **kwargs):
                captured["command"] = command
                captured["env"] = kwargs.get("env") or {}
                captured["process"] = FakeProcess(lines)
                return captured["process"]

            patches = [
                mock.patch.object(bridge, "PI_CODING_AGENT_DIR", str(agent_dir)),
                mock.patch.object(bridge, "PI_SESSION_DIR", str(session_dir)),
                mock.patch.object(bridge, "PI_MODELS_SANDBOX_PATH", str(models)),
                mock.patch.object(bridge, "PI_SETTINGS_SANDBOX_PATH", str(settings)),
                mock.patch.object(bridge.subprocess, "Popen", side_effect=popen),
                mock.patch.dict(
                    os.environ,
                    {
                        "PI_CODING_AGENT_DIR": str(agent_dir),
                        "PI_CODING_AGENT_SESSION_DIR": str(session_dir),
                        "PI_OFFLINE": "1",
                        "PI_SKIP_VERSION_CHECK": "1",
                        "PI_TELEMETRY": "0",
                        "PI_MODEL": "sonnet",
                        "HARNESS_AGENT_UID": str(os.geteuid()),
                        "HARNESS_AGENT_GID": str(os.getegid()),
                    },
                    clear=True,
                ),
            ]
            with patches[0], patches[1], patches[2], patches[3], patches[4], patches[5]:
                runner = bridge.PiRPCTurnRunner()
                runner.set_turn_context(
                    {
                        "turn_id": 9,
                        "driver_state": {
                            "driver_id": "pi",
                            "state_digest": "sha256:" + "a" * 64,
                            "state_version": 1,
                            "state_payload": {
                                "schema_version": 1,
                                "driver_id": "pi",
                                "state_kind": "pi_uninitialized",
                                "session_dir": str(session_dir),
                            },
                        },
                    }
                )
                emitted = []
                status, error_class, error, update = runner.run_turn("hello", lambda stream, line: emitted.append((stream, line)))

            self.assertEqual((status, error_class, error), ("completed", "", ""))
            self.assertEqual(captured["command"][:7], ["/usr/local/bin/pi", "--mode", "rpc", "--provider", "harness_anthropic_proxy", "--model", "sonnet"])
            self.assertIn("--session-dir", captured["command"])
            self.assertEqual(captured["command"][captured["command"].index("--session-dir") + 1], str(session_dir))
            self.assertNotIn("--no-session", captured["command"])
            self.assertEqual(captured["env"]["PI_CODING_AGENT_DIR"], str(agent_dir))
            self.assertEqual(captured["env"]["PI_CODING_AGENT_SESSION_DIR"], str(session_dir))
            self.assertEqual(captured["env"]["PI_OFFLINE"], "1")
            self.assertEqual(captured["env"]["PI_TELEMETRY"], "0")
            writes = [json.loads(value) for value in captured["process"].stdin.writes]
            self.assertEqual(writes[0], {"id": "harness-turn-9", "type": "prompt", "message": "hello"})
            self.assertEqual(writes[1], {"id": "harness-turn-9-stats", "type": "get_session_stats"})
            self.assertEqual([json.loads(line)["type"] for _, line in emitted], ["response", "message_update", "turn_end"])
            self.assertEqual(update["driver_id"], "pi")
            self.assertEqual(update["previous_state_digest"], "sha256:" + "a" * 64)
            self.assertEqual(update["state_version"], 2)
            self.assertEqual(update["state_payload"]["selected_session_relpath"], "session-1.jsonl")
            self.assertEqual(update["state_payload"]["selected_session_id"], "pi-session-1")
            self.assertTrue(update["state_digest"].startswith("sha256:"))

    def test_pi_rpc_runner_passes_system_prompt_as_cli_append(self):
        class FakeStdin:
            def __init__(self):
                self.writes = []

            def write(self, value):
                self.writes.append(value)

            def flush(self):
                pass

        class FakeStdout:
            def __init__(self, lines):
                self.lines = list(lines)

            def readline(self):
                if not self.lines:
                    return ""
                return self.lines.pop(0)

        class FakeProcess:
            def __init__(self, lines):
                self.stdin = FakeStdin()
                self.stdout = FakeStdout(lines)
                self.stderr = iter([])

            def poll(self):
                return None

            def terminate(self):
                pass

        with tempfile.TemporaryDirectory() as root:
            agent_dir = Path(root) / "agent"
            session_dir = agent_dir / "sessions"
            session_dir.mkdir(parents=True)
            session_file = session_dir / "session-1.jsonl"
            session_file.write_text("{}\n", encoding="utf-8")
            models = agent_dir / "models.json"
            settings = agent_dir / "settings.json"
            system_prompt = Path(root) / "pi-system-prompt.md"
            system_prompt.write_text("You are BatteryGPT.\n\nUse Doris safely.\n", encoding="utf-8")
            for path in (models, settings):
                path.write_text("{}\n", encoding="utf-8")
            for path in (models, settings, system_prompt):
                path.chmod(0o444)

            lines = [
                '{"id":"harness-turn-9","type":"response","command":"prompt","success":true}\n',
                '{"type":"turn_end","status":"completed"}\n',
                json.dumps(
                    {
                        "id": "harness-turn-9-stats",
                        "type": "response",
                        "command": "get_session_stats",
                        "success": True,
                        "data": {"sessionFile": str(session_file), "sessionId": "pi-session-1"},
                    },
                    separators=(",", ":"),
                )
                + "\n",
            ]
            captured = {}

            def popen(command, **kwargs):
                captured["command"] = command
                captured["process"] = FakeProcess(lines)
                return captured["process"]

            patches = [
                mock.patch.object(bridge, "PI_CODING_AGENT_DIR", str(agent_dir)),
                mock.patch.object(bridge, "PI_SESSION_DIR", str(session_dir)),
                mock.patch.object(bridge, "PI_MODELS_SANDBOX_PATH", str(models)),
                mock.patch.object(bridge, "PI_SETTINGS_SANDBOX_PATH", str(settings)),
                mock.patch.object(bridge, "PI_SYSTEM_PROMPT_PATH", str(system_prompt)),
                mock.patch.object(bridge.subprocess, "Popen", side_effect=popen),
                mock.patch.dict(
                    os.environ,
                    {
                        "PI_CODING_AGENT_DIR": str(agent_dir),
                        "PI_CODING_AGENT_SESSION_DIR": str(session_dir),
                        "PI_OFFLINE": "1",
                        "PI_SKIP_VERSION_CHECK": "1",
                        "PI_TELEMETRY": "0",
                        "PI_MODEL": "sonnet",
                        "HARNESS_AGENT_UID": str(os.geteuid()),
                        "HARNESS_AGENT_GID": str(os.getegid()),
                    },
                    clear=True,
                ),
            ]
            with patches[0], patches[1], patches[2], patches[3], patches[4], patches[5], patches[6]:
                runner = bridge.PiRPCTurnRunner()
                runner.set_turn_context(
                    {
                        "turn_id": 9,
                        "driver_state": {
                            "driver_id": "pi",
                            "state_digest": "sha256:" + "a" * 64,
                            "state_version": 1,
                            "state_payload": {"schema_version": 1, "driver_id": "pi", "state_kind": "pi_uninitialized"},
                        },
                    }
                )
                runner.run_turn("hello", lambda stream, line: None)

            append_index = captured["command"].index("--append-system-prompt")
            self.assertEqual(captured["command"][append_index + 1], "You are BatteryGPT.\n\nUse Doris safely.")
            writes = [json.loads(value) for value in captured["process"].stdin.writes]
            self.assertEqual(writes[0], {"id": "harness-turn-9", "type": "prompt", "message": "hello"})

    def test_pi_rpc_runner_keeps_stderr_out_of_json_rpc_stream(self):
        class FakeStdin:
            def __init__(self):
                self.writes = []

            def write(self, value):
                self.writes.append(value)

            def flush(self):
                pass

        class FakeStdout:
            def __init__(self, lines):
                self.lines = list(lines)

            def readline(self):
                if not self.lines:
                    return ""
                return self.lines.pop(0)

        class FakeProcess:
            def __init__(self, stdout_lines):
                self.stdin = FakeStdin()
                self.stdout = FakeStdout(stdout_lines)
                self.stderr = iter(["Warning: startup settings lock failed\n"])

            def poll(self):
                return None

            def terminate(self):
                pass

        with tempfile.TemporaryDirectory() as root:
            agent_dir = Path(root) / "agent"
            session_dir = agent_dir / "sessions"
            session_dir.mkdir(parents=True)
            session_file = session_dir / "session-1.jsonl"
            session_file.write_text("{}\n", encoding="utf-8")
            models = agent_dir / "models.json"
            settings = agent_dir / "settings.json"
            for path in (models, settings):
                path.write_text("{}\n", encoding="utf-8")
                path.chmod(0o444)

            lines = [
                '{"id":"harness-turn-9","type":"response","command":"prompt","success":true}\n',
                '{"type":"turn_end","status":"completed"}\n',
                json.dumps(
                    {
                        "id": "harness-turn-9-stats",
                        "type": "response",
                        "command": "get_session_stats",
                        "success": True,
                        "data": {"sessionFile": str(session_file), "sessionId": "pi-session-1"},
                    },
                    separators=(",", ":"),
                )
                + "\n",
            ]
            captured = {}

            def popen(command, **kwargs):
                captured["stderr"] = kwargs.get("stderr")
                captured["process"] = FakeProcess(lines)
                return captured["process"]

            patches = [
                mock.patch.object(bridge, "PI_CODING_AGENT_DIR", str(agent_dir)),
                mock.patch.object(bridge, "PI_SESSION_DIR", str(session_dir)),
                mock.patch.object(bridge, "PI_MODELS_SANDBOX_PATH", str(models)),
                mock.patch.object(bridge, "PI_SETTINGS_SANDBOX_PATH", str(settings)),
                mock.patch.object(bridge.subprocess, "Popen", side_effect=popen),
                mock.patch.object(bridge.threading.Thread, "start", lambda self: None),
                mock.patch.dict(
                    os.environ,
                    {
                        "PI_CODING_AGENT_DIR": str(agent_dir),
                        "PI_CODING_AGENT_SESSION_DIR": str(session_dir),
                        "PI_OFFLINE": "1",
                        "PI_SKIP_VERSION_CHECK": "1",
                        "PI_TELEMETRY": "0",
                        "PI_MODEL": "sonnet",
                        "HARNESS_AGENT_UID": str(os.geteuid()),
                        "HARNESS_AGENT_GID": str(os.getegid()),
                    },
                    clear=True,
                ),
            ]
            with patches[0], patches[1], patches[2], patches[3], patches[4], patches[5], patches[6]:
                runner = bridge.PiRPCTurnRunner()
                runner.set_turn_context(
                    {
                        "turn_id": 9,
                        "driver_state": {
                            "driver_id": "pi",
                            "state_digest": "sha256:" + "a" * 64,
                            "state_version": 1,
                            "state_payload": {"schema_version": 1, "driver_id": "pi", "state_kind": "pi_uninitialized"},
                        },
                    }
                )
                status, error_class, error, update = runner.run_turn("hello", lambda stream, line: None)

            self.assertEqual((status, error_class, error), ("completed", "", ""))
            self.assertEqual(captured["stderr"], bridge.subprocess.PIPE)
            self.assertEqual(update["state_payload"]["selected_session_id"], "pi-session-1")

    def test_pi_rpc_runner_reports_recent_stderr_when_process_exits(self):
        class FakeStdin:
            def write(self, value):
                pass

            def flush(self):
                pass

        class FakeStdout:
            def readline(self):
                return ""

        class FakeProcess:
            def __init__(self):
                self.stdin = FakeStdin()
                self.stdout = FakeStdout()
                self.stderr = iter([])

            def poll(self):
                return 1

            def terminate(self):
                pass

        with tempfile.TemporaryDirectory() as root:
            agent_dir = Path(root) / "agent"
            session_dir = agent_dir / "sessions"
            session_dir.mkdir(parents=True)
            models = agent_dir / "models.json"
            settings = agent_dir / "settings.json"
            for path in (models, settings):
                path.write_text("{}\n", encoding="utf-8")
                path.chmod(0o444)

            patches = [
                mock.patch.object(bridge, "PI_CODING_AGENT_DIR", str(agent_dir)),
                mock.patch.object(bridge, "PI_SESSION_DIR", str(session_dir)),
                mock.patch.object(bridge, "PI_MODELS_SANDBOX_PATH", str(models)),
                mock.patch.object(bridge, "PI_SETTINGS_SANDBOX_PATH", str(settings)),
                mock.patch.object(bridge.subprocess, "Popen", return_value=FakeProcess()),
                mock.patch.object(bridge.threading.Thread, "start", lambda self: None),
                mock.patch.dict(
                    os.environ,
                    {
                        "PI_CODING_AGENT_DIR": str(agent_dir),
                        "PI_CODING_AGENT_SESSION_DIR": str(session_dir),
                        "PI_OFFLINE": "1",
                        "PI_SKIP_VERSION_CHECK": "1",
                        "PI_TELEMETRY": "0",
                        "PI_MODEL": "sonnet",
                        "HARNESS_AGENT_UID": str(os.geteuid()),
                        "HARNESS_AGENT_GID": str(os.getegid()),
                    },
                    clear=True,
                ),
            ]
            with patches[0], patches[1], patches[2], patches[3], patches[4], patches[5], patches[6]:
                runner = bridge.PiRPCTurnRunner()
                runner.set_turn_context(
                    {
                        "turn_id": 9,
                        "driver_state": {
                            "driver_id": "pi",
                            "state_digest": "sha256:" + "a" * 64,
                            "state_version": 1,
                            "state_payload": {"schema_version": 1, "driver_id": "pi", "state_kind": "pi_uninitialized"},
                        },
                    }
                )
                runner.stderr_tail.append('Error: Unknown provider "harness_anthropic_proxy"')

                with self.assertRaisesRegex(RuntimeError, 'pi rpc process exited with status 1: Error: Unknown provider "harness_anthropic_proxy"'):
                    runner.run_turn("hello", lambda stream, line: None)

    def test_pi_rpc_runner_restores_sidecar_session_before_prompt(self):
        class FakeStdin:
            def __init__(self):
                self.writes = []

            def write(self, value):
                self.writes.append(value)

            def flush(self):
                pass

        class FakeStdout:
            def __init__(self, lines):
                self.lines = list(lines)

            def readline(self):
                if not self.lines:
                    return ""
                return self.lines.pop(0)

        class FakeProcess:
            def __init__(self, lines):
                self.stdin = FakeStdin()
                self.stdout = FakeStdout(lines)
                self.stderr = iter([])

            def poll(self):
                return None

            def terminate(self):
                pass

        with tempfile.TemporaryDirectory() as root:
            agent_dir = Path(root) / "agent"
            session_dir = agent_dir / "sessions"
            session_dir.mkdir(parents=True)
            session_file = session_dir / "session-1.jsonl"
            session_file.write_text("{}\n", encoding="utf-8")
            models = agent_dir / "models.json"
            settings = agent_dir / "settings.json"
            for path in (models, settings):
                path.write_text("{}\n", encoding="utf-8")
                path.chmod(0o444)

            lines = [
                '{"id":"harness-restore-10","type":"response","command":"switch_session","success":true}\n',
                json.dumps(
                    {
                        "id": "harness-restore-10-stats",
                        "type": "response",
                        "command": "get_session_stats",
                        "success": True,
                        "data": {"sessionFile": str(session_file), "sessionId": "pi-session-1"},
                    },
                    separators=(",", ":"),
                )
                + "\n",
                '{"id":"harness-turn-10","type":"response","command":"prompt","success":true}\n',
                '{"type":"turn_end","status":"completed"}\n',
                json.dumps(
                    {
                        "id": "harness-turn-10-stats",
                        "type": "response",
                        "command": "get_session_stats",
                        "success": True,
                        "data": {"sessionFile": str(session_file), "sessionId": "pi-session-1"},
                    },
                    separators=(",", ":"),
                )
                + "\n",
            ]
            captured = {}

            def popen(command, **kwargs):
                captured["process"] = FakeProcess(lines)
                return captured["process"]

            patches = [
                mock.patch.object(bridge, "PI_CODING_AGENT_DIR", str(agent_dir)),
                mock.patch.object(bridge, "PI_SESSION_DIR", str(session_dir)),
                mock.patch.object(bridge, "PI_MODELS_SANDBOX_PATH", str(models)),
                mock.patch.object(bridge, "PI_SETTINGS_SANDBOX_PATH", str(settings)),
                mock.patch.object(bridge.subprocess, "Popen", side_effect=popen),
                mock.patch.dict(
                    os.environ,
                    {
                        "PI_CODING_AGENT_DIR": str(agent_dir),
                        "PI_CODING_AGENT_SESSION_DIR": str(session_dir),
                        "PI_OFFLINE": "1",
                        "PI_SKIP_VERSION_CHECK": "1",
                        "PI_TELEMETRY": "0",
                        "PI_MODEL": "sonnet",
                        "HARNESS_AGENT_UID": str(os.geteuid()),
                        "HARNESS_AGENT_GID": str(os.getegid()),
                    },
                    clear=True,
                ),
            ]
            with patches[0], patches[1], patches[2], patches[3], patches[4], patches[5]:
                runner = bridge.PiRPCTurnRunner()
                runner.set_turn_context(
                    {
                        "turn_id": 10,
                        "driver_state": {
                            "driver_id": "pi",
                            "state_digest": "sha256:" + "b" * 64,
                            "state_version": 2,
                            "state_payload": {
                                "schema_version": 1,
                                "driver_id": "pi",
                                "state_kind": "pi_session",
                                "session_dir": str(session_dir),
                                "selected_session_relpath": "session-1.jsonl",
                                "selected_session_file": str(session_file),
                                "selected_session_id": "pi-session-1",
                                "last_completed_turn_id": "9",
                            },
                        },
                    }
                )
                status, error_class, error, update = runner.run_turn("restored", lambda stream, line: None)

            self.assertEqual((status, error_class, error), ("completed", "", ""))
            writes = [json.loads(value) for value in captured["process"].stdin.writes]
            self.assertEqual([write["type"] for write in writes], ["switch_session", "get_session_stats", "prompt", "get_session_stats"])
            self.assertEqual(writes[0]["sessionPath"], str(session_file))
            self.assertNotIn("sessionFile", writes[0])
            self.assertEqual(writes[2], {"id": "harness-turn-10", "type": "prompt", "message": "restored"})
            self.assertEqual(update["previous_state_digest"], "sha256:" + "b" * 64)
            self.assertEqual(update["state_version"], 3)
            self.assertEqual(update["state_payload"]["selected_session_id"], "pi-session-1")

    def test_pi_rpc_runner_reports_failed_command_detail(self):
        class FakeStdin:
            def __init__(self):
                self.writes = []

            def write(self, value):
                self.writes.append(value)

            def flush(self):
                pass

        class FakeStdout:
            def __init__(self, lines):
                self.lines = list(lines)

            def readline(self):
                if not self.lines:
                    return ""
                return self.lines.pop(0)

        class FakeProcess:
            def __init__(self, lines):
                self.stdin = FakeStdin()
                self.stdout = FakeStdout(lines)
                self.stderr = iter([])

            def poll(self):
                return None

            def terminate(self):
                pass

        with tempfile.TemporaryDirectory() as root:
            agent_dir = Path(root) / "agent"
            session_dir = agent_dir / "sessions"
            session_dir.mkdir(parents=True)
            session_file = session_dir / "session-1.jsonl"
            session_file.write_text("{}\n", encoding="utf-8")
            models = agent_dir / "models.json"
            settings = agent_dir / "settings.json"
            for path in (models, settings):
                path.write_text("{}\n", encoding="utf-8")
                path.chmod(0o444)

            lines = ['{"id":"harness-restore-10","type":"response","command":"switch_session","success":false,"error":"sessionPath is required"}\n']

            def popen(command, **kwargs):
                return FakeProcess(lines)

            patches = [
                mock.patch.object(bridge, "PI_CODING_AGENT_DIR", str(agent_dir)),
                mock.patch.object(bridge, "PI_SESSION_DIR", str(session_dir)),
                mock.patch.object(bridge, "PI_MODELS_SANDBOX_PATH", str(models)),
                mock.patch.object(bridge, "PI_SETTINGS_SANDBOX_PATH", str(settings)),
                mock.patch.object(bridge.subprocess, "Popen", side_effect=popen),
                mock.patch.dict(
                    os.environ,
                    {
                        "PI_CODING_AGENT_DIR": str(agent_dir),
                        "PI_CODING_AGENT_SESSION_DIR": str(session_dir),
                        "PI_OFFLINE": "1",
                        "PI_SKIP_VERSION_CHECK": "1",
                        "PI_TELEMETRY": "0",
                        "PI_MODEL": "sonnet",
                        "HARNESS_AGENT_UID": str(os.geteuid()),
                        "HARNESS_AGENT_GID": str(os.getegid()),
                    },
                    clear=True,
                ),
            ]
            with patches[0], patches[1], patches[2], patches[3], patches[4], patches[5]:
                runner = bridge.PiRPCTurnRunner()
                runner.set_turn_context(
                    {
                        "turn_id": 10,
                        "driver_state": {
                            "driver_id": "pi",
                            "state_digest": "sha256:" + "b" * 64,
                            "state_version": 2,
                            "state_payload": {
                                "schema_version": 1,
                                "driver_id": "pi",
                                "state_kind": "pi_session",
                                "session_dir": str(session_dir),
                                "selected_session_relpath": "session-1.jsonl",
                                "selected_session_file": str(session_file),
                                "selected_session_id": "pi-session-1",
                                "last_completed_turn_id": "9",
                            },
                        },
                    }
                )
                with self.assertRaisesRegex(RuntimeError, "pi rpc command switch_session failed: sessionPath is required"):
                    runner.run_turn("restored", lambda stream, line: None)

    def test_pi_rpc_runner_requires_restore_payload(self):
        runner = bridge.PiRPCTurnRunner()
        runner.set_turn_context({"driver_state": {"state_digest": "sha256:" + "c" * 64}})
        with self.assertRaisesRegex(RuntimeError, "state_payload is required"):
            runner._restore_session_if_needed(11)

    def test_validate_pi_runtime_environment_returns_projected_env(self):
        with tempfile.TemporaryDirectory() as root:
            agent_dir = Path(root) / "agent"
            session_dir = agent_dir / "sessions"
            with mock.patch.object(bridge, "PI_CODING_AGENT_DIR", str(agent_dir)):
                with mock.patch.object(bridge, "PI_SESSION_DIR", str(session_dir)):
                    with mock.patch.dict(
                        os.environ,
                        {
                            "PI_CODING_AGENT_DIR": str(agent_dir),
                            "PI_CODING_AGENT_SESSION_DIR": str(session_dir),
                            "PI_OFFLINE": "1",
                            "PI_SKIP_VERSION_CHECK": "1",
                            "PI_TELEMETRY": "0",
                        },
                        clear=True,
                    ):
                        self.assertEqual(
                            bridge.validate_pi_runtime_environment(),
                            {
                                "PI_CODING_AGENT_DIR": str(agent_dir),
                                "PI_CODING_AGENT_SESSION_DIR": str(session_dir),
                                "PI_OFFLINE": "1",
                                "PI_SKIP_VERSION_CHECK": "1",
                                "PI_TELEMETRY": "0",
                            },
                        )

    def test_normalize_pi_session_file_rejects_symlink_session_root(self):
        with tempfile.TemporaryDirectory() as root:
            agent_dir = Path(root) / "agent"
            agent_dir.mkdir()
            real_sessions = Path(root) / "real-sessions"
            real_sessions.mkdir()
            session_link = agent_dir / "sessions"
            session_link.symlink_to(real_sessions, target_is_directory=True)
            with mock.patch.object(bridge, "PI_SESSION_DIR", str(session_link)):
                with self.assertRaisesRegex(RuntimeError, "session dir must not use sandbox symlink"):
                    bridge.normalize_pi_session_file("session-1.jsonl")

    def test_pi_config_materialization_rejects_missing_config(self):
        with tempfile.TemporaryDirectory() as root:
            agent_dir = Path(root) / "agent"
            agent_dir.mkdir(parents=True)
            settings = agent_dir / "settings.json"
            settings.write_text("{}\n", encoding="utf-8")
            settings.chmod(0o444)
            with mock.patch.object(bridge, "PI_MODELS_SANDBOX_PATH", str(agent_dir / "models.json")):
                with mock.patch.object(bridge, "PI_SETTINGS_SANDBOX_PATH", str(settings)):
                    with self.assertRaisesRegex(RuntimeError, "pi config missing"):
                        bridge.validate_pi_config_materialization()

    def test_pi_config_materialization_rejects_symlink_config_path(self):
        with tempfile.TemporaryDirectory() as root:
            agent_dir = Path(root) / "agent"
            agent_dir.mkdir(parents=True)
            real_models = Path(root) / "models.json"
            real_models.write_text("{}\n", encoding="utf-8")
            real_models.chmod(0o444)
            models = agent_dir / "models.json"
            models.symlink_to(real_models)
            settings = agent_dir / "settings.json"
            settings.write_text("{}\n", encoding="utf-8")
            settings.chmod(0o444)
            with mock.patch.object(bridge, "PI_MODELS_SANDBOX_PATH", str(models)):
                with mock.patch.object(bridge, "PI_SETTINGS_SANDBOX_PATH", str(settings)):
                    with self.assertRaisesRegex(RuntimeError, "pi config must not use sandbox symlink"):
                        bridge.validate_pi_config_materialization()

    def test_pi_rpc_runner_rejects_writable_config(self):
        with tempfile.TemporaryDirectory() as root:
            agent_dir = Path(root) / "agent"
            session_dir = agent_dir / "sessions"
            session_dir.mkdir(parents=True)
            models = agent_dir / "models.json"
            settings = agent_dir / "settings.json"
            models.write_text("{}\n", encoding="utf-8")
            settings.write_text("{}\n", encoding="utf-8")
            models.chmod(0o644)
            settings.chmod(0o444)
            with mock.patch.object(bridge, "PI_MODELS_SANDBOX_PATH", str(models)):
                with mock.patch.object(bridge, "PI_SETTINGS_SANDBOX_PATH", str(settings)):
                    with mock.patch.dict(os.environ, {"HARNESS_AGENT_UID": str(os.geteuid()), "HARNESS_AGENT_GID": str(os.getegid())}, clear=False):
                        with self.assertRaisesRegex(RuntimeError, "writable by sandbox uid/gid"):
                            bridge.validate_pi_config_materialization()

    def test_claim_loop_handles_multiple_turns(self):
        with tempfile.TemporaryDirectory() as root:
            args = argparse_namespace(
                bridge_dir=root,
                session_id="sess",
                generation_id="gen",
                driver_id="sh",
                poll_interval=0.001,
                timeout=0.1,
                base_url="",
                healthz_statuses="200",
                http_timeout=0.1,
                heartbeat_interval=60,
                idle_interval=0.001,
                max_turns=2,
                max_empty_polls=0,
            )
            grants = [
                {"turn_id": 9, "sequence": 1, "turn_input_schema": "RunTurn", "input": {"content": "first"}},
                {"turn_id": 10, "sequence": 2, "turn_input_schema": "RunTurn", "input": {"content": "second"}},
            ]
            inbox = bridge.Queue(root, bridge.INBOX)
            original_write = bridge.Queue.write

            def write_and_respond(queue, envelope):
                message_type = envelope.get("type")
                request_id = envelope.get("request_id")
                if queue.name == bridge.OUTBOX and message_type in {"hello", "probe_network", "claim_next_turn"}:
                    response_type = "no_work"
                    payload = {}
                    if message_type == "hello":
                        response_type = "hello_ack"
                        payload = {"last_output_sequence_by_turn": {}}
                    elif message_type == "claim_next_turn" and grants:
                        response_type = "grant"
                        payload = grants.pop(0)
                    inbox.write(
                        {
                            "message_id": f"host_{message_type}_{request_id}",
                            "request_id": request_id,
                            "type": response_type,
                            "session_id": "sess",
                            "generation_id": "gen",
                            "payload": payload,
                        }
                    )
                return original_write(queue, envelope)

            class FakeRunner:
                def __init__(self):
                    self.contents = []

                def run_turn(self, content, emit):
                    self.contents.append(content)
                    emit("stdout", content + " output")
                    return "completed", "", ""

                def close(self):
                    pass

            runner = FakeRunner()
            with mock.patch.object(bridge.Queue, "write", write_and_respond):
                with mock.patch.object(bridge, "sandbox_source_ip", return_value="10.240.0.2"):
                    bridge.run_claim_loop(args, runner=runner)

            self.assertEqual(runner.contents, ["first", "second"])
            messages = [envelope for _, _, envelope in bridge.Queue(root, bridge.OUTBOX).read_all()]
            self.assertEqual([message["type"] for message in messages].count("claim_next_turn"), 2)
            self.assertEqual([message["turn_id"] for message in messages if message["type"] == "ack_turn_started"], [9, 10])
            self.assertEqual([message["turn_id"] for message in messages if message["type"] == "ack_turn_completed"], [9, 10])
            outputs = [message for message in messages if message["type"] == "emit_output"]
            self.assertEqual([message["turn_id"] for message in outputs], [9, 10])
            self.assertEqual([message["payload"]["output_sequence"] for message in outputs], [1, 1])

    def test_claim_loop_resumes_leased_turn_from_hello_ack(self):
        with tempfile.TemporaryDirectory() as root:
            args = argparse_namespace(
                bridge_dir=root,
                session_id="sess",
                generation_id="gen",
                driver_id="sh",
                poll_interval=0.001,
                timeout=0.1,
                base_url="",
                healthz_statuses="200",
                http_timeout=0.1,
                heartbeat_interval=60,
                idle_interval=0.001,
                max_turns=1,
                max_empty_polls=0,
            )
            inbox = bridge.Queue(root, bridge.INBOX)
            original_write = bridge.Queue.write

            def write_and_respond(queue, envelope):
                message_type = envelope.get("type")
                request_id = envelope.get("request_id")
                if queue.name == bridge.OUTBOX and message_type in {"hello", "probe_network", "resume_turn"}:
                    response_type = "no_work"
                    payload = {}
                    if message_type == "hello":
                        response_type = "hello_ack"
                        payload = {"last_output_sequence_by_turn": {"9": 3}, "leased_turn_id": 9}
                    elif message_type == "resume_turn":
                        response_type = "grant"
                        payload = {"turn_id": 9, "sequence": 1, "turn_input_schema": "RunTurn", "input": {"content": "resume"}}
                    inbox.write(
                        {
                            "message_id": f"host_{message_type}",
                            "request_id": request_id,
                            "type": response_type,
                            "session_id": "sess",
                            "generation_id": "gen",
                            "turn_id": envelope.get("turn_id"),
                            "payload": payload,
                        }
                    )
                return original_write(queue, envelope)

            class FakeRunner:
                def run_turn(self, content, emit):
                    emit("stdout", "resumed")
                    return "completed", "", ""

                def close(self):
                    pass

            with mock.patch.object(bridge.Queue, "write", write_and_respond):
                bridge.run_claim_loop(args, runner=FakeRunner())

            outbox = bridge.Queue(root, bridge.OUTBOX)
            messages = [envelope for _, _, envelope in outbox.read_all()]
            self.assertIn("resume_turn", [message["type"] for message in messages])
            output = next(message for message in messages if message["type"] == "emit_output")
            self.assertEqual(output["payload"]["output_sequence"], 4)

    def test_network_probe_checks_health_only(self):
        with mock.patch.object(bridge, "http_status", return_value=200) as http_status:
            bridge.run_network_probe("http://10.240.0.1:8082", {200}, 0.1)

        http_status.assert_called_once_with("http://10.240.0.1:8082/healthz", timeout=0.1)

    def test_network_probe_rejects_unaccepted_statuses(self):
        with mock.patch.object(bridge, "http_status", return_value=204):
            with self.assertRaisesRegex(RuntimeError, r"probe GET /healthz returned 204"):
                bridge.run_network_probe("http://10.240.0.1:8082", {200}, 0.1)

    def test_pi_probe_runs_proxy_http_probe(self):
        with tempfile.TemporaryDirectory() as root:
            args = argparse_namespace(
                bridge_dir=root,
                session_id="sess",
                generation_id="gen",
                driver_id="pi",
                poll_interval=0.001,
                timeout=0.1,
                base_url="http://10.240.0.1:8082",
                healthz_statuses="200",
                http_timeout=0.1,
            )
            inbox = bridge.Queue(root, bridge.INBOX)

            with mock.patch.object(bridge, "run_network_probe") as run_network_probe:
                original_write = bridge.Queue.write

                def write_and_respond(queue, envelope):
                    message_type = envelope.get("type")
                    request_id = envelope.get("request_id")
                    if queue.name == bridge.OUTBOX and message_type in {"hello", "probe_network"}:
                        inbox.write(
                            {
                                "message_id": f"host_{message_type}",
                                "request_id": request_id,
                                "type": "hello_ack" if message_type == "hello" else "no_work",
                                "session_id": "sess",
                                "generation_id": "gen",
                                "payload": {"last_output_sequence_by_turn": {}} if message_type == "hello" else {},
                            }
                        )
                    return original_write(queue, envelope)

                with mock.patch.object(bridge.Queue, "write", write_and_respond):
                    bridge.run_probe(args)

            run_network_probe.assert_called_once_with("http://10.240.0.1:8082", {200}, 0.1)

    def test_shell_probe_skips_proxy_http_probe(self):
        with tempfile.TemporaryDirectory() as root:
            args = argparse_namespace(
                bridge_dir=root,
                session_id="sess",
                generation_id="gen",
                driver_id="sh",
                poll_interval=0.001,
                timeout=0.1,
                base_url="",
                healthz_statuses="200",
                http_timeout=0.1,
            )
            inbox = bridge.Queue(root, bridge.INBOX)

            with mock.patch.object(bridge, "run_network_probe") as run_network_probe:
                sent = []
                original_write = bridge.Queue.write

                def write_and_respond(queue, envelope):
                    sent.append(dict(envelope))
                    message_type = envelope.get("type")
                    request_id = envelope.get("request_id")
                    if queue.name == bridge.OUTBOX and message_type in {"hello", "probe_network"}:
                        inbox.write(
                            {
                                "message_id": f"host_{message_type}",
                                "request_id": request_id,
                                "type": "hello_ack" if message_type == "hello" else "no_work",
                                "session_id": "sess",
                                "generation_id": "gen",
                                "payload": {"last_output_sequence_by_turn": {}} if message_type == "hello" else {},
                            }
                        )
                    return original_write(queue, envelope)

                with mock.patch.object(bridge.Queue, "write", write_and_respond):
                    bridge.run_probe(args)

            run_network_probe.assert_not_called()
            self.assertEqual([message["type"] for message in sent if message["type"] in {"hello", "probe_network"}], ["hello", "probe_network"])
            self.assertTrue((Path(root) / bridge.HEARTBEAT / bridge.BRIDGE_HEARTBEAT).exists())
            self.assertTrue((Path(root) / bridge.HEARTBEAT / bridge.CHECKPOINT_READY).exists())


class PiTurnBoundaryTest(unittest.TestCase):
    """Two consecutive turns through one pi process must not share a stream.

    The invariant under test: when run_turn returns, pi's stdout holds no events
    belonging to the turn that just finished, so the next turn's first read is
    its own first event.

    Which of these failed before the boundary-barrier fix is worth recording,
    because it explains the shape of the production bug exactly:

      - the COMPLETED path was accidentally safe. After breaking on turn_end the
        old code called _request_response for session stats, and that helper
        skips events until the matching id arrives — incidentally swallowing the
        leftover agent_end. So test_second_turn_does_not_consume_first_turns_
        agent_end passes either way. It is kept as a guard: it pins the invariant
        for the path that no longer has an accidental drain to rely on.

      - the ABORTED and FAILED paths returned early, BEFORE that stats call, so
        nothing consumed the residue. The next turn then broke on the previous
        turn's agent_end (and, after an abort, response{command:"abort"}) and
        reported a terminal status having never run its own prompt. The two abort
        tests below fail without the fix, which is the regression that matters.
    """

    def _pi_env(self, root):
        agent_dir = Path(root) / "agent"
        session_dir = agent_dir / "sessions"
        session_dir.mkdir(parents=True)
        session_file = session_dir / "session-1.jsonl"
        session_file.write_text("{}\n", encoding="utf-8")
        models = agent_dir / "models.json"
        settings = agent_dir / "settings.json"
        for path in (models, settings):
            path.write_text("{}\n", encoding="utf-8")
            path.chmod(0o444)
        return agent_dir, session_dir, session_file, models, settings

    def _patches(self, agent_dir, session_dir, models, settings, fake):
        return [
            mock.patch.object(bridge, "PI_CODING_AGENT_DIR", str(agent_dir)),
            mock.patch.object(bridge, "PI_SESSION_DIR", str(session_dir)),
            mock.patch.object(bridge, "PI_MODELS_SANDBOX_PATH", str(models)),
            mock.patch.object(bridge, "PI_SETTINGS_SANDBOX_PATH", str(settings)),
            mock.patch.object(bridge, "PI_SYSTEM_PROMPT_PATH", str(agent_dir / "missing-prompt")),
            mock.patch.object(bridge.subprocess, "Popen", side_effect=lambda command, **kw: fake),
            mock.patch.dict(
                os.environ,
                {
                    "PI_CODING_AGENT_DIR": str(agent_dir),
                    "PI_CODING_AGENT_SESSION_DIR": str(session_dir),
                    "PI_OFFLINE": "1",
                    "PI_SKIP_VERSION_CHECK": "1",
                    "PI_TELEMETRY": "0",
                },
                clear=False,
            ),
        ]

    def _grant(self, turn_id, session_file, version):
        payload = {
            "schema_version": 1,
            "driver_id": "pi",
            "state_kind": "pi_session",
            "session_dir": str(session_file.parent),
            "selected_session_relpath": session_file.name,
            "selected_session_file": str(session_file),
            "selected_session_id": "pi-session-1",
            "last_completed_turn_id": "0",
        }
        return {
            "turn_id": turn_id,
            "driver_state": {
                "driver_id": "pi",
                "state_digest": bridge.driver_state_digest(payload),
                "state_version": version,
                "state_payload": payload,
            },
        }

    def _run(self, runner, grant, content):
        emitted = []
        runner.set_turn_context(grant)
        result = runner.run_turn(content, lambda stream, line: emitted.append(json.loads(line)))
        return result, emitted

    def test_second_turn_does_not_consume_first_turns_agent_end(self):
        with tempfile.TemporaryDirectory() as root:
            agent_dir, session_dir, session_file, models, settings = self._pi_env(root)
            fake = ScriptedPi(session_file)
            runner = bridge.PiRPCTurnRunner()
            with contextlib.ExitStack() as stack:
                for patch in self._patches(agent_dir, session_dir, models, settings, fake):
                    stack.enter_context(patch)
                first, first_events = self._run(runner, self._grant(1, session_file, 1), "hello")
                second, second_events = self._run(runner, self._grant(2, session_file, 2), "again")

            self.assertEqual(first[0], "completed")
            self.assertEqual(second[0], "completed")
            # The regression: turn 2 must see its OWN reply, not turn 1's residue.
            deltas = [
                event["assistantMessageEvent"]["delta"]
                for event in second_events
                if event.get("type") == "message_update"
            ]
            self.assertEqual(deltas, ["reply-2"])
            self.assertEqual(fake.prompts, ["hello", "again"])
            # Both turns must produce a real terminal event of their own.
            for events in (first_events, second_events):
                terminals = [e for e in events if e.get("type") in {"turn_end", "agent_end"}]
                self.assertTrue(terminals, f"turn produced no terminal event: {events}")

    def test_turn_after_abort_runs_its_own_prompt(self):
        """The production failure: turn N is stopped, turn N+1 completes instantly.

        An aborted turn leaves agent_end AND response{abort} unread, so the next
        turn used to break on the first of them — reporting 'completed' in
        milliseconds while its prompt was never answered.
        """
        with tempfile.TemporaryDirectory() as root:
            agent_dir, session_dir, session_file, models, settings = self._pi_env(root)
            fake = ScriptedPi(session_file, abort_on_prompt={"harness-turn-1"})
            runner = bridge.PiRPCTurnRunner()
            with contextlib.ExitStack() as stack:
                for patch in self._patches(agent_dir, session_dir, models, settings, fake):
                    stack.enter_context(patch)
                aborted, _ = self._run(runner, self._grant(1, session_file, 1), "count to 400")
                after, after_events = self._run(runner, self._grant(2, session_file, 2), "say hi")

            self.assertEqual(aborted[:3], ("canceled", "user_interrupted", "interrupted by user"))
            self.assertEqual(after[0], "completed")
            deltas = [
                event["assistantMessageEvent"]["delta"]
                for event in after_events
                if event.get("type") == "message_update"
            ]
            self.assertEqual(deltas, ["reply-2"])
            self.assertEqual(fake.prompts, ["count to 400", "say hi"])

    def test_aborted_turn_does_not_carry_marker_into_next_turn(self):
        """set_turn_context clears aborted_turn_id; assert it end-to-end."""
        with tempfile.TemporaryDirectory() as root:
            agent_dir, session_dir, session_file, models, settings = self._pi_env(root)
            fake = ScriptedPi(session_file, abort_on_prompt={"harness-turn-1"})
            runner = bridge.PiRPCTurnRunner()
            with contextlib.ExitStack() as stack:
                for patch in self._patches(agent_dir, session_dir, models, settings, fake):
                    stack.enter_context(patch)
                self._run(runner, self._grant(1, session_file, 1), "count to 400")
                self.assertEqual(runner.aborted_turn_id, None)
                after, _ = self._run(runner, self._grant(2, session_file, 2), "say hi")
            self.assertEqual(after[0], "completed")
            self.assertIsNone(runner.aborted_turn_id)


class EntrypointStaticTest(unittest.TestCase):
    def test_manifest_digest_matches_host_fixture(self):
        fixture = Path(__file__).resolve().parent / "testdata" / "control-manifest-payload.json"
        payload = json.loads(fixture.read_text(encoding="utf-8"))
        digest = "d6bec86f6b3b600c5f44d2d668d82594e5a96160fa22650cbff02544edc12497"

        self.assertEqual(manifest.manifest_digest(payload), digest)
        with tempfile.TemporaryDirectory() as root:
            control_file = Path(root) / "session.json"
            control_file.write_text(json.dumps({"payload": payload, "digest": digest}), encoding="utf-8")
            self.assertEqual(manifest.load_control_manifest(control_file), payload)

            payload["generation_id"] = "gen_tampered"
            tampered_file = Path(root) / "tampered-session.json"
            tampered_file.write_text(json.dumps({"payload": payload, "digest": digest}), encoding="utf-8")
            with self.assertRaises(SystemExit) as raised:
                manifest.load_control_manifest(tampered_file)
            self.assertEqual(str(raised.exception), "control manifest digest mismatch")

    def test_entrypoint_has_probe_mode(self):
        entrypoint = SCRIPT.with_name("harness-agent-entrypoint")
        text = entrypoint.read_text(encoding="utf-8")
        self.assertIn('HARNESS_BRIDGE_MODE:-}" = "probe"', text)
        self.assertIn("exec /usr/local/bin/harness-bridge-client probe", text)
        self.assertIn('HARNESS_BRIDGE_MODE:-}" = "claim-loop"', text)
        self.assertIn("exec_bridge_client claim-loop", text)
        self.assertIn('HARNESS_BRIDGE_MODE:-}" = "heartbeat-loop"', text)
        self.assertIn("exec_bridge_client heartbeat-loop", text)
        self.assertNotIn('HARNESS_BRIDGE_MODE:-auto}" = "auto"', text)
        self.assertNotIn("heartbeat-loop &", text)

    def test_entrypoint_expected_mismatch_message_matches_host_classifier(self):
        entrypoint = SCRIPT.with_name("harness-agent-entrypoint")
        text = entrypoint.read_text(encoding="utf-8")
        self.assertIn('expected ${label}=${expected} got ${actual}', text)

    def test_entrypoint_requires_configured_agent_identity(self):
        entrypoint = SCRIPT.with_name("harness-agent-entrypoint")
        text = entrypoint.read_text(encoding="utf-8")
        self.assertIn('AGENT_UID="${HARNESS_AGENT_UID:-}"', text)
        self.assertIn('AGENT_GID="${HARNESS_AGENT_GID:-}"', text)
        self.assertIn("HARNESS_AGENT_UID is required", text)
        self.assertIn("HARNESS_AGENT_GID is required", text)
        self.assertNotIn('AGENT_UID="65534"', text)
        self.assertNotIn('AGENT_GID="65534"', text)

    def test_entrypoint_supports_host_only_credentials(self):
        entrypoint = SCRIPT.with_name("harness-agent-entrypoint")
        text = entrypoint.read_text(encoding="utf-8")
        self.assertIn("exec_bridge_client()", text)
        self.assertIn('/usr/local/bin/harness-bridge-client "$@"', text)
        self.assertIn("sandbox secret mounts are not supported", text)
        self.assertNotIn("AGENT_USER", text)
        self.assertNotIn("ensure_agent_user", text)
        self.assertNotIn("--groups \"$HARNESS_SECRET_READERS_GID\"", text)

    def test_entrypoint_exports_pi_startup_gates(self):
        entrypoint = SCRIPT.with_name("harness-agent-entrypoint")
        text = entrypoint.read_text(encoding="utf-8")
        self.assertIn('driver_runtime = data.get("driver_runtime")', text)
        self.assertIn("if not isinstance(driver_runtime, dict):", text)
        self.assertIn("return driver_runtime.get(key)", text)
        self.assertNotIn("return driver_runtime.get(key, data.get(key))", text)
        self.assertIn('emit("HARNESS_DRIVER_ID", data.get("driver_id"))', text)
        self.assertNotIn('emit("CLAUDE_SESSION_UUID"', text)
        self.assertNotIn('emit("CLAUDE_RESUME"', text)
        self.assertIn('emit("PI_CODING_AGENT_DIR", driver_runtime_value("pi_coding_agent_dir"))', text)
        self.assertIn('emit("PI_CODING_AGENT_SESSION_DIR", driver_runtime_value("pi_coding_agent_session_dir"))', text)
        self.assertIn('emit("PI_OFFLINE", driver_runtime_value("pi_offline"))', text)
        self.assertIn('emit("PI_SKIP_VERSION_CHECK", driver_runtime_value("pi_skip_version_check"))', text)
        self.assertIn('pi_telemetry_disabled = driver_runtime_value("pi_telemetry_disabled")', text)
        self.assertIn('emit("PI_TELEMETRY", "0" if pi_telemetry_disabled else "1")', text)

    # ---- host → sandbox turn control (abort / steer) ----
    #
    # These pin the delivery path that lets a Stop reach a turn that is already
    # executing. Before it existed, the only way to stop a turn was to destroy the
    # generation, which cost the user their sandbox and every queued follow-up.

    def _control_payload(self, **overrides):
        payload = {"command": "abort", "token": "tok-1", "turn_id": 7}
        payload.update(overrides)
        return payload

    def _write_control(self, root, *commands, version=bridge.TURN_CONTROL_VERSION):
        """Write the control log exactly as the host's AppendTurnControl does."""
        inbox = Path(root) / bridge.INBOX
        inbox.mkdir(parents=True, exist_ok=True)
        (inbox / bridge.TURN_CONTROL_FILE).write_text(
            json.dumps({"version": version, "commands": list(commands)}), encoding="utf-8"
        )

    def _write_control_raw(self, root, payload):
        inbox = Path(root) / bridge.INBOX
        inbox.mkdir(parents=True, exist_ok=True)
        (inbox / bridge.TURN_CONTROL_FILE).write_text(json.dumps(payload), encoding="utf-8")

    def test_turn_control_file_is_invisible_to_the_message_queue(self):
        """The control file shares the inbox with seq-named responses.

        It must be inert there: the queue reader has to skip it (it is not a
        response) and the host's prune must not treat it as one. Both hinge on the
        name not parsing as a sequence, so pin that rather than trusting it.
        """
        with tempfile.TemporaryDirectory() as root:
            self.assertIsNone(bridge.parse_seq_name(bridge.TURN_CONTROL_FILE))
            queue = bridge.Queue(root, bridge.INBOX)
            queue.write({"type": "no_work", "message_id": "m1", "session_id": "s", "generation_id": "g", "payload": {}})
            self._write_control(root, self._control_payload())
            # The queue sees only the real response, not the control file.
            self.assertEqual([e["message_id"] for _, _, e in queue.read_all()], ["m1"])

    def test_read_turn_control_rejects_unusable_payloads(self):
        """Polled every 200ms while a turn runs, so it must never raise.

        The host replaces this file with an atomic rename; a reader can catch it
        missing or half-visible. Each rejection below yields an empty log so the
        watcher simply looks again on its next tick.
        """
        with tempfile.TemporaryDirectory() as root:
            # Absent file: the normal case for the whole life of most turns.
            self.assertEqual(bridge.read_turn_control(root), [])
            for label, payload in [
                ("not an object", ["abort"]),
                ("no version", {"commands": []}),
                # A version this rootfs does not know means the host is writing a
                # shape it cannot read. Refusing beats guessing at the fields.
                ("future version", {"version": bridge.TURN_CONTROL_VERSION + 1, "commands": [self._control_payload()]}),
                ("commands not a list", {"version": bridge.TURN_CONTROL_VERSION, "commands": {"command": "abort"}}),
            ]:
                self._write_control_raw(root, payload)
                self.assertEqual(bridge.read_turn_control(root), [], label)
            (Path(root) / bridge.INBOX / bridge.TURN_CONTROL_FILE).write_text("{not json", encoding="utf-8")
            self.assertEqual(bridge.read_turn_control(root), [], "malformed json")

            # One unusable entry is skipped rather than discarding the log: the
            # good commands around it are still the user's, and still deliverable.
            self._write_control(
                root,
                {"command": "explode", "token": "t", "turn_id": 1},
                {"command": "abort", "turn_id": 1},
                {"command": "abort", "token": "t"},
                {"command": "abort", "token": "t", "turn_id": "abc"},
                "not-an-object",
                self._control_payload(command="steer", message="do this instead"),
            )
            commands = bridge.read_turn_control(root)
            self.assertEqual(
                [(c["command"], c["token"], c["turn_id"], c["message"]) for c in commands],
                [("steer", "tok-1", 7, "do this instead")],
            )

    def test_read_turn_control_preserves_command_order(self):
        """The log is ordered, and steering twice must arrive in the order typed."""
        with tempfile.TemporaryDirectory() as root:
            self._write_control(
                root,
                self._control_payload(command="steer", token="tok-1", message="first"),
                self._control_payload(command="steer", token="tok-2", message="second"),
            )
            self.assertEqual([c["message"] for c in bridge.read_turn_control(root)], ["first", "second"])

    def test_deliver_control_abort_marks_the_turn_and_writes_pi_abort(self):
        runner = bridge.PiRPCTurnRunner()
        runner.proc = mock.Mock()
        runner.proc.poll.return_value = None
        written = []
        runner._write_rpc = lambda payload: written.append(payload)

        self.assertTrue(runner.deliver_control("abort", 7))
        self.assertEqual([p["type"] for p in written], ["abort"])
        # Recorded so run_turn can call the terminal event a user stop rather than
        # an agent failure, even if pi's event omits the stopReason.
        self.assertEqual(runner.aborted_turn_id, 7)

    def test_deliver_control_steer_sends_the_message_without_aborting(self):
        """steer is pi's native injection: the running turn continues.

        This is the difference that makes steer worth having — the agent picks up
        the new instruction while keeping the work it has already done, instead of
        that work being thrown away and redone in a fresh turn.
        """
        runner = bridge.PiRPCTurnRunner()
        runner.proc = mock.Mock()
        runner.proc.poll.return_value = None
        written = []
        runner._write_rpc = lambda payload: written.append(payload)

        self.assertTrue(runner.deliver_control("steer", 7, "actually, do this"))
        self.assertEqual(written, [{"id": "harness-steer-7", "type": "steer", "message": "actually, do this"}])
        self.assertIsNone(runner.aborted_turn_id, "steer must not mark the turn aborted")

        # An empty steer would inject nothing; refuse it rather than confuse pi.
        written.clear()
        self.assertFalse(runner.deliver_control("steer", 7, ""))
        self.assertEqual(written, [])

    def test_deliver_control_is_a_noop_without_a_live_pi(self):
        runner = bridge.PiRPCTurnRunner()
        self.assertFalse(runner.deliver_control("abort", 7))
        runner.proc = mock.Mock()
        runner.proc.poll.return_value = 1  # exited
        self.assertFalse(runner.deliver_control("abort", 7))

    def test_set_turn_context_clears_a_previous_abort_marker(self):
        """Otherwise the turn after a stopped one is cancelled on arrival."""
        runner = bridge.PiRPCTurnRunner()
        runner.aborted_turn_id = 7
        runner.set_turn_context({"turn_id": 8})
        self.assertIsNone(runner.aborted_turn_id)

    def test_control_watcher_applies_each_token_once_and_ignores_stale_turns(self):
        class FakeRunner:
            def __init__(self):
                self.calls = []

            def deliver_control(self, command, turn_id, message=""):
                self.calls.append((command, turn_id, message))
                return True

        with tempfile.TemporaryDirectory() as root:
            runner = FakeRunner()
            # An entry naming turn 99 while turn 7 runs is a leftover from a turn
            # that already ended. Applying it would abort the wrong turn.
            self._write_control(root, self._control_payload(turn_id=99))
            stop = bridge.start_turn_control_watcher(root, runner, 7, interval=0.01)
            deadline = time.monotonic() + 0.5
            while time.monotonic() < deadline and not runner.calls:
                time.sleep(0.01)
            stop.set()
            self.assertEqual(runner.calls, [], "stale control file was applied to the wrong turn")

            runner = FakeRunner()
            self._write_control(root, self._control_payload(command="steer", token="tok-2", message="hi"))
            stop = bridge.start_turn_control_watcher(root, runner, 7, interval=0.01)
            deadline = time.monotonic() + 1.0
            while time.monotonic() < deadline and not runner.calls:
                time.sleep(0.01)
            # The file stays on disk (the sandbox cannot delete it — the inbox is
            # read-only), so the token, not the file, is what prevents re-delivery.
            time.sleep(0.15)
            stop.set()
            self.assertEqual(runner.calls, [("steer", 7, "hi")])

    def test_control_watcher_delivers_every_queued_command_in_order(self):
        """Two commands can land inside one poll interval; neither may be lost.

        This is why the file is a log rather than a single slot: with one slot the
        watcher would see only the last write, and a steer holds text the user
        typed.
        """
        class FakeRunner:
            def __init__(self):
                self.calls = []

            def deliver_control(self, command, turn_id, message=""):
                self.calls.append((command, turn_id, message))
                return True

        with tempfile.TemporaryDirectory() as root:
            runner = FakeRunner()
            self._write_control(
                root,
                self._control_payload(command="steer", token="tok-1", message="first"),
                self._control_payload(command="steer", token="tok-2", message="second"),
            )
            stop = bridge.start_turn_control_watcher(root, runner, 7, interval=0.01)
            deadline = time.monotonic() + 1.0
            while time.monotonic() < deadline and len(runner.calls) < 2:
                time.sleep(0.01)
            stop.set()
            self.assertEqual(runner.calls, [("steer", 7, "first"), ("steer", 7, "second")])

    def test_control_watcher_stops_at_the_first_abort_in_the_log(self):
        """Anything after an abort belongs to a turn this watcher will never see."""
        class FakeRunner:
            def __init__(self):
                self.calls = []

            def deliver_control(self, command, turn_id, message=""):
                self.calls.append((command, turn_id, message))
                return True

        with tempfile.TemporaryDirectory() as root:
            runner = FakeRunner()
            self._write_control(
                root,
                self._control_payload(command="steer", token="tok-1", message="keep going"),
                self._control_payload(command="abort", token="tok-2"),
                self._control_payload(command="steer", token="tok-3", message="never delivered"),
            )
            stop = bridge.start_turn_control_watcher(root, runner, 7, interval=0.01)
            deadline = time.monotonic() + 1.0
            while time.monotonic() < deadline and not any(c[0] == "abort" for c in runner.calls):
                time.sleep(0.01)
            time.sleep(0.15)
            stop.set()
            self.assertEqual(runner.calls, [("steer", 7, "keep going"), ("abort", 7, "")])

    def test_control_watcher_survives_a_runner_that_raises(self):
        """This thread runs inside the sandbox's init process.

        An exception escaping it would kill the sandbox — the very outcome this
        change exists to stop — so the loop must absorb anything the runner throws.
        """
        class ExplodingRunner:
            def deliver_control(self, command, turn_id, message=""):
                raise RuntimeError("pi stdin is gone")

        with tempfile.TemporaryDirectory() as root:
            self._write_control(root, self._control_payload())
            stop = bridge.start_turn_control_watcher(root, ExplodingRunner(), 7, interval=0.01)
            time.sleep(0.2)
            self.assertFalse(stop.is_set(), "watcher should still be running")
            stop.set()

    def test_control_watcher_declines_a_runner_without_control_support(self):
        """The shell driver has no pi to abort, so it gets no watcher thread.

        Asserted by observation rather than by inspecting internals: with a runner
        that cannot take control commands, dropping a valid control file must
        produce no effect at all.
        """
        with tempfile.TemporaryDirectory() as root:
            # Set difference over the live thread objects, not active_count(): a
            # daemon watcher another test just stopped may still be winding down,
            # which would inflate a count-based baseline and fail this at random.
            before = set(threading.enumerate())
            self._write_control(root, self._control_payload())
            stop = bridge.start_turn_control_watcher(root, object(), 7, interval=0.01)
            time.sleep(0.1)
            self.assertEqual(set(threading.enumerate()) - before, set(), "no watcher thread should have started")
            # Still returns a usable Event so execute_grant's finally can call set()
            # unconditionally.
            stop.set()

    def test_claim_loop_starts_after_workspace_and_agent_home_setup(self):
        entrypoint = SCRIPT.with_name("harness-agent-entrypoint")
        text = entrypoint.read_text(encoding="utf-8")
        claim_loop = text.index('HARNESS_BRIDGE_MODE:-}" = "claim-loop"')
        cd_workspace = text.index("cd /workspace")
        self.assertLess(text.index(': "${SESSION_WORKSPACE:?SESSION_WORKSPACE is required in control file}"'), claim_loop)
        self.assertLess(text.index(': "${HARNESS_AGENT_HOME:?HARNESS_AGENT_HOME is required in control file}"'), claim_loop)
        self.assertLess(text.index('[ "$SESSION_WORKSPACE" != "/workspace" ]'), claim_loop)
        self.assertLess(text.index('[ "$AGENT_HOME" != "/agent-home" ]'), claim_loop)
        self.assertLess(cd_workspace, claim_loop)
        self.assertLess(text.index("exec_bridge_client()"), claim_loop)
        self.assertNotIn("/sessions/$SESSION_ID", text)
        self.assertNotIn("/agent-homes/$SESSION_ID", text)
        self.assertNotIn("ln -sfn", text)


def argparse_namespace(**kwargs):
    return type("Args", (), kwargs)()


if __name__ == "__main__":
    unittest.main()
