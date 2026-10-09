import { SaxesParser } from "saxes";
import { PREVIEW_LIMITS as LIMIT, PreviewFailure } from "./preview-budget";

export type PreviewPlan = {
  renderBytes: number;
  expandedBytes: number;
  nodes: number;
  pixels: number;
  cells: number;
  rows: number;
  slides: number;
};
function requireLimit(valid: boolean): asserts valid {
  if (!valid) throw new PreviewFailure("container_invalid", "container");
}
function requireCapacity(actual: number, limit: number, metric: "bytes" | "entries" | "nodes" | "cells" | "rows" | "columns" | "slides" | "pixels") {
  if (actual > limit) throw new PreviewFailure("content_limit", "parse", { actual, limit, metric });
}
const decoder = new TextDecoder("utf-8", { fatal: true });
function decodeXML(bytes: Uint8Array) {
  const encoding = bytes[0] === 0xff && bytes[1] === 0xfe ? "utf-16le"
    : bytes[0] === 0xfe && bytes[1] === 0xff ? "utf-16be" : "utf-8";
  return new TextDecoder(encoding, { fatal: true }).decode(bytes);
}
const sheet = "http://schemas.openxmlformats.org/spreadsheetml/2006/main";

/** Read dimensions without letting the browser allocate decoded pixel buffers. */
export function imagePixels(bytes: Uint8Array): number {
  const view = new DataView(bytes.buffer, bytes.byteOffset, bytes.byteLength);
  let width = 0,
    height = 0;
  if (bytes.length >= 14 && ["GIF87a", "GIF89a"].includes(new TextDecoder().decode(bytes.subarray(0, 6)))) {
    width = view.getUint16(6, true);
    height = view.getUint16(8, true);
    const skipBlocks = (from: number) => {
      let offset = from;
      for (;;) {
        requireLimit(offset < bytes.length);
        const length = bytes[offset++];
        if (!length) return offset;
        requireLimit(offset + length <= bytes.length);
        offset += length;
      }
    };
    let offset = 13 + (bytes[10] & 0x80 ? 3 * (1 << ((bytes[10] & 7) + 1)) : 0);
    requireLimit(offset < bytes.length);
    let frames = 0, trailer = false;
    while (offset < bytes.length) {
      const marker = bytes[offset++];
      if (marker === 0x3b) { trailer = true; break; }
      if (marker === 0x21) {
        requireLimit(offset < bytes.length);
        offset = skipBlocks(offset + 1);
      } else if (marker === 0x2c) {
        requireLimit(offset + 9 <= bytes.length);
        requireLimit(
          view.getUint16(offset, true) + view.getUint16(offset + 4, true) <= width &&
          view.getUint16(offset + 2, true) + view.getUint16(offset + 6, true) <= height &&
          view.getUint16(offset + 4, true) > 0 && view.getUint16(offset + 6, true) > 0,
        );
        const flags = bytes[offset + 8];
        offset += 9 + (flags & 0x80 ? 3 * (1 << ((flags & 7) + 1)) : 0);
        requireLimit(offset < bytes.length);
        offset = skipBlocks(offset + 1); // LZW code size, then bounded subblocks.
        frames++;
      } else requireLimit(false);
    }
    requireLimit(trailer && offset === bytes.length && frames > 0);
    if (frames > 1) throw new PreviewFailure("unsupported_feature", "parse");
  } else if (
    bytes.length >= 33 &&
    view.getUint32(0) === 0x89504e47 &&
    view.getUint32(4) === 0x0d0a1a0a &&
    view.getUint32(12) === 0x49484452
  ) {
    width = view.getUint32(16);
    height = view.getUint32(20);
    // Animated PNG adds a separate unbounded frame allocation dimension.
    let offset = 8;
    while (offset + 12 <= bytes.length) {
      const size = view.getUint32(offset);
      requireLimit(
        offset + size + 12 <= bytes.length &&
          view.getUint32(offset + 4) !== 0x6163544c,
      );
      offset += size + 12;
    }
    requireLimit(offset === bytes.length);
  } else if (bytes.length >= 4 && view.getUint16(0) === 0xffd8) {
    let offset = 2;
    while (offset + 4 <= bytes.length) {
      requireLimit(bytes[offset++] === 0xff);
      while (bytes[offset] === 0xff) offset++;
      const marker = bytes[offset++];
      if (marker === 0xda || marker === 0xd9) break;
      const size = view.getUint16(offset);
      requireLimit(size >= 2 && offset + size <= bytes.length);
      if ([0xc0, 0xc1, 0xc2].includes(marker)) {
        requireLimit(size >= 8);
        height = view.getUint16(offset + 3);
        width = view.getUint16(offset + 5);
      }
      offset += size;
    }
  }
  requireLimit(width > 0 && height > 0);
  requireCapacity(width * height, LIMIT.imagePixels, "pixels");
  return width * height;
}

type Entry = {
  name: string;
  size: number;
  compressed: number;
  method: number;
  start: number;
  end: number;
  crc: number;
};
/** Reject ambiguous ZIP containers before any third-party ZIP parser sees them. */
function zipEntries(bytes: Uint8Array): Entry[] {
  const view = new DataView(bytes.buffer, bytes.byteOffset, bytes.byteLength);
  let end = bytes.length - 22;
  while (
    end >= Math.max(0, bytes.length - 65557) &&
    view.getUint32(end, true) !== 0x06054b50
  )
    end--;
  requireLimit(
    end >= 0 &&
      view.getUint32(end, true) === 0x06054b50 &&
      end + 22 + view.getUint16(end + 20, true) === bytes.length,
  );
  const count = view.getUint16(end + 10, true),
    start = view.getUint32(end + 16, true);
  requireLimit(
    view.getUint32(end + 4, true) === 0 &&
      view.getUint16(end + 8, true) === count &&
      count > 0 &&
      start + view.getUint32(end + 12, true) === end,
  );
  requireCapacity(count, LIMIT.entries, "entries");
  const entries: Entry[] = [],
    names = new Set<string>();
  let offset = start,
    expanded = 0;
  for (let i = 0; i < count; i++) {
    requireLimit(
      offset + 46 <= end && view.getUint32(offset, true) === 0x02014b50,
    );
    const flags = view.getUint16(offset + 8, true),
      method = view.getUint16(offset + 10, true);
    const crc = view.getUint32(offset + 16, true),
      compressed = view.getUint32(offset + 20, true),
      size = view.getUint32(offset + 24, true);
    const nameLength = view.getUint16(offset + 28, true),
      local = view.getUint32(offset + 42, true);
    const next =
      offset +
      46 +
      nameLength +
      view.getUint16(offset + 30, true) +
      view.getUint16(offset + 32, true);
    requireLimit(
      next <= end &&
        (flags & ~(method === 8 ? 0x80e : 0x808)) === 0 &&
        [0, 8].includes(method) &&
        local + 30 <= start,
    );
    requireCapacity(size, LIMIT.entryBytes, "bytes");
    const name = decoder.decode(
      bytes.subarray(offset + 46, offset + 46 + nameLength),
    );
    requireLimit(
      name.length > 0 &&
        !names.has(name.toLowerCase()) &&
        !/\\|(^|\/)\.\.?($|\/)|^\//.test(name),
    );
    names.add(name.toLowerCase());
    requireLimit(
      view.getUint32(local, true) === 0x04034b50 &&
        view.getUint16(local + 6, true) === flags &&
        view.getUint16(local + 8, true) === method,
    );
    const localName = view.getUint16(local + 26, true);
    const data = local + 30 + localName + view.getUint16(local + 28, true);
    requireLimit(
      data <= start &&
        data + compressed <= start &&
        decoder.decode(bytes.subarray(local + 30, local + 30 + localName)) ===
          name,
    );
    if (!(flags & 8))
      requireLimit(
        view.getUint32(local + 14, true) === crc &&
          view.getUint32(local + 18, true) === compressed &&
          view.getUint32(local + 22, true) === size,
      );
    expanded += size;
    requireCapacity(expanded, LIMIT.expandedBytes, "bytes");
    entries.push({
      name,
      size,
      compressed,
      method,
      start: data,
      end: data + compressed,
      crc,
    });
    offset = next;
  }
  requireLimit(offset === end);
  const spans = [...entries].sort((a, b) => a.start - b.start);
  for (let i = 1; i < spans.length; i++)
    requireLimit(spans[i].start >= spans[i - 1].end);
  return entries;
}

const crcTable = new Uint32Array(256).map((_, n) => {
  for (let i = 0; i < 8; i++) n = (n >>> 1) ^ (n & 1 ? 0xedb88320 : 0);
  return n >>> 0;
});
async function expand(bytes: Uint8Array, entry: Entry, signal: AbortSignal) {
  signal.throwIfAborted();
  const input = bytes.slice(entry.start, entry.end);
  const stream = new Blob([input]).stream();
  const reader = (
    entry.method === 8
      ? stream.pipeThrough(new DecompressionStream("deflate-raw"))
      : stream
  ).getReader();
  const cancel = () => {
    void reader.cancel().catch(() => {});
  };
  signal.addEventListener("abort", cancel, { once: true });
  const output = new Uint8Array(entry.size);
  let length = 0,
    crc = 0xffffffff;
  try {
    for (;;) {
      signal.throwIfAborted();
      const part = await reader.read();
      if (part.done) break;
      requireLimit(length + part.value.length <= output.length);
      output.set(part.value, length);
      length += part.value.length;
      for (const b of part.value) crc = (crc >>> 8) ^ crcTable[(crc ^ b) & 255];
    }
    signal.throwIfAborted();
    requireLimit(
      length === entry.size && (crc ^ 0xffffffff) >>> 0 === entry.crc,
    );
    return output;
  } finally {
    signal.removeEventListener("abort", cancel);
    await reader.cancel().catch(() => {});
    reader.releaseLock();
  }
}

/** Bounded decompression precedes DOM adapters. Unsupported legacy formats fail closed. */
export async function preflightPreview(
  blob: Blob,
  path: string,
  signal: AbortSignal,
  nested = false,
): Promise<PreviewPlan> {
  if (blob.size > LIMIT.blobBytes)
    throw new PreviewFailure("source_too_large", "download", { actual: blob.size, limit: LIMIT.blobBytes, metric: "bytes" });
  return preflightBytes(
    new Uint8Array(await blob.arrayBuffer()),
    path,
    signal,
    nested,
  );
}
export async function preflightBytes(
  bytes: Uint8Array,
  path: string,
  signal: AbortSignal,
  nested = false,
): Promise<PreviewPlan> {
  if (bytes.length > LIMIT.blobBytes)
    throw new PreviewFailure("source_too_large", "download", { actual: bytes.length, limit: LIMIT.blobBytes, metric: "bytes" });
  signal.throwIfAborted();
  const ext = path.toLowerCase().split(".").pop();
  if (ext === "pdf") {
    // Rendering belongs to the browser's PDF viewer, never an HTML frame parser.
    requireLimit(/^%PDF-\d\.\d/.test(new TextDecoder().decode(bytes.subarray(0, 8))));
    return { renderBytes: 0, expandedBytes: 0, nodes: 0, pixels: 0, cells: 0, rows: 0, slides: 0 };
  }
  if (["png", "jpg", "jpeg", "gif"].includes(ext || "")) {
    const pixels = imagePixels(bytes);
    return {
      renderBytes: pixels * 4,
      expandedBytes: 0,
      nodes: 0,
      pixels,
      cells: 0,
      rows: 0,
      slides: 0,
    };
  }
  if (ext !== "pptx" && !(nested && ext === "xlsx"))
    throw new PreviewFailure("unsupported_feature", "parse");
  const entries = zipEntries(bytes);
  requireLimit(
    entries.some(
      (e) =>
        e.name ===
        (ext === "xlsx"
          ? "xl/workbook.xml"
          : "ppt/presentation.xml"),
    ),
  );
  let nodes = 0,
    pixels = 0,
    expandedBytes = 0,
    cells = 0,
    rows = 0;
  requireCapacity(entries.filter((e) => /^xl\/worksheets\/[^/]+\.xml$/.test(e.name)).length, LIMIT.sheets, "entries");
  const slides = entries.filter((e) => /^ppt\/slides\/slide\d+\.xml$/.test(e.name)).length;
  for (const entry of entries) {
    const data = await expand(bytes, entry, signal);
    expandedBytes += data.length;
    if (entry.name.endsWith("/")) {
      requireLimit(!data.length);
      continue;
    }
    if (/\.(xml|rels)$/.test(entry.name)) {
      const xml = decodeXML(data);
      requireLimit(!/<!DOCTYPE|<!ENTITY/i.test(xml));
      const parser = new SaxesParser({ xmlns: true });
      let depth = 0;
      parser.on("error", () => {
        throw new PreviewFailure("container_invalid", "parse");
      });
      parser.on("doctype", () => {
        throw new PreviewFailure("container_invalid", "parse");
      });
      parser.on("opentag", (element) => {
        requireCapacity(++nodes, LIMIT.xmlNodes, "nodes");
        requireLimit(++depth <= LIMIT.xmlDepth);
        const tag = element.local;
        const attr = (name: string) => element.attributes[name]?.value || "";
        if (element.uri === sheet) {
          if (tag === "c") requireCapacity(++cells, LIMIT.cells, "cells");
          if (tag === "row") {
            requireCapacity(++rows, LIMIT.rows, "rows");
            requireCapacity(Number(attr("r") || 0), LIMIT.rows, "rows");
          }
          if (tag === "col") {
            requireLimit(Number(attr("min")) >= 1 && Number.isSafeInteger(Number(attr("max"))) &&
              Number(attr("max")) >= Number(attr("min")) && Number(attr("width") || 0) <= 256);
            requireCapacity(Number(attr("max")), LIMIT.columns, "columns");
          }
          if (
            tag === "c" ||
            tag === "dimension" ||
            tag === "mergeCell" ||
            (tag === "f" && attr("ref"))
          ) {
            const refs = attr(tag === "c" ? "r" : "ref");
            for (const ref of refs.split(":")) {
              const match = /^([A-Z]+)(\d+)$/.exec(ref);
              requireLimit(!!match);
              let column = 0;
              for (const char of match[1])
                column = column * 26 + char.charCodeAt(0) - 64;
              requireCapacity(column, LIMIT.columns, "columns");
              requireCapacity(Number(match[2]), LIMIT.rows, "rows");
              requireCapacity(column * Number(match[2]), LIMIT.cells, "cells");
            }
          }
        }
        if (tag === "Relationship")
          requireLimit(
            attr("TargetMode") !== "External" ||
              /\/hyperlink$/.test(attr("Type")),
          );
      });
      parser.on("closetag", () => {
        depth--;
      });
      parser.write(xml).close();
    } else if (
      /^(ppt|xl)\/printerSettings\/printerSettings\d+\.bin$/.test(entry.name)
    ) {
      // Bounded opaque printer metadata is not used by the preview libraries.
      continue;
    } else if (!nested && /\/embeddings\/[^/]+\.xlsx$/.test(entry.name)) {
      const embedded = await preflightBytes(data, entry.name, signal, true);
      nodes += embedded.nodes;
      pixels += embedded.pixels;
      expandedBytes += embedded.expandedBytes;
      cells += embedded.cells;
      rows += embedded.rows;
      requireLimit(cells <= LIMIT.cells && rows <= LIMIT.rows);
      requireLimit(
        nodes <= LIMIT.xmlNodes &&
          pixels <= LIMIT.totalPixels &&
          expandedBytes <= LIMIT.expandedBytes,
      );
    } else if (/\/media\//.test(entry.name)) {
      pixels += imagePixels(data);
      requireLimit(pixels <= LIMIT.totalPixels);
    } else {
      // Thumbnails are decoded only after the same pixel checks. Nested archives,
      // OLE objects, macros and fonts need a separately qualified adapter.
      requireLimit(/\.(png|jpe?g|gif)$/.test(entry.name));
      pixels += imagePixels(data);
      requireLimit(pixels <= LIMIT.totalPixels);
    }
  }
  const renderBytes =
    expandedBytes * 4 + nodes * 128 + cells * 256 + pixels * 4;
  requireCapacity(renderBytes, LIMIT.renderBytes, "bytes");
  return { renderBytes, expandedBytes, nodes, pixels, cells, rows, slides };
}
