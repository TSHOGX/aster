import { expect, it } from "vitest";
import { environmentForRelease, type RuntimeOption } from "./runtime-models";
const release: RuntimeOption = {
  kind: "sandbox",
  reference: { id: "runtime", version: "v1", digest: "digest" },
  enabled: true,
  availability: "available",
  required_publications: [
    { role: "agent_config", destination: "/opt/config", artifact_id: "config" },
  ],
};
const source: RuntimeOption = {
  kind: "volume",
  reference: { id: "config", version: "v1", digest: "source" },
  enabled: true,
  availability: "available",
  roles: ["agent_config"],
};
it("creates an environment from exact current inventory without a default document", () => {
  const env = environmentForRelease(release, [source]);
  expect(env.mounts.map((m) => m.role)).toEqual([
    "workspace",
    "runtime",
    "agent_config",
  ]);
  expect(env.mounts[2]).toMatchObject({
    destination: "/opt/config",
    storage: "shared_readonly",
    access: "read_only",
    source: { artifact_id: "config", version: "v1", digest: "source" },
  });
});
it("preserves user storage and exact required sources while honoring a new release's destinations", () => {
  const before = environmentForRelease(release, [source]);
  before.mounts[0].destination = "/work";
  const next = environmentForRelease(
    {
      ...release,
      required_publications: [
        {
          role: "agent_config",
          destination: "/opt/new-config",
          artifact_id: "config",
        },
      ],
    },
    [{ ...source, reference: { ...source.reference, version: "v2" } }, source],
    before,
  );
  expect(next.mounts[0].destination).toBe("/work");
  expect(next.mounts[2].source.version).toBe("v1");
  expect(next.mounts[2].destination).toBe("/opt/new-config");
});
it("leaves unavailable sources unselected instead of inventing a usable reference", () => {
  expect(
    environmentForRelease(release, [{ ...source, availability: "other_store" }])
      .mounts[2].source.version,
  ).toBeUndefined();
});
