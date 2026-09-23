import { reservePreview } from "./preview-budget";
import type { PreviewPlan } from "./preview-preflight";

/** Immutable bytes own their URL until the last renderer releases its lease. */
export class PreviewRepresentation {
  readonly url: string;
  private refs = 1;
  private readonly releaseBudget: () => void;
  constructor(
    readonly blob: Blob,
    readonly digest: string,
    readonly text: string,
    readonly path: string,
    mime: string,
    readonly plan?: PreviewPlan,
    readonly textReadable = false,
  ) {
    this.releaseBudget = reservePreview(
      blob.size +
        text.length * 2 +
        (mime.startsWith("image/") ? (plan?.renderBytes ?? 0) : 0),
      "urls",
    );
    try {
      this.url = URL.createObjectURL(new Blob([blob], { type: mime }));
    } catch (error) {
      this.releaseBudget();
      throw error;
    }
  }
  retain() {
    if (!this.refs) throw new Error("Preview already released");
    this.refs++;
    let released = false;
    return () => {
      if (!released) {
        released = true;
        this.release();
      }
    };
  }
  release() {
    if (this.refs > 0 && --this.refs === 0) {
      URL.revokeObjectURL(this.url);
      this.releaseBudget();
    }
  }
}
export async function contentDigest(blob: Blob) {
  const release = reservePreview(blob.size);
  try {
    const digest = await crypto.subtle.digest(
      "SHA-256",
      await blob.arrayBuffer(),
    );
    return Array.from(new Uint8Array(digest), (b) =>
      b.toString(16).padStart(2, "0"),
    ).join("");
  } finally {
    release();
  }
}
