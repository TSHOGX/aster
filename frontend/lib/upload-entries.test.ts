import { describe, expect, it } from "vitest";

import { entriesFromFileList, isSessionWritable } from "./upload-entries";
import type { SessionStatus } from "./types";

describe("isSessionWritable", () => {
  it("allows uploads for idle/created/checkpointed sessions", () => {
    for (const status of ["created", "running_idle", "checkpointed"] as SessionStatus[]) {
      expect(isSessionWritable(status)).toBe(true);
    }
  });

  it("blocks uploads while a turn is busy or the session is gone", () => {
    for (const status of [
      "running_active",
      "checkpointing",
      "failed",
      "destroyed"
    ] as SessionStatus[]) {
      expect(isSessionWritable(status)).toBe(false);
    }
    expect(isSessionWritable(null)).toBe(false);
    expect(isSessionWritable(undefined)).toBe(false);
  });
});

describe("entriesFromFileList", () => {
  it("uses webkitRelativePath when present, else the file name", () => {
    const flat = new File(["a"], "notes.txt");
    const nested = Object.assign(new File(["b"], "a.ts"), {
      webkitRelativePath: "src/utils/a.ts"
    });
    const entries = entriesFromFileList([flat, nested]);
    expect(entries).toHaveLength(2);
    expect(entries[0].relPath).toBe("notes.txt");
    expect(entries[1].relPath).toBe("src/utils/a.ts");
    expect(entries[1].file).toBe(nested);
  });
});
