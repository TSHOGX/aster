// Lazy-load the spreadsheet renderer and stylesheet together.
import "@js-preview/excel/lib/index.css";
import jsPreviewExcel from "@js-preview/excel";

type Registration = [string, EventListenerOrEventListenerObject, boolean | AddEventListenerOptions | undefined];

const spreadsheet = {
  init(...args: Parameters<typeof jsPreviewExcel.init>) {
    // The renderer registers global resize/keyboard/clipboard handlers during
    // synchronous initialization, but its destroy method leaves them behind.
    // Capture that bounded registration phase so detached previews release them.
    const original = window.addEventListener;
    const registrations: Registration[] = [];
    const detach = () => {
      for (const [type, listener, options] of registrations)
        window.removeEventListener(type, listener, options);
      registrations.length = 0;
    };
    window.addEventListener = ((type: string, listener: EventListenerOrEventListenerObject, options?: boolean | AddEventListenerOptions) => {
      registrations.push([type, listener, options]);
      original.call(window, type, listener, options);
    }) as typeof window.addEventListener;
    try {
      const view = jsPreviewExcel.init(...args);
      return {
        preview: view.preview.bind(view),
        setOptions: view.setOptions.bind(view),
        destroy() { detach(); view.destroy(); },
      };
    } catch (error) {
      detach();
      throw error;
    } finally {
      window.addEventListener = original;
    }
  },
};

export default spreadsheet;
