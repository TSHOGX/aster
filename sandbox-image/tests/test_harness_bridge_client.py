import contextlib
import importlib.util
from importlib.machinery import SourceFileLoader
import contextlib
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
    pass

    def test_build_rootfs_reuse_generates_pi_agent_manifest(self):
        with tempfile.TemporaryDirectory() as root:
            rootfs = Path(root)
            bin_dir = rootfs / "usr" / "local" / "bin"
            bin_dir.mkdir(parents=True)
            pi = bin_dir / "pi"
            pi.write_text("#!/bin/sh\n", encoding="utf-8")
            pi.chmod(0o755)
            rpc_types = rootfs / "usr/local/lib/node_modules/@earendil-works/pi-coding-agent/dist/modes/rpc/rpc-types.d.ts"
            rpc_types.parent.mkdir(parents=True)
            rpc_types.write_text('type Command = { type: "abort" } | { type: "steer"; message: string };\n', encoding="utf-8")

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
            self.assertIn("/root/.pi/agent/models.json", pi_entry["installed_config_paths"])

    pass

    pass

    pass

    pass

    pass

    pass

    pass

    pass

    pass

    pass

    pass

    pass

    pass

    pass

    pass

    pass

    def test_execute_grant_rejects_non_run_turn_schema(self):
        with tempfile.TemporaryDirectory() as root:
            client = RecordingClient(root, "sess", "gen", "sh", poll_interval=0.001)

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
            client = RecordingClient(root, "sess", "gen", "native_events_probe", poll_interval=0.001)
            runner = bridge.make_turn_runner("native_events_probe")
            with contextlib.nullcontext():
                bridge.execute_grant(
                    client,
                    runner,
                    {"turn_id": 9, "sequence": 1, "turn_input_schema": "RunTurn", "input": {"content": "native ok"}},
                )

            messages = client.messages
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
            client = RecordingClient(root, "sess", "gen", "pi", poll_interval=0.001)

            class FailedRunner:
                def run_turn(self, content, emit):
                    return "failed", "agent_execution_failed", "boom", {"driver_id": "pi"}

            with contextlib.nullcontext():
                bridge.execute_grant(
                    client,
                    FailedRunner(),
                    {"turn_id": 9, "sequence": 1, "turn_input_schema": "RunTurn", "input": {"content": "run"}},
                )

            messages = client.messages
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
                        with self.assertRaisesRegex(RuntimeError, "must be read-only"):
                            bridge.validate_pi_config_materialization()

    pass

    pass

    def test_network_probe_checks_health_only(self):
        with mock.patch.object(bridge, "http_status", return_value=200) as http_status:
            bridge.run_network_probe("http://10.240.0.1:8082", {200}, 0.1)

        http_status.assert_called_once_with("http://10.240.0.1:8082/healthz", timeout=0.1)

    def test_network_probe_rejects_unaccepted_statuses(self):
        with mock.patch.object(bridge, "http_status", return_value=204):
            with self.assertRaisesRegex(RuntimeError, r"probe GET /healthz returned 204"):
                bridge.run_network_probe("http://10.240.0.1:8082", {200}, 0.1)

    pass

    pass


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

    def test_host_abort_waits_for_idle_ack_before_stats_boundary(self):
        with tempfile.TemporaryDirectory() as root:
            agent_dir, session_dir, session_file, models, settings = self._pi_env(root)
            fake = ScriptedPi(session_file, abort_on_prompt={'harness-turn-1'})
            runner = bridge.PiRPCTurnRunner()
            original_dispatch = fake._dispatch
            original_read = fake.readline
            acknowledged = []
            def dispatch(request):
                if request.get('id') == 'harness-turn-1-stats':
                    self.assertTrue(acknowledged, 'stats raced ahead of asynchronous abort settlement')
                original_dispatch(request)
                if request.get('type') == 'prompt':
                    runner.aborted_turn_id = 1
                    fake.pending[-1] = jline({'id': 'harness-abort-1', 'type': 'response', 'command': 'abort', 'success': True})
            def read():
                line = original_read()
                if line and json.loads(line).get('id') == 'harness-abort-1':
                    acknowledged.append(True)
                return line
            fake._dispatch = dispatch
            fake.readline = read
            with contextlib.ExitStack() as stack:
                for patch in self._patches(agent_dir, session_dir, models, settings, fake):
                    stack.enter_context(patch)
                result, _ = self._run(runner, self._grant(1, session_file, 1), 'interrupt me')
            self.assertEqual(result[0], 'canceled')
            self.assertTrue(acknowledged)

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
    pass

    pass

    pass

    pass

    pass

    pass

    # ---- host → sandbox turn control (abort / steer) ----
    #
    # These pin the delivery path that lets a Stop reach a turn that is already
    # executing. Before it existed, the only way to stop a turn was to destroy the
    # generation, which cost the user their sandbox and every queued follow-up.

    def _control_payload(self, **overrides):
        payload = {"command": "abort", "token": "tok-1", "turn_id": 7}
        payload.update(overrides)
        return payload


    def _write_control_raw(self, root, payload):
        inbox = Path(root) / bridge.INBOX
        inbox.mkdir(parents=True, exist_ok=True)
        (inbox / bridge.TURN_CONTROL_FILE).write_text(json.dumps(payload), encoding="utf-8")

    pass

    pass

    pass

    def test_deliver_control_abort_marks_the_turn_and_writes_pi_abort(self):
        runner = bridge.PiRPCTurnRunner()
        runner.proc = mock.Mock()
        runner.proc.poll.return_value = None
        written = []
        runner._write_rpc = lambda payload: written.append(payload)
        runner.current_grant = {'turn_id': 7}
        runner.control_ready = True

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
        runner.current_grant = {'turn_id': 7}
        runner.control_ready = True

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
        runner.current_grant = {'turn_id': 7}
        runner.control_ready = True
        runner.proc = mock.Mock()
        runner.proc.poll.return_value = 1
        self.assertFalse(runner.deliver_control('abort', 7))

    def test_pi_control_waits_until_prompt_is_ready(self):
        runner = bridge.PiRPCTurnRunner()
        runner.proc = mock.Mock()
        runner.proc.poll.return_value = None
        written = []
        runner._write_rpc = lambda payload: written.append(payload)
        runner.set_turn_context({'turn_id': 7})
        self.assertFalse(runner.deliver_control('abort', 7))
        self.assertEqual(written, [])
        runner.control_ready = True
        self.assertTrue(runner.deliver_control('abort', 7))
        runner.set_turn_context({'turn_id': 8})
        self.assertFalse(runner.deliver_control('abort', 7))

    def test_shell_control_before_process_start_is_retained_for_current_turn(self):
        runner = bridge.ShellTurnRunner()
        runner.set_turn_context({'turn_id': 7})
        self.assertTrue(runner.deliver_control('abort', 7))
        self.assertTrue(runner.aborted)
        runner.set_turn_context({'turn_id': 8})
        self.assertFalse(runner.deliver_control('abort', 7))
        self.assertFalse(runner.aborted)

    def test_set_turn_context_clears_a_previous_abort_marker(self):
        """Otherwise the turn after a stopped one is cancelled on arrival."""
        runner = bridge.PiRPCTurnRunner()
        runner.aborted_turn_id = 7
        runner.set_turn_context({"turn_id": 8})
        self.assertIsNone(runner.aborted_turn_id)

    pass

    pass

    pass

    pass

    pass

    pass


def argparse_namespace(**kwargs):
    return type("Args", (), kwargs)()


if __name__ == "__main__":
    unittest.main()

class RecordingClient(bridge.BridgeClient):
    def __init__(self,root,session,generation,driver,poll_interval=0.05):
        self.session_id=session; self.generation_id=generation;self.last_output_sequence_by_turn={};self.messages=[]
    def send(self,*args,**kwargs):
        envelope=self.envelope(*args,**kwargs);self.messages.append(envelope);return envelope
    def start_control_watcher(self,runner,turn):
        return threading.Event()
