import excel from '@js-preview/excel';
import '@js-preview/excel/lib/index.css';
import type { RendererFactory } from './adapters';

export const createRenderer: RendererFactory = host => {
  const view = excel.init(host, { minColLength: 10, minRowLength: 20, showContextmenu: false });
  return {
    // The public API accepts bytes, but this pinned engine internally reads
    // a blob URL. CSP permits only blob: connections, never HTTP or app APIs.
    async render(bytes) { await view.preview(bytes); },
    resize() { window.dispatchEvent(new Event('resize')); },
    dispose() { view.destroy(); host.replaceChildren(); },
  };
};
