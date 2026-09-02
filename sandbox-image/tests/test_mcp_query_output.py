#!/usr/bin/env python3
"""Run the pi-mcp executeQuery output landing suite (Node)."""

import os
import subprocess
import unittest
from pathlib import Path

MCP = (
    Path(__file__).resolve().parents[1]
    / "files"
    / "usr"
    / "local"
    / "share"
    / "pi-extensions"
    / "mcp"
)


class QueryOutputNodeTest(unittest.TestCase):
    def test_query_output_suite(self):
        test_file = MCP / "tests" / "query-output.test.ts"
        self.assertTrue(test_file.is_file(), f"missing {test_file}")
        env = os.environ.copy()
        env["NODE_NO_WARNINGS"] = "1"
        proc = subprocess.run(
            [
                "node",
                "--experimental-strip-types",
                "--test",
                str(test_file),
            ],
            cwd=str(MCP),
            capture_output=True,
            text=True,
            env=env,
        )
        self.assertEqual(
            proc.returncode,
            0,
            proc.stdout + "\n" + proc.stderr,
        )


if __name__ == "__main__":
    unittest.main()
