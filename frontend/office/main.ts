import type { RendererFactory, OfficeRenderer } from './adapters';
import { OFFICE_CHANNEL, OFFICE_LIMITS, validPosition, type OfficeKind, type ViewerLabels } from './protocol';
import './styles.css';

export function startViewer(format: OfficeKind, createRenderer: RendererFactory) {
  // Standalone document: no React hydration, application bootstrap or storage.
  const token = location.hash.slice(1);
  const host = document.getElementById('content')!;
  const stage = document.getElementById('stage')!;
  const navigation = document.getElementById('navigation')!;
  let renderer: OfficeRenderer | undefined;
  let started = false;
  let disposed = false;
  let slide = 0;
  let labels: ViewerLabels;
  const send = (kind: string, extra = {}) => parent.postMessage({ channel: OFFICE_CHANNEL, token, kind, ...extra }, '*');
  const position = () => send('position', { position: { top: host.scrollTop, left: host.scrollLeft, slide, focus: (document.activeElement as HTMLElement)?.dataset.previewFocus } });
  const fail = () => send('failed');
  function slideChanged(index: number, count: number) {
    slide = index;
    const input = navigation.querySelector('input')!;
    input.value = String(index + 1); input.max = String(count);
    navigation.querySelector('output')!.textContent = `/ ${count}`;
    navigation.querySelector<HTMLButtonElement>('[data-preview-focus=previous]')!.disabled = index === 0;
    navigation.querySelector<HTMLButtonElement>('[data-preview-focus=next]')!.disabled = index + 1 === count;
    navigation.hidden = false;
    position();
  }
  function setupNavigation() {
    navigation.setAttribute('aria-label', labels.slides);
    navigation.innerHTML = '<button type="button" data-preview-focus="previous">‹</button><form><input name="slide" type="number" min="1" data-preview-focus="slide"><output aria-live="polite"></output><button type="button"></button></form><button type="button" data-preview-focus="next">›</button>';
    const previous = navigation.querySelector<HTMLButtonElement>('[data-preview-focus=previous]')!;
    const next = navigation.querySelector<HTMLButtonElement>('[data-preview-focus=next]')!;
    previous.setAttribute('aria-label', labels.previous); previous.title = labels.previous;
    next.setAttribute('aria-label', labels.next); next.title = labels.next;
    navigation.querySelector('input')!.setAttribute('aria-label', labels.slide);
    navigation.querySelector('form button')!.textContent = labels.go;
    previous.onclick = () => { try { renderer?.navigate?.(slide - 1); } catch { fail(); } };
    next.onclick = () => { try { renderer?.navigate?.(slide + 1); } catch { fail(); } };
    const go = () => {
      const value = Number(navigation.querySelector('input')!.value);
      if (Number.isSafeInteger(value) && value > 0 && value <= (renderer?.slideCount?.() ?? 0)) {
        try { renderer?.navigate?.(value - 1); } catch { fail(); }
      }
    };
    navigation.querySelector<HTMLButtonElement>('form button')!.onclick = go;
    navigation.querySelector('input')!.onkeydown = e => { if (e.key === 'Enter') { e.preventDefault(); go(); } };
    navigation.querySelector('form')!.onsubmit = e => e.preventDefault();
  }
  function setupDocumentZoom() {
    let zoom = 1;
    const chinese = document.documentElement.lang === 'zh-CN';
    navigation.setAttribute('aria-label', chinese ? '文档缩放' : 'Document zoom');
    navigation.innerHTML = '<button type="button" data-preview-focus="zoom-out">−</button><output></output><button type="button" data-preview-focus="zoom-in">+</button><button type="button" data-preview-focus="fit"></button>';
    const output = navigation.querySelector('output')!;
    const set = (value: number | null) => { zoom = renderer?.zoom?.(value) ?? 1; output.textContent = `${Math.round(zoom * 100)}%`; };
    const minus = navigation.querySelector<HTMLButtonElement>('[data-preview-focus=zoom-out]')!;
    const plus = navigation.querySelector<HTMLButtonElement>('[data-preview-focus=zoom-in]')!;
    minus.setAttribute('aria-label', chinese ? '缩小' : 'Zoom out'); plus.setAttribute('aria-label', chinese ? '放大' : 'Zoom in');
    minus.onclick = () => set(Math.max(.25, zoom - .1)); plus.onclick = () => set(Math.min(2, zoom + .1));
    const fit = navigation.querySelector<HTMLButtonElement>('[data-preview-focus=fit]')!;
    fit.textContent = chinese ? '适合宽度' : 'Fit width'; fit.onclick = () => set(null);
    set(null); navigation.hidden = false;
  }
  window.addEventListener('message', async event => {
    if (event.source !== parent || event.origin !== location.origin || event.data?.channel !== OFFICE_CHANNEL || event.data.token !== token) return;
    const message = event.data;
    if (message.kind === 'render' && !started) {
      if (!(message.buffer instanceof ArrayBuffer) || message.buffer.byteLength > OFFICE_LIMITS.sourceBytes || message.format !== format) return;
      started = true; labels = message.labels;
      document.documentElement.lang = typeof message.locale === 'string' && /^zh(?:-|_|$)/i.test(message.locale) ? 'zh-Hans' : 'en';
      host.classList.add(message.format);
      try {
        send('stage', { stage: 'loading-engine' });
        setupNavigation();
        renderer = createRenderer(stage, slideChanged);
        if (disposed) { renderer.dispose(); return; }
        send('stage', { stage: 'rendering' });
        await renderer.render(message.buffer);
        if (message.format === 'document') setupDocumentZoom();
        if (!disposed) send('ready');
      } catch { if (!disposed) fail(); }
    } else if (message.kind === 'restore' && validPosition(message.position)) {
      host.scrollTop = message.position.top; host.scrollLeft = message.position.left;
      try { renderer?.navigate?.(message.position.slide); } catch { fail(); }
      if (message.position.focus) {
        const target = document.querySelector<HTMLElement>(`[data-preview-focus="${CSS.escape(message.position.focus)}"]`);
        (target && !target.matches(':disabled') ? target : host).focus({ preventScroll: true });
      }
    } else if (message.kind === 'theme' && ['dark','light'].includes(message.theme)) {
      document.documentElement.dataset.theme = message.theme;
    }
  });
  window.addEventListener('keydown', e => {
    if (e.key === 'Escape' && !e.defaultPrevented) send('escape');
    if (renderer?.navigate && !(e.target instanceof HTMLInputElement) && ['ArrowLeft','ArrowRight'].includes(e.key)) {
      e.preventDefault(); try { renderer.navigate(slide + (e.key === 'ArrowRight' ? 1 : -1)); } catch { fail(); }
    }
  });
  host.addEventListener('scroll', position, { passive: true });
  window.addEventListener('focusin', position);
  new ResizeObserver(() => renderer?.resize()).observe(host);
  window.addEventListener('pagehide', () => { disposed = true; renderer?.dispose(); });
  if (parent !== window && token) send('boot');

}
