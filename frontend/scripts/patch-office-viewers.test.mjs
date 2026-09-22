import { expect, it } from "vitest";
import { readFileSync } from "node:fs";
import { runInNewContext } from "node:vm";
import { patchPresentationSource } from "./patch-office-viewers.mjs";

it("releases both chart listener lists when advancing or destroying a presentation", () => {
  const source = patchPresentationSource(
    readFileSync(
      new URL(
        "../node_modules/pptx-preview/dist/pptx-preview.es.js",
        import.meta.url,
      ),
      "utf8",
    ),
  );
  const bus = source.slice(
    source.indexOf("var yt={};"),
    source.indexOf("var mt=function"),
  );
  const method = (name) => {
    const start =
      source.indexOf(`prototype.${name}=function(){`) +
      `prototype.${name}=`.length;
    return source.slice(start, source.indexOf("},", start) + 1);
  };
  // Execute the actual pinned library's event ownership methods, without DOM or
  // chart rendering. This regression fails on the upstream no-argument off bug.
  const api = runInNewContext(
    `${bus}; ({on:vt, emit:wt, off:bt, advance:${method("removeCurrentSlide")}, destroy:${method("destroy")}, count:()=>Object.values(yt).reduce((n,a)=>n+a.length,0)})`,
  );
  let calls = 0;
  const view = { wrapper: { querySelector: () => null }, currentIndex: 0 };
  for (let i = 0; i < 100; i++) {
    api.on("removeSlide", () => calls++);
    api.on("destroy", () => calls++);
    api.advance.call(view);
    expect(api.count()).toBe(0);
  }
  expect(calls).toBe(100);
  api.on("destroy", () => calls++);
  api.on("removeSlide", () => calls++);
  api.destroy.call(view);
  expect(api.count()).toBe(0);
  expect(calls).toBe(101);
  expect(patchPresentationSource(source)).toBe(source);
  expect(() => patchPresentationSource(source + "changed")).toThrow(
    "Unreviewed",
  );
});
