/** Versioned transport between the application and its authority-free viewer. */
export const OFFICE_CHANNEL = 'office-visual-v2';
export type OfficeKind = 'document' | 'spreadsheet' | 'presentation';
export type OfficeStage = 'loading-engine' | 'rendering';
export type OfficePosition = { top: number; left: number; slide: number; focus?: string };
export const OFFICE_LIMITS = {
  sourceBytes: 100 * 1024 * 1024,
  entryBytes: 64 * 1024 * 1024,
  expandedBytes: 256 * 1024 * 1024,
  entries: 4096,
  xmlNodes: 4_000_000,
  cells: 1_000_000,
  renderBytes: 384 * 1024 * 1024,
  inspectMs: 60_000,
  bootMs: 30_000,
  renderMs: 180_000,
} as const;
export const officeKind = (path: string): OfficeKind | null => {
  switch (path.toLowerCase().split('.').pop()) {
    case 'docx': return 'document';
    case 'xlsx': return 'spreadsheet';
    case 'pptx': return 'presentation';
    default: return null;
  }
};
export function validPosition(value: unknown): value is OfficePosition {
  if (!value || typeof value !== 'object') return false;
  const p = value as OfficePosition;
  return [p.top, p.left].every(n => Number.isFinite(n) && n >= 0 && n <= 100_000_000) &&
    Number.isInteger(p.slide) && p.slide >= 0 && p.slide < 10000 &&
    (p.focus === undefined || typeof p.focus === 'string' && p.focus.length < 128);
}
export type ViewerLabels = { previous: string; next: string; slide: string; slides: string; go: string };
