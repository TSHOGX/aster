import { renderAsync } from 'docx-preview';
import type { RendererFactory } from './adapters';

export const createRenderer: RendererFactory = host => {
  let wrapper: HTMLElement | null = null;
  let naturalWidth = 816;
  let manualZoom: number | null = null;
  const resize = () => {
    if (!wrapper) return;
    const ratio = manualZoom ?? Math.min(1, Math.max(0.2, (host.clientWidth - 16) / (naturalWidth + 48)));
    wrapper.style.width = `${naturalWidth + 48}px`;
    wrapper.style.margin = '0 auto';
    wrapper.style.zoom = String(ratio);
  };
  return {
    async render(bytes) {
      await renderAsync(bytes, host, undefined, { useBase64URL: true, renderAltChunks: false, ignoreLastRenderedPageBreak: true });
      wrapper = host.querySelector<HTMLElement>('.docx-wrapper');
      for (const section of host.querySelectorAll<HTMLElement>('section.docx')) naturalWidth = Math.max(naturalWidth, section.offsetWidth);
      resize();
    },
    resize,
    zoom(value) { manualZoom = value; resize(); return Number(wrapper?.style.zoom || 1); },
    dispose() { host.replaceChildren(); },
  };
};
