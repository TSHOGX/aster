import { init } from 'pptx-preview';
import type { RendererFactory } from './adapters';

export const createRenderer: RendererFactory = (host, onSlide) => {
  const view = init(host, { width: 960, height: 540, mode: 'slide' });
  let current = 0;
  const resize = () => {
    view.wrapper.style.zoom = String(Math.max(1, host.clientWidth) / 960);
  };
  const navigate = (index: number) => {
    current = Math.max(0, Math.min(view.slideCount - 1, index));
    view.renderSingleSlide(current);
    onSlide(current, view.slideCount);
  };
  return {
    async render(bytes) {
      await view.load(bytes);
      const height = view.htmlRender.renderPort.height;
      if (!Number.isFinite(height) || height < 100 || height > 5000) throw new Error('aspect-ratio');
      view.htmlRender.options.viewPort!.height = height;
      view.wrapper.style.height = `${height}px`;
      navigate(0); resize();
      await new Promise<void>(resolve => setTimeout(resolve, 0));
    },
    resize, navigate, slideCount: () => view.slideCount,
    dispose() { view.destroy(); host.replaceChildren(); },
  };
};
