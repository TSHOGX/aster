import importlib.util
import json
import os
import subprocess
import sys
import tempfile
import unittest
from importlib.machinery import SourceFileLoader
from pathlib import Path


LIB = Path(__file__).resolve().parents[1] / "files" / "usr" / "local" / "lib" / "agents_knowledge.py"
CLI = Path(__file__).resolve().parents[1] / "files" / "usr" / "local" / "bin" / "kb"
SEED = (
    Path(__file__).resolve().parents[1]
    / "files"
    / "usr"
    / "local"
    / "share"
    / "agents-kb-seed"
    / "knowledge"
)

spec = importlib.util.spec_from_loader(
    "agents_knowledge", SourceFileLoader("agents_knowledge", str(LIB))
)
kb = importlib.util.module_from_spec(spec)
sys.modules["agents_knowledge"] = kb
spec.loader.exec_module(kb)


ARTICLE = """---
id: dingpan/tools
title: Dingpan MCP tools
description: The three supported dingpan MCP tools and how to call them
tags: [dingpan, mcp, tools]
---

# Tools

Use listTables, tableSchemas, executeQuery.
"""


class ParseFrontmatterTest(unittest.TestCase):
    def test_inline_tags_and_quoted_scalars(self):
        raw = (
            "---\n"
            'id: "billing/refund-policy"\n'
            "title: Refund policy\n"
            "description: When customers may request a refund\n"
            "tags: [billing, refund]\n"
            "---\n"
            "\n"
            "# Refund policy\n"
        )
        parsed = kb.parse_knowledge_document(raw, Path("/tmp/x.md"), Path("/tmp"))
        self.assertIsNotNone(parsed)
        self.assertEqual(parsed.id, "billing/refund-policy")
        self.assertEqual(parsed.title, "Refund policy")
        self.assertEqual(parsed.tags, ("billing", "refund"))
        self.assertEqual(parsed.body, "# Refund policy")

    def test_yaml_list_tags(self):
        raw = (
            "---\n"
            "description: A note\n"
            "tags:\n"
            "  - dingpan\n"
            "  - tables\n"
            "---\n"
            "body\n"
        )
        data, body = kb.parse_frontmatter(raw)
        self.assertEqual(data["tags"], ["dingpan", "tables"])
        self.assertEqual(body.strip(), "body")

    def test_missing_description_is_rejected(self):
        raw = "---\nid: foo\n---\nbody\n"
        self.assertIsNone(kb.parse_knowledge_document(raw, Path("/tmp/foo.md"), Path("/tmp")))

    def test_nested_yaml_object_is_rejected(self):
        raw = "---\ndescription: x\nmeta: {a: 1}\n---\nbody\n"
        self.assertIsNone(kb.parse_frontmatter(raw))


class CatalogTest(unittest.TestCase):
    def test_missing_root_is_empty(self):
        root = Path("/tmp/does-not-exist-agents-kb")
        self.assertEqual(kb.load_knowledge_articles(root), [])

    def test_skips_readme_without_frontmatter_and_malformed_files(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            (root / "README.md").write_text("# how to add articles\n", encoding="utf-8")
            (root / "broken.md").write_text("no frontmatter\n", encoding="utf-8")
            nested = root / "dingpan"
            nested.mkdir()
            (nested / "tools.md").write_text(ARTICLE, encoding="utf-8")
            articles = kb.load_knowledge_articles(root)
            self.assertEqual([a.id for a in articles], ["dingpan/tools"])

    def test_default_id_from_path(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            path = root / "dingpan" / "tables.md"
            path.parent.mkdir()
            path.write_text(
                "---\ndescription: Table map for GaussDB digital_operation\n---\n# tables\n",
                encoding="utf-8",
            )
            article = kb.load_knowledge_article(root, "dingpan/tables")
            self.assertIsNotNone(article)
            self.assertEqual(article.id, "dingpan/tables")

    def test_rejects_path_escape_ids(self):
        self.assertIsNone(kb.article_path_for_id(Path("/tmp/kb"), "../etc/passwd"))
        self.assertFalse(kb.is_knowledge_id("../secret"))
        self.assertFalse(kb.is_knowledge_id("Dingpan/Tools"))

    def test_catalog_render_and_get_frame(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp) / "knowledge"
            dest = root / "dingpan"
            dest.mkdir(parents=True)
            (dest / "tools.md").write_text(ARTICLE, encoding="utf-8")
            articles = kb.load_knowledge_articles(root)
            entries = kb.catalog_entries_from_articles(articles)
            catalog = kb.render_catalog_message(entries)
            self.assertIn("<available_knowledge>", catalog)
            self.assertIn("`dingpan/tools`", catalog)
            self.assertNotIn("listTables", catalog)
            body = kb.render_knowledge_content(articles[0])
            self.assertIn('<knowledge_content id="dingpan/tools">', body)
            self.assertIn("listTables", body)


class SeedTreeTest(unittest.TestCase):
    def test_seed_articles_are_loadable(self):
        self.assertTrue(SEED.is_dir(), f"missing seed knowledge tree: {SEED}")
        articles = kb.load_knowledge_articles(SEED)
        ids = {article.id for article in articles}
        self.assertIn("dingpan/overview", ids)
        self.assertIn("dingpan/tools", ids)
        self.assertIn("dingpan/tables", ids)
        self.assertIn("dingpan/query-cookbook", ids)
        self.assertIn("dingpan/pitfalls", ids)
        catalog = kb.render_catalog_message(kb.catalog_entries_from_articles(articles))
        self.assertNotIn("<knowledge_body>", catalog)

    def test_agent_facing_docs_do_not_publish_gateway_url(self):
        articles = kb.load_knowledge_articles(SEED)
        for article in articles:
            self.assertNotIn("harness-mcp.internal", article.body, msg=article.id)
        constitution = (SEED.parent / "CONSTITUTION.md").read_text(encoding="utf-8")
        skill = (
            SEED.parent / "skills" / "mcp-data-query" / "SKILL.md"
        ).read_text(encoding="utf-8")
        self.assertNotIn("harness-mcp.internal", constitution)
        self.assertNotIn("harness-mcp.internal", skill)
        self.assertIn("mcp_client.py", constitution)
        self.assertIn("mcp_client.py", skill)
        self.assertIn("mcp_dp_dingpan_mcp_server_listTables", constitution)
        self.assertIn("mcp_dp_dingpan_mcp_server_listTables", skill)
        self.assertIn("mcp_dingpan_knowledge_kb_catalog", skill)


class CliTest(unittest.TestCase):
    def _cli_env(self):
        env = os.environ.copy()
        env["KB_ALLOW_CLI"] = "1"
        return env

    def test_catalog_and_get_roundtrip(self):
        env = self._cli_env()
        proc = subprocess.run(
            [sys.executable, str(CLI), "--root", str(SEED), "catalog"],
            check=True,
            capture_output=True,
            text=True,
            env=env,
        )
        self.assertIn("dingpan/tables", proc.stdout)
        got = subprocess.run(
            [sys.executable, str(CLI), "--root", str(SEED), "get", "dingpan/tools"],
            check=True,
            capture_output=True,
            text=True,
            env=env,
        )
        self.assertIn("<knowledge_body>", got.stdout)
        self.assertIn("listTables", got.stdout)

    def test_cli_refused_without_allow_env(self):
        env = os.environ.copy()
        env.pop("KB_ALLOW_CLI", None)
        proc = subprocess.run(
            [sys.executable, str(CLI), "--root", str(SEED), "get", "dingpan/tools"],
            capture_output=True,
            text=True,
            env=env,
        )
        self.assertEqual(proc.returncode, 2)
        self.assertIn("mcp_dingpan_knowledge_kb_catalog", proc.stderr)
        self.assertNotIn("<knowledge_body>", proc.stdout)

    def test_unknown_id_exits_2(self):
        proc = subprocess.run(
            [sys.executable, str(CLI), "--root", str(SEED), "get", "dingpan/nope"],
            capture_output=True,
            text=True,
            env=self._cli_env(),
        )
        self.assertEqual(proc.returncode, 2)
        self.assertIn("unknown", proc.stderr)

    def test_json_catalog(self):
        proc = subprocess.run(
            [sys.executable, str(CLI), "--root", str(SEED), "--json", "catalog"],
            check=True,
            capture_output=True,
            text=True,
            env=self._cli_env(),
        )
        payload = json.loads(proc.stdout)
        ids = [entry["id"] for entry in payload["entries"]]
        self.assertIn("dingpan/overview", ids)


class McpStdioTest(unittest.TestCase):
    def _rpc(self, proc, payload):
        proc.stdin.write(json.dumps(payload) + "\n")
        proc.stdin.flush()
        line = proc.stdout.readline()
        self.assertTrue(line, "MCP server closed stdout before reply")
        return json.loads(line)

    def test_initialize_catalog_and_get(self):
        proc = subprocess.Popen(
            [sys.executable, str(CLI), "--root", str(SEED), "mcp"],
            stdin=subprocess.PIPE,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            text=True,
        )
        try:
            init = self._rpc(
                proc,
                {
                    "jsonrpc": "2.0",
                    "id": 1,
                    "method": "initialize",
                    "params": {"protocolVersion": "2024-11-05", "capabilities": {}, "clientInfo": {"name": "test"}},
                },
            )
            self.assertEqual(init["result"]["serverInfo"]["name"], "dingpan-knowledge")
            listed = self._rpc(proc, {"jsonrpc": "2.0", "id": 2, "method": "tools/list", "params": {}})
            names = [tool["name"] for tool in listed["result"]["tools"]]
            self.assertEqual(names, ["kb_catalog", "kb_get"])
            catalog = self._rpc(
                proc,
                {
                    "jsonrpc": "2.0",
                    "id": 3,
                    "method": "tools/call",
                    "params": {"name": "kb_catalog", "arguments": {}},
                },
            )
            text = catalog["result"]["content"][0]["text"]
            self.assertIn("dingpan/tables", text)
            self.assertNotIn("<knowledge_body>", text)
            got = self._rpc(
                proc,
                {
                    "jsonrpc": "2.0",
                    "id": 4,
                    "method": "tools/call",
                    "params": {"name": "kb_get", "arguments": {"id": "dingpan/tools"}},
                },
            )
            body = got["result"]["content"][0]["text"]
            self.assertIn("<knowledge_body>", body)
            self.assertIn("listTables", body)
            bad = self._rpc(
                proc,
                {
                    "jsonrpc": "2.0",
                    "id": 5,
                    "method": "tools/call",
                    "params": {"name": "kb_get", "arguments": {"id": "dingpan/nope"}},
                },
            )
            self.assertTrue(bad["result"].get("isError"))
        finally:
            if proc.stdin:
                proc.stdin.close()
            proc.kill()
            proc.wait(timeout=5)
            if proc.stdout:
                proc.stdout.close()
            if proc.stderr:
                proc.stderr.close()


if __name__ == "__main__":
    unittest.main()
