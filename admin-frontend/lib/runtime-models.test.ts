import { describe, expect, it } from "vitest";
import { mergeEnvironment, type EnvironmentBody, type Mount } from "./runtime-models";
const data = (key: string): Mount => ({ key, role: "data", destination: `/data/${key}`, storage: "owned", access: "read_write", source: { kind: "empty" }, capacity: { bytes: "9007199254740993" } });
const reference = { id: "runtime", version: "v1", digest: "digest" };
describe("runtime reload merge", () => {
  it("preserves edited fields, server additions and exact byte strings", () => {
    const base: EnvironmentBody = { sandbox_release: reference, mounts: [data("first"), data("removed")] };
    const draft = { ...base, mounts: [{ ...data("first"), destination: "/changed" }, data("local")] };
    const remote = { ...base, sandbox_release: { ...reference, version: "v2" }, mounts: [{ ...data("first"), access: "read_only" as const }, data("removed"), data("remote")] };
    const merged = mergeEnvironment(base, draft, remote);
    expect(merged.sandbox_release.version).toBe("v2");
    expect(merged.mounts.map(mount => mount.key)).toEqual(["first", "remote", "local"]);
    expect(merged.mounts[0]).toMatchObject({ destination: "/changed", access: "read_only", capacity: { bytes: "9007199254740993" } });
  });
  it("honors a remote deletion unless the user edited that slot", () => {
    const base = { sandbox_release: reference, mounts: [data("first"), data("second")] };
    const draft = { ...base, mounts: [data("first"), { ...data("second"), access: "read_only" as const }] };
    expect(mergeEnvironment(base, draft, { ...base, mounts: [] }).mounts.map(mount => mount.key)).toEqual(["second"]);
  });
});
