export type RevertCommand = { library: string; body: string };
export function readRevertCommand(raw: string, library: string): RevertCommand {
  if (raw.length > 8192) throw new Error("oversized recovery");
  const command = JSON.parse(raw) as RevertCommand;
  if (command?.library !== library || typeof command.body !== "string") throw new Error("invalid recovery scope");
  const body = JSON.parse(command.body);
  if (!body || typeof body.request_id !== "string" || !/^[A-Za-z0-9_-]{1,128}$/.test(body.request_id) || typeof body.name !== "string" || !body.name.trim() || body.name.trim() !== body.name || body.name.length > 128 || body.name === "main" || typeof body.commit !== "string" || !/^[a-f0-9]{64}$/.test(body.commit) || typeof body.parent !== "string" || !/^[a-f0-9]{64}$/.test(body.parent)) throw new Error("invalid revert request");
  return { library, body: command.body };
}
