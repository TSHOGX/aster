import { describe, expect, test } from "bun:test";
import { canonicalScope, conceptOf } from "./concepts.ts";

describe("concept tagging", () => {
  test("bilingual paraphrase maps to one concept", () => {
    // This equivalence is why clustering is done by an agent over a whole block
    // instead of by a similarity cutoff — and why SCORING must not be, since a
    // model grading itself would decide this case in its own favour.
    expect(conceptOf("current_curve is double-encoded JSON")).toBe(
      conceptOf("充电曲线字段需要二次解析")
    );
  });

  test("unrelated statements do not collide", () => {
    expect(conceptOf("compute group permission denied")).not.toBe(conceptOf("工况码是有限枚举"));
  });

  test("free-text scopes collapse to a canonical label", () => {
    expect(canonicalScope("vhr_data", "soc 采样")).toBe("battery");
    expect(canonicalScope("doris/bms", "表结构")).toBe("battery");
    expect(canonicalScope("数据源选择", "doris 查询")).toBe("doris");
    expect(canonicalScope("", "工况码枚举")).toBe("work_condition");
  });
});
