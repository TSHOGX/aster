import { DatabaseSync } from "node:sqlite";
import { resolve } from "node:path";
import type { Route } from "@playwright/test";
import type {
  ReadingJob,
  ReadingManifest,
  ReadingItem,
} from "../../lib/office-reading";

export class ReadingFixture {
  private sequence = 0;
  private jobs = new Map<
    string,
    { name: string; db: DatabaseSync; manifest: ReadingManifest }
  >();
  readonly names = new Map<string, string>();
  async reply(route: Route) {
    const url = new URL(route.request().url());
    if (!url.pathname.includes("/files/previews")) return false;
    const suffix = url.pathname.split("/files/previews")[1];
    const id = suffix.split("/")[1];
    const json = (data: unknown, status = 200) =>
      route.fulfill({ status, json: data });
    if (!id) {
      const path = url.searchParams.get("path") || "";
      const name = this.names.get(path) ?? path.split("/").pop()!;
      try {
        const db = new DatabaseSync(
          resolve(process.env.PREVIEW_FIXTURES!, `${name}.model/model.sqlite`),
          { readOnly: true },
        );
        const manifest = JSON.parse(
          db.prepare("SELECT data FROM metadata").get()!.data as string,
        ) as ReadingManifest;
        const lease = `reading-${++this.sequence}`;
        this.jobs.set(lease, { name, db, manifest });
        await json({
          id: lease,
          digest: name,
          state: "ready",
          manifest,
        } satisfies ReadingJob);
      } catch {
        await json(
          { code: "container_invalid", error: "container_invalid" },
          422,
        );
      }
    } else {
      const job = this.jobs.get(id);
      if (!job) {
        await json(
          { code: "snapshot_expired", error: "snapshot_expired" },
          410,
        );
        return true;
      }
      if (route.request().method() === "DELETE") {
        job.db.close();
        this.jobs.delete(id);
        await route.fulfill({ status: 204 });
        return true;
      }
      const sheet = Number(url.searchParams.get("sheet") || 0),
        start = Number(url.searchParams.get("start") || 0);
      if (suffix.endsWith("/window")) {
        const items = job.db
          .prepare(
            "SELECT data FROM items WHERE sheet=? AND position>=? ORDER BY position LIMIT 100",
          )
          .all(sheet, start)
          .map((row) => JSON.parse(row.data as string) as ReadingItem);
        const from = Number(url.searchParams.get("from_column") ?? -1),
          columns = Number(url.searchParams.get("columns") ?? 0);
        if (from >= 0)
          for (const item of items)
            if (item.cells)
              item.cells = item.cells.filter(
                (cell) => cell.column >= from && cell.column < from + columns,
              );
        await json({
          digest: job.name,
          start,
          total: job.manifest.sheets[sheet].count,
          coverage: "complete",
          items,
        });
      } else if (suffix.endsWith("/find")) {
        const row = job.db
          .prepare(
            "SELECT position FROM items WHERE sheet=? AND position>=? AND instr(lower(text),lower(?))>0 ORDER BY position LIMIT 1",
          )
          .get(sheet, start, url.searchParams.get("q"));
        await json({ position: row?.position ?? -1 });
      } else if (suffix.endsWith("/copy")) {
        const end = Number(url.searchParams.get("end")),
          from = Number(url.searchParams.get("from_column")),
          to = Number(url.searchParams.get("to_column"));
        const items = job.db
          .prepare(
            "SELECT data FROM items WHERE sheet=? AND position BETWEEN ? AND ? ORDER BY position",
          )
          .all(sheet, start, end)
          .map((row) => JSON.parse(row.data as string) as ReadingItem);
        await json({
          text: items
            .map((item) =>
              item.cells
                ? Array.from(
                    { length: to - from + 1 },
                    (_, col) =>
                      item.cells!.find((cell) => cell.column === col + from)
                        ?.text ?? "",
                  ).join("\t")
                : (item.text ?? ""),
            )
            .join("\n"),
        });
      } else if (suffix.endsWith("/locate")) {
        const row = job.db
          .prepare(
            "SELECT position FROM items WHERE sheet=? AND row>=? ORDER BY row LIMIT 1",
          )
          .get(sheet, Number(url.searchParams.get("row")));
        await json({ position: row?.position ?? -1 });
      } else
        await json({
          id,
          digest: job.name,
          state: "ready",
          manifest: job.manifest,
        });
    }
    return true;
  }
  close() {
    for (const job of this.jobs.values()) job.db.close();
    this.jobs.clear();
  }
}
