const labels: Record<string,string> = {
  projects:"Project count", running_projects:"Running Projects", cpu_millis:"CPU per Project (millicores)", memory_bytes:"Memory per Project (bytes)", overlay_bytes:"Overlay per Project (bytes)", workspace_bytes:"Workspace capacity (bytes)", runtime_bytes:"Runtime capacity (bytes)", aggregate_cpu_millis:"Total CPU (millicores)", aggregate_memory_bytes:"Total memory (bytes)", owned_volume_bytes:"Owned Volume capacity (bytes)", project_owned_volume_bytes:"Project storage (bytes)", mount_count:"Mount count", persistent_bytes:"Persistent storage (bytes)", agents:"Concurrent Agents", terminals:"Concurrent Terminals", schedules:"Active schedules", transfer_bytes:"File transfer (bytes)",
};
export function resourceQuotaMessage(value: unknown): string {
  if(!value || typeof value!=="object")return "Resource allowance exceeded.";
  const quota=value as Record<string,unknown>;
  if(typeof quota.dimension!=="string" || !labels[quota.dimension])return "Resource allowance exceeded.";
  const quantity=(field:string)=>typeof quota[field]==="string"&&/^\d{1,19}$/.test(quota[field] as string)?new Intl.NumberFormat().format(BigInt(quota[field] as string)):"unavailable";
  return `${labels[quota.dimension]} allowance exceeded. Limit: ${quantity("limit")}; used: ${quantity("used")}; reserved: ${quantity("reserved")}; requested: ${quantity("requested_delta")}.`;
}
