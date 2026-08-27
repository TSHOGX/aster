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

Use listBusinessTables, getTableSchemas, executeQuery.
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
            self.assertNotIn("listBusinessTables", catalog)
            body = kb.render_knowledge_content(articles[0])
            self.assertIn('<knowledge_content id="dingpan/tools">', body)
            self.assertIn("listBusinessTables", body)


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


class CliTest(unittest.TestCase):
    def test_catalog_and_get_roundtrip(self):
        env = os.environ.copy()
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
        self.assertIn("listBusinessTables", got.stdout)

    def test_unknown_id_exits_2(self):
        proc = subprocess.run(
            [sys.executable, str(CLI), "--root", str(SEED), "get", "dingpan/nope"],
            capture_output=True,
            text=True,
        )
        self.assertEqual(proc.returncode, 2)
        self.assertIn("unknown", proc.stderr)

    def test_json_catalog(self):
        proc = subprocess.run(
            [sys.executable, str(CLI), "--root", str(SEED), "--json", "catalog"],
            check=True,
            capture_output=True,
            text=True,
        )
        payload = json.loads(proc.stdout)
        ids = [entry["id"] for entry in payload["entries"]]
        self.assertIn("dingpan/overview", ids)


if __name__ == "__main__":
    unittest.main()
