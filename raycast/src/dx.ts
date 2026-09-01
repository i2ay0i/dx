import { execFile } from "node:child_process";
import { promisify } from "node:util";
import { homedir } from "node:os";
import { getPreferenceValues } from "@raycast/api";
import { Service } from "./group";

export { groupByRoot } from "./group";
export type { Service, Env, EnvState } from "./group";

const pExecFile = promisify(execFile);

function dxBin(): string {
  const { dxPath } = getPreferenceValues<{ dxPath?: string }>();
  const raw = (dxPath && dxPath.trim()) || "~/.local/bin/dx";
  return raw.startsWith("~") ? raw.replace(/^~/, homedir()) : raw;
}

// remoteHosts preference: comma/space separated ssh hosts (e.g. "nano").
function remoteHosts(): string[] {
  const { remoteHosts } = getPreferenceValues<{ remoteHosts?: string }>();
  return (remoteHosts ?? "")
    .split(/[,\s]+/)
    .map((h) => h.trim())
    .filter(Boolean);
}

export interface StatusResult {
  services: Service[];
  // dx's stderr — e.g. "warning: host nano: ..." when a remote host is unreachable.
  warning?: string;
}

export async function listServices(): Promise<StatusResult> {
  const args = ["status", "--all", "--json"];
  for (const h of remoteHosts()) args.push("--host", h);
  const { stdout, stderr } = await pExecFile(dxBin(), args);
  const parsed = JSON.parse(stdout || "[]");
  return {
    services: Array.isArray(parsed) ? (parsed as Service[]) : [],
    warning: stderr.trim() || undefined,
  };
}

export async function stopService(name: string): Promise<void> {
  await pExecFile(dxBin(), ["stop", name]);
}
