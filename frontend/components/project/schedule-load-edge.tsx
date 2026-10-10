"use client";
import { useEffect, useRef } from "react";
// The button is the same demand as the automatic observer; it remains reachable
// by keyboard and serves as a fallback when observers are unavailable.
export function ScheduleLoadEdge({
  enabled,
  busy,
  onLoad,
  label,
}: {
  enabled: boolean;
  busy: boolean;
  onLoad: () => void;
  label: string;
}) {
  const edge = useRef<HTMLDivElement>(null);
  const load = useRef(onLoad);
  useEffect(() => {
    load.current = onLoad;
  }, [onLoad]);
  useEffect(() => {
    if (!enabled || busy || !edge.current) return;
    const observer = new IntersectionObserver(
      (entries) => {
        if (entries.some((entry) => entry.isIntersecting)) load.current();
      },
      { root: edge.current.parentElement, rootMargin: "200px" },
    );
    observer.observe(edge.current);
    return () => observer.disconnect();
  }, [enabled, busy]);
  return enabled ? (
    <div ref={edge} className="schedule-load-edge">
      <button disabled={busy} onClick={onLoad}>
        {label}
      </button>
    </div>
  ) : null;
}
