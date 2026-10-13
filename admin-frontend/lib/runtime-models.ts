export type Artifact = { id: string; version: string; digest: string };
export type RequiredPublication = {
  role: string;
  destination: string;
  artifact_id: string;
};
export type Mount = {
  key: string;
  role: string;
  destination: string;
  storage: "owned" | "shared_readonly" | "shared";
  access: "read_write" | "read_only";
  source: {
    kind: "empty" | "builtin" | "shared";
    shared_volume_id?: string;
    artifact_id?: string;
    version?: string;
    digest?: string;
  };
  capacity: { setting?: "workspace_bytes" | "runtime_bytes"; bytes?: string };
};
export type EnvironmentBody = { sandbox_release: Artifact; mounts: Mount[] };
export type RuntimeOption = {
 library_name?: string; version_label?: string; version_note?: string;
  kind: "sandbox" | "volume";
  reference: Artifact;
  enabled: boolean;
  availability: string;
  checked_at?: string;
  required_publications?: RequiredPublication[];
  protected_roots?: string[];
  blocked_path_count?: number;
  roles?: string[];
  minimum_bytes?: string;
  source_capacity_bytes?: string;
  clean_runtime?: boolean;
};
export type OptionsPage = {
  options: RuntimeOption[];
  max_mounts: string;
  next_cursor?: string;
};
export const artifactKey = (value: Artifact) =>
  JSON.stringify([value.id, value.version, value.digest]);

export function environmentForRelease(
  release: RuntimeOption,
  sources: RuntimeOption[],
  previous?: EnvironmentBody,
): EnvironmentBody {
  const core: Mount[] = previous?.mounts.filter((m) =>
    ["workspace", "runtime", "data"].includes(m.role),
  ) || [
    {
      key: "workspace",
      role: "workspace",
      destination: "/workspace",
      storage: "owned",
      access: "read_write",
      source: { kind: "empty" },
      capacity: { setting: "workspace_bytes" },
    },
    {
      key: "runtime",
      role: "runtime",
      destination: "/state",
      storage: "owned",
      access: "read_write",
      source: { kind: "empty" },
      capacity: { setting: "runtime_bytes" },
    },
  ];
  return {
    sandbox_release: release.reference,
    mounts: [
      ...core,
      ...(release.required_publications || []).map((p) => {
        const old = previous?.mounts.find((m) => m.role === p.role);
        const selected =
          sources.find(
            (s) =>
              old?.source.artifact_id === s.reference.id &&
              old.source.version === s.reference.version &&
              old.source.digest === s.reference.digest &&
              s.enabled &&
              s.reference.id === p.artifact_id &&
              s.roles?.includes(p.role),
          ) ||
          sources.find(
            (s) =>
              s.reference.id === p.artifact_id &&
              s.roles?.includes(p.role) &&
              s.enabled &&
              ![
                "retired",
                "other_store",
                "incompatible",
                "unavailable",
              ].includes(s.availability),
          );
        return {
          key: old?.key || p.role,
          role: p.role,
          destination: p.destination,
          storage: "shared_readonly" as const,
          access: "read_only" as const,
          capacity: {},
          source: {
            kind: "builtin" as const,
            artifact_id: p.artifact_id,
            version: selected?.reference.version,
            digest: selected?.reference.digest,
          },
        };
      }),
    ],
  };
}
