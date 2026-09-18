"use client";
import { useEffect, useRef, useState } from "react";
import {
  Virtuoso,
  type VirtuosoHandle,
  type StateSnapshot,
} from "react-virtuoso";

const rows = (start: number, count: number) =>
  Array.from({ length: count }, (_, i) => ({
    id: start + i,
    text: "Variable height content. ".repeat(1 + (i % 12)),
  }));
export function VirtualFixture() {
  const [items, setItems] = useState(() => rows(100, 100));
  const [wide, setWide] = useState(true);
  const [mounted, setMounted] = useState(true);
  const [snapshot, setSnapshot] = useState<StateSnapshot>();
  const ref = useRef<VirtuosoHandle>(null);
  const host = useRef<HTMLDivElement>(null);
  const anchor = useRef<{ id: number; y: number } | null>(null);
  useEffect(() => {
    const saved = anchor.current;
    if (!saved || !mounted) return;
    const index = items.findIndex((row) => row.id === saved.id);
    if (index < 0) return;
    ref.current?.scrollToIndex({ index, align: "start" });
    let frame = 0;
    let remaining = 20;
    const settle = () => {
      const row = host.current?.querySelector(`[data-row="${saved.id}"]`);
      if (row)
        ref.current?.scrollBy({
          top: row.getBoundingClientRect().top - saved.y,
        });
      else ref.current?.scrollToIndex({ index, align: "start" });
      if (--remaining > 0) frame = requestAnimationFrame(settle);
      else anchor.current = null;
    };
    frame = requestAnimationFrame(settle);
    return () => cancelAnimationFrame(frame);
  }, [items, mounted]);
  const capture = () => {
    const rect = host.current?.getBoundingClientRect();
    const row = [
      ...(host.current?.querySelectorAll<HTMLElement>("[data-row]") ?? []),
    ].find((el) => el.getBoundingClientRect().bottom > (rect?.top ?? 0));
    if (row)
      anchor.current = {
        id: Number(row.dataset.row),
        y: row.getBoundingClientRect().top,
      };
  };
  const prepend = () => {
    capture();
    setItems((v) => [...rows(v[0].id - 40, 40), ...v]);
  };
  return (
    <main>
      <button onClick={prepend}>Prepend</button>
      <button
        onClick={() =>
          setItems((v) => [
            ...v.slice(0, -1),
            {
              ...v[v.length - 1],
              text: v[v.length - 1].text + " streaming".repeat(30),
            },
          ])
        }
      >
        Stream
      </button>
      <button
        onClick={() =>
          ref.current?.scrollToIndex({ index: 50, align: "center" })
        }
      >
        Jump
      </button>
      <button onClick={() => setWide((v) => !v)}>Resize</button>
      <button
        onClick={() => {
          if (mounted) {
            capture();
            ref.current?.getState((s) => {
              setSnapshot(s);
              setMounted(false);
            });
          } else setMounted(true);
        }}
      >
        Toggle
      </button>
      <div ref={host} style={{ width: wide ? 700 : 400, height: 600 }}>
        {mounted && (
          <Virtuoso
            ref={ref}
            data={items}
            style={{ overflowAnchor: "none" }}
            initialTopMostItemIndex={
              snapshot ? undefined : { index: 99, align: "end" }
            }
            restoreStateFrom={snapshot}
            computeItemKey={(_, row) => row.id}
            followOutput="auto"
            increaseViewportBy={300}
            itemContent={(_, row) => (
              <div data-row={row.id} style={{ padding: 12, minHeight: 48 }}>
                {row.text}
                <details>
                  <summary>Trace</summary>
                  {"Expanded detail. ".repeat(60)}
                </details>
              </div>
            )}
          />
        )}
      </div>
    </main>
  );
}
