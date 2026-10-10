import { type Page } from "@playwright/test";
import { ConversationFixture } from "./conversation-fixture";
export async function workspaceFixture(
  page: Page,
  options: { lostFirstReply?: boolean; many?: boolean; projectState?: string } = {},
) {
  const views = new Map<string, ConversationFixture>();
  await page.addInitScript(() => {
    let offline = false;
    let sequence = 0;
    const ready = new Set<EventTarget>();
    Object.defineProperty(window, "fixtureReadyResources", {
      get: () => ready.size,
    });
    window.addEventListener("fixture-file-event", (event) => {
      const value = (event as CustomEvent).detail;
      if (value === "connection-error") offline = true;
      if (value === "connection-open") offline = false;
    });
    // Deterministic UI fixtures keep streams open without testing SSE transport.
    window.EventSource = class extends EventTarget {
      static CONNECTING = 0;
      static OPEN = 1;
      static CLOSED = 2;
      readyState = 1;
      onopen: (() => void) | null = null;
      onerror: (() => void) | null = null;
      private rotation: ReturnType<typeof setTimeout> | undefined;
      private emit = (payload: Record<string, unknown>) => {
        if (payload.resource_kind === "ready") ready.add(this);
        this.dispatchEvent(
          new MessageEvent("resource", {
            data: JSON.stringify({
              protocol_version: 1,
              stream_incarnation: "fixture",
              cursor: String(++sequence),
              ...payload,
            }),
          }),
        );
      };
      private resourceEvent = (event: Event) => {
        const value = (event as CustomEvent).detail;
        if (value.transport === "rotate")
          this.dispatchEvent(new MessageEvent("rotate", { data: "{}" }));
        else this.emit(value);
      };
      private fileEvent = (event: Event) => {
        const payload = (event as CustomEvent).detail;
        if (payload === "connection-error") {
          this.onerror?.();
        } else if (payload === "connection-open") {
          if (!this.readyState) return;
          this.onopen?.();
          this.emit({ resource_kind: "ready" });
        } else this.emit({ resource_kind: "files", payload });
      };
      constructor(public url: string) {
        super();
        window.addEventListener("fixture-file-event", this.fileEvent);
        window.addEventListener("fixture-resource-event", this.resourceEvent);
        setTimeout(() => {
          if (offline) {
            this.onerror?.();
            return;
          }
          this.onopen?.();
          this.emit({ resource_kind: "ready" });
        }, 0);
        this.rotation = setTimeout(
          () => this.dispatchEvent(new MessageEvent("rotate", { data: "{}" })),
          25000,
        );
      }
      close() {
        ready.delete(this);
        clearTimeout(this.rotation);
        window.removeEventListener("fixture-file-event", this.fileEvent);
        window.removeEventListener(
          "fixture-resource-event",
          this.resourceEvent,
        );
        this.readyState = 2;
      }
    } as unknown as typeof EventSource;
  });
  const projects = [
    {
      id: "battery",
      title: "Battery research",
      state: options.projectState ?? "active",
      revision: 1,
      activity_revision: 1,
    },
    {
      id: "notes",
      title: "Research notes",
      state: "active",
      revision: 1,
      activity_revision: 1,
    },
  ];
  const sessions = [
    {
      id: "analysis",
      project_id: "battery",
      title: "Compare cycle performance",
      state: "idle",
      revision: 1,
      control_revision: 1,
      deletion: 0,
    },
    {
      id: "report",
      project_id: "battery",
      title: "Prepare the research report",
      state: "idle",
      revision: 1,
      control_revision: 1,
      deletion: 0,
    },
  ];
  if (options.many)
    for (let i = 0; i < 105; i++)
      sessions.push({
        ...sessions[0],
        id: `extra-${String(i).padStart(3, "0")}`,
        title: `Study ${i}`,
      });
  const messages: Record<
    string,
    {
      id: string;
      role: string;
      content: string;
      sequence: number;
      turn_id: string;
      created_at: string;
    }[]
  > = {
    analysis: [
      {
        id: "m1",
        role: "user",
        content:
          "Compare the cycle performance and summarize the results in a report.",
        sequence: 1,
        turn_id: "turn1",
        created_at: "2026-09-22",
      },
      {
        id: "m2",
        role: "assistant",
        content:
          "I reviewed the experiment data and checked the capacity measurements.\n\nThe cells retain **95% of their initial capacity** after 200 cycles. The results are consistent across the three samples, with a gradual change in resistance.\n\n### Findings\n\n- Capacity retention remains stable through the first 150 cycles.\n- The last 50 cycles show a small increase in internal resistance.\n- Temperature stayed within the expected range.\n\nThe full analysis is ready in [the research report](/workspace/reports/summary.md).",
        sequence: 2,
        turn_id: "turn1",
        created_at: "2026-09-22",
      },
    ],
    report: [
      {
        id: "r1",
        role: "assistant",
        content: "The report is ready for your review.",
        sequence: 1,
        turn_id: "turn2",
        created_at: "2026-09-22",
      },
    ],
  };
  const files: Record<string, string> = {
    "reports/summary.md":
      "# Cycle performance report\n\nA summary of the latest battery experiments.\n\n## Results\n\n| Sample | Initial capacity | Retention |\n| --- | --- | --- |\n| Cell A | 100 mAh | 95% |\n| Cell B | 101 mAh | 96% |\n\n## Observations\n\nThe cells show stable capacity retention after 200 cycles.\n\n```python\nretention = final_capacity / initial_capacity\n```",
    "README.md": "# Battery research\n\nExperiment notes and reports.",
  };
  const terminals: {
    id: string;
    project_id: string;
    title: string;
    state: string;
    revision: number;
    deletion: number;
    rows: number;
    cols: number;
    incarnation: string;
  }[] = [];
  const calls: {
    method: string;
    path: string;
    body: Record<string, unknown> | null;
  }[] = [];
  let lost = false;
  await page.routeWebSocket(/\/terminals\/.+\/attach/, (socket) => {
    socket.send(JSON.stringify({ kind: "status", input: true }));
    socket.send(Buffer.from("research % "));
    socket.onMessage((message) => {
      if (typeof message !== "string" || !message.startsWith("{"))
        socket.send(Buffer.from(String(message)));
    });
  });
  await page.route("**/api/**", async (route) => {
    const req = route.request(),
      url = new URL(req.url()),
      path = url.pathname,
      method = req.method();
    let body: Record<string, unknown> | null = null;
    try {
      body = req.postDataJSON();
    } catch {}
    calls.push({ method, path, body });
    const json = (data: unknown, status = 200) =>
      route.fulfill({
        status,
        contentType: "application/json",
        body: JSON.stringify(data),
      });
    if (path === "/api/auth/session")
      return json({ id: "researcher", username: "researcher", display_name: "researcher", status: "active", role: "user", revision: 1, session_revision: 1, must_change_password: false });
    if (path.endsWith("/events") || path.endsWith("/watch"))
      return route.fulfill({
        contentType: "text/event-stream",
        body: ": connected\n\n",
      });
    if (path === "/api/projects") return json({ projects });
    if (path === "/api/schedules/preview") {
      const body = route.request().postDataJSON();
      return json({ valid: true, ...body, reference_time: "2026-09-24T00:00:00Z", occurrences: body.cron ? ["2026-09-25T01:00:00Z", "2026-09-26T01:00:00Z", "2026-09-27T01:00:00Z"] : [] });
    }
    if (path === "/api/schedules")
      return json({
        schedules: [
          {
            id: "daily",
            project_id: "battery",
            project_title: "Battery research",
            title: "Daily experiment summary",
            prompt: "Summarize experiments",
            timezone: "Asia/Shanghai",
            cron: "0 9 * * *",
            state: "active", trigger_kind: "cron", notify: true,

            revision: 1,
            next_at: "2026-09-23T01:00:00Z",
          },
        ],
      });
    if (path === "/api/notifications") return json({ notifications: [] });
    if (path === "/api/conversations") return json({ conversations: [] });
    const parts = path.split("/").filter(Boolean),
      project = parts[2],
      kind = parts[3],
      id = parts[4],
      action = parts[5];
    if (parts.length === 3)
      return json(
        projects.find((p) => p.id === project) || {},
        projects.some((p) => p.id === project) ? 200 : 404,
      );
    if (kind === "sessions") {
      if (!id) {
        if (method === "POST") {
          const s = {
            ...sessions[0],
            id: String(body!.id),
            title: "New conversation",
            project_id: project,
          };
          sessions.push(s);
          return json(s, 201);
        }
        const list = sessions
          .filter(
            (s) =>
              s.project_id === project &&
              s.id > (url.searchParams.get("after") || ""),
          )
          .sort((a, b) => a.id.localeCompare(b.id));
        return json({ sessions: list.slice(0, 100) });
      }
      const s = sessions.find((s) => s.id === id && s.project_id === project);
      if (!s) return json({ error: "Not found" }, 404);
      if (!action) return json(s);
      if (action === "view") {
        let view = views.get(id);
        if (!view) {
          view = new ConversationFixture();
          views.set(id, view);
        }
        return json(
          view.reply(
            { session: s, messages: messages[id] || [], turns: [] },
            url,
          ),
        );
      }
      if (action === "turns") {
        if (method === "POST") {
          if (!messages[id]?.some((m) => m.id === body!.request_id)) {
            messages[id] = [
              ...(messages[id] || []),
              {
                id: String(body!.request_id),
                role: "user",
                content: String(body!.content),
                sequence: (messages[id]?.length || 0) + 1,
                turn_id: "new",
                created_at: "2026-09-22",
              },
            ];
          }
          if (options.lostFirstReply && !lost) {
            lost = true;
            return route.abort();
          }
          return json({ id: "new", state: "queued" }, 201);
        }
        return json({ turns: [] });
      }
      return json({});
    }
    if (kind === "terminals") {
      if (!id) {
        if (method === "POST") {
          const t = {
            id: String(body!.id),
            project_id: project,
            title: String(body!.title),
            state: "created",
            revision: 1,
            deletion: 0,
            rows: 24,
            cols: 80,
            incarnation: "first",
          };
          terminals.push(t);
          return json(t, 201);
        }
        return json({ terminals });
      }
      const t = terminals.find((t) => t.id === id);
      if (action === "start" && t) t.state = "running";
      return json(t || {});
    }
    if (kind === "files") {
      const p = url.searchParams.get("path") || "";
      if (id === "content") {
        if (method === "PUT") {
          files[p] = req.postData() || "";
          return json({ state: "succeeded" });
        }
        return route.fulfill({
          contentType: "application/octet-stream",
          body: files[p] || "",
        });
      }
      return json({
        entries:
          p === ""
            ? [
                { name: "reports", kind: "directory", size: 0 },
                { name: "README.md", kind: "file", size: 500 },
              ]
            : [{ name: "summary.md", kind: "file", size: 1000 }],
      });
    }
    return json({});
  });
  return { calls, sessions, messages, terminals, files };
}
