import { projectURL } from "./project-api";
import { ResourceStore } from "./resource-store";

export type ResourceEvent = {
  protocol_version: number;
  stream_incarnation: string;
  cursor: string;
  resource_kind: string;
  resource_id?: string;
  revision?: string;
  reason?: string;
  payload?: unknown;
};
export type WatchHealth =
  "connecting" | "live" | "reconnecting" | "stale" | "closed";
type Entry = {
  project: string;
  listeners: Set<(event: ResourceEvent) => void>;
  health: Set<(health: WatchHealth) => void>;
  state: WatchHealth;
  source: EventSource | null;
  cursor: string;
  attempt: number;
  timer?: ReturnType<typeof setTimeout>;
  serial: number;
  dirtyHidden: boolean;
};

/** One shared transport per mounted Project. Mutations and PTYs bypass it. */
export class ProjectRealtime {
  private entries = new Map<string, Entry>();
  private listening = false;
  constructor(
    private resources: ResourceStore,
    private source = (url: string) => new EventSource(url),
  ) {}
  private visible = () => {
    for (const e of this.entries.values()) {
      if (typeof document !== "undefined" && document.hidden) {
        this.disconnect(e);
        this.setHealth(e, "stale");
        e.dirtyHidden = true;
      } else if (e.listeners.size) {
        // Buffered transport cursors alone cannot prove that a hidden view applied
        // invalidations. Returning tabs explicitly revalidate their visible scopes.
        if (e.dirtyHidden) {
          this.deliver(e, {
            protocol_version: 1,
            stream_incarnation: "",
            cursor: e.cursor,
            resource_kind: "resync",
            reason: "visibility",
          });
          e.dirtyHidden = false;
        }
        this.connect(e);
      }
    }
  };
  subscribe(
    project: string,
    listener: (event: ResourceEvent) => void,
    health?: (state: WatchHealth) => void,
  ) {
    let e = this.entries.get(project);
    if (!e) {
      e = {
        project,
        listeners: new Set(),
        health: new Set(),
        state: "connecting",
        source: null,
        cursor: "",
        attempt: 0,
        serial: 0,
        dirtyHidden: false,
      };
      this.entries.set(project, e);
    }
    e.listeners.add(listener);
    if (health) {
      e.health.add(health);
      health(e.state);
    }
    if (!this.listening && typeof document !== "undefined") {
      document.addEventListener("visibilitychange", this.visible);
      this.listening = true;
    }
    this.connect(e);
    const entry = e;
    queueMicrotask(() => {
      if (entry.listeners.has(listener))
        listener({
          protocol_version: 1,
          stream_incarnation: "",
          cursor: entry.cursor,
          resource_kind: "resync",
          reason: "consumer_attach",
        });
    });
    return () => {
      entry.listeners.delete(listener);
      if (health) entry.health.delete(health);
      if (!entry.listeners.size) {
        this.disconnect(entry);
        this.setHealth(entry, "closed");
      }
      if (this.entries.size > 32) {
        for (const [key, value] of this.entries)
          if (!value.listeners.size) {
            this.entries.delete(key);
            if (this.entries.size <= 32) break;
          }
      }
    };
  }
  clear() {
    for (const entry of this.entries.values()) this.disconnect(entry);
    this.entries.clear();
    if (this.listening) {
      document.removeEventListener("visibilitychange", this.visible);
      this.listening = false;
    }
  }
  private disconnect(e: Entry) {
    e.serial++;
    e.source?.close();
    e.source = null;
    clearTimeout(e.timer);
    e.timer = undefined;
  }
  private setHealth(e: Entry, state: WatchHealth) {
    e.state = state;
    for (const listener of e.health) listener(state);
  }
  private deliver(e: Entry, event: ResourceEvent) {
    for (const listener of e.listeners) listener(event);
  }
  private connect(e: Entry) {
    if (
      e.source ||
      e.timer ||
      !e.listeners.size ||
      (typeof document !== "undefined" && document.hidden)
    )
      return;
    const serial = ++e.serial,
      source = this.source(
        `${projectURL(e.project)}/resources/events?cursor=${encodeURIComponent(e.cursor)}`,
      );
    e.source = source;
    this.setHealth(e, e.cursor ? "reconnecting" : "connecting");
    const retry = (planned = false) => {
      if (serial !== e.serial) return;
      this.disconnect(e);
      if (!e.listeners.size) return;
      if (!planned) this.setHealth(e, "reconnecting");
      const delay = planned
        ? 50
        : Math.min(15000, 250 * 2 ** Math.min(6, e.attempt++)) *
          (0.75 + Math.random() / 2);
      e.timer = setTimeout(() => {
        e.timer = undefined;
        this.connect(e);
      }, delay);
    };
    source.addEventListener("resource", (message) => {
      if (serial !== e.serial) return;
      try {
        const event = JSON.parse(
          (message as MessageEvent).data,
        ) as ResourceEvent;
        if (event.protocol_version !== 1 || typeof event.cursor !== "string")
          throw new Error("Unsupported resource stream");
        if (event.resource_kind === "closed") {
          this.resources.clear(e.project);
          this.deliver(e, event);
          this.disconnect(e);
          this.setHealth(e, "closed");
          return;
        }
        if (
          event.cursor === e.cursor &&
          event.resource_kind !== "ready" &&
          event.resource_kind !== "resync"
        )
          return;
        this.deliver(e, event); // Accept into each domain before advancing our cursor.
        e.cursor = event.cursor;
        if (event.resource_kind === "ready") {
          e.attempt = 0;
          this.setHealth(e, "live");
        }
      } catch {
        e.cursor = "";
        retry();
      }
    });
    source.addEventListener("rotate", () => retry(true));
    source.addEventListener("closed", (message) => {
      if (serial !== e.serial) return;
      let reason = "authorization";
      try {
        reason = JSON.parse((message as MessageEvent).data).reason;
      } catch {}
      this.disconnect(e);
      this.resources.clear(e.project);
      this.setHealth(e, "closed");
      this.deliver(e, {
        protocol_version: 1,
        stream_incarnation: "",
        cursor: e.cursor,
        resource_kind: "closed",
        reason,
      });
      if (reason === "authentication" && typeof window !== "undefined")
        window.dispatchEvent(new Event("project-auth-expired"));
    });
    source.onerror = () => retry();
  }
}
