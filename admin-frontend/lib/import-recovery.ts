export type ImportAction = "admit" | "retry" | "cancel" | "retire";
export type ImportCommand = { account: string; action: ImportAction; imported: string; body: string };
export const importEndpoint = (command: ImportCommand) => command.action === "admit" ? "/api/admin/volume-imports" : `/api/admin/volume-imports/${encodeURIComponent(command.imported)}/${command.action}`;
export function readImportCommand(raw: string, account: string): ImportCommand {
  if (raw.length > 8192) throw new Error("oversized recovery");
  const command = JSON.parse(raw) as ImportCommand;
  const id = (value: unknown) => typeof value === "string" && /^[A-Za-z0-9_-]{1,128}$/.test(value);
  if (command?.account !== account || !["admit", "retry", "cancel", "retire"].includes(command.action) || typeof command.body !== "string") throw new Error("invalid recovery scope");
  const body = JSON.parse(command.body);
  if (!body || !id(body.request_id)) throw new Error("invalid request");
  if (command.action === "admit") {
    if (command.imported !== "" || !id(body.source_id) || !id(body.version_id) || typeof body.expected_manifest_digest !== "string" || !/^sha256:[a-f0-9]{64}$/.test(body.expected_manifest_digest) || !["authored-v1", "shared-assets-v1"].includes(body.consumer_profile)) throw new Error("invalid admission");
  } else if (!id(command.imported) || Object.keys(body).some(key => key !== "request_id")) throw new Error("invalid lifecycle action");
  return {account, action:command.action, imported:command.imported, body:command.body};
}
