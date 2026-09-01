// Pure grouping logic (no Raycast imports — testable with plain node).

export interface Service {
  name: string;
  root: string;
  state: "running" | "stopped";
  pid: number;
  url: string;
  log: string;
  key?: string;
  open?: boolean;
  host?: string; // ssh host the service runs on (from `dx status --host`); absent = local
}

export type EnvState = "running" | "partial" | "stopped";

// Env is one checkout (primary or worktree) with all its services.
export interface Env {
  root: string;
  host?: string;
  services: Service[];
  openSvc: Service;
  state: EnvState;
}

// groupByRoot folds the flat service list into one Env per (host, checkout root).
// openSvc: the service with open=true, else the single service, else the
// alphabetically-first name (covers registry records that predate the flag).
export function groupByRoot(services: Service[]): Env[] {
  const byRoot = new Map<string, Service[]>();
  for (const svc of services) {
    const key = `${svc.host ?? ""}\x00${svc.root}`;
    const list = byRoot.get(key) ?? [];
    list.push(svc);
    byRoot.set(key, list);
  }
  const envs: Env[] = [];
  for (const list of byRoot.values()) {
    const sorted = [...list].sort((a, b) => a.name.localeCompare(b.name));
    const openSvc = sorted.find((s) => s.open) ?? sorted[0];
    const running = sorted.filter((s) => s.state === "running").length;
    const state: EnvState = running === 0 ? "stopped" : running === sorted.length ? "running" : "partial";
    envs.push({ root: sorted[0].root, host: sorted[0].host, services: sorted, openSvc, state });
  }
  // local first, then by host, then by name
  return envs.sort(
    (a, b) => (a.host ?? "").localeCompare(b.host ?? "") || a.openSvc.name.localeCompare(b.openSvc.name),
  );
}
