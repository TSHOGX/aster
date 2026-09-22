import { expect, it } from "vitest";
import { workspaceLinkPlugin, workspacePath } from "./workspace-links";
it("links absolute paths in prose and inline code without changing fences or existing links", () => {
  const tree = {
    type: "root",
    children: [
      {
        type: "paragraph",
        children: [
          { type: "text", value: "保存到 /workspace/heart.png。" },
          { type: "inlineCode", value: "/workspace/heart.py" },
          {
            type: "link",
            url: "/workspace/a.md",
            children: [{ type: "text", value: "/workspace/a.md" }],
          },
        ],
      },
      { type: "code", value: "cat /workspace/heart.py" },
    ],
  };
  workspaceLinkPlugin()(tree);
  expect(tree.children[0].children?.map((n) => n.type)).toEqual([
    "text",
    "link",
    "text",
    "link",
    "link",
  ]);
  expect(tree.children[1].type).toBe("code");
});
it("resolves a unique referenced basename but rejects ambiguous or escaping paths", () => {
  expect(workspacePath("heart.png", ["heart.png"])).toBe("heart.png");
  expect(workspacePath("x.py", ["a/x.py", "b/x.py"])).toBeNull();
  expect(workspacePath("/workspace/../secret")).toBeNull();
  expect(workspacePath("https://example.com")).toBeNull();
});
