// Lazy chunk boundary for the spreadsheet viewer. Bundling the library and its
// stylesheet here (statically) means a single dynamic import() of this module
// pulls both, and only when a spreadsheet is actually opened — the excel viewer
// code + CSS never touch the initial bundle or other Office kinds' chunks.
import "@js-preview/excel/lib/index.css";
import jsPreviewExcel from "@js-preview/excel";

export default jsPreviewExcel;
