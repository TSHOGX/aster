

/** Engines own rendering only: no project APIs, authentication or source fetching. */
export interface OfficeRenderer {
  render(bytes: ArrayBuffer): Promise<void>;
  dispose(): void;
  resize(): void;
  navigate?(index: number): void;
  slideCount?(): number;
  zoom?(value: number | null): number;
}

export type RendererFactory = (host: HTMLElement, onSlide: (index: number, count: number) => void) => OfficeRenderer;
