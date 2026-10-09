"use client";
import { Component, createRef, useEffect, useRef, type ReactNode } from "react";

type Row = { id: string; node: ReactNode };
type Props = { rows: Row[]; estimate: number };
type State = {
  top: number;
  height: number;
  sizes: Map<string, number>;
  focused?: string;
};
type Anchor = { id: string; offset: number } | null;

/** Variable-height rows, bounded by the owning five-page window. Capturing the
 * visible row before a commit preserves reading position after prepend,
 * eviction, background replacement and row measurement. */
export class ScheduleViewport extends Component<Props, State> {
  state: State = { top: 0, height: 800, sizes: new Map() };
  host = createRef<HTMLDivElement>();
  scroll: HTMLElement | null = null;
  observer?: ResizeObserver;
  positions(props = this.props, state = this.state) {
    let at = 0;
    return props.rows.map((row) => {
      const top = at;
      at += state.sizes.get(row.id) || props.estimate;
      return { ...row, top, height: at - top };
    });
  }
  read = () => {
    if (!this.host.current || !this.scroll) return;
    const top =
      this.scroll.getBoundingClientRect().top -
      this.host.current.getBoundingClientRect().top;
    this.setState({ top: Math.max(0, top), height: this.scroll.clientHeight });
  };
  componentDidMount() {
    this.scroll = this.host.current?.parentElement || null;
    this.scroll?.addEventListener("scroll", this.read, { passive: true });
    this.observer = new ResizeObserver(this.read);
    if (this.scroll) this.observer.observe(this.scroll);
    this.read();
  }
  componentWillUnmount() {
    this.scroll?.removeEventListener("scroll", this.read);
    this.observer?.disconnect();
  }
  getSnapshotBeforeUpdate(previous: Props, state: State): Anchor {
    if (
      !this.scroll ||
      !this.host.current ||
      (previous.rows === this.props.rows && state.sizes === this.state.sizes)
    )
      return null;
    const local =
      this.scroll.getBoundingClientRect().top -
      this.host.current.getBoundingClientRect().top;
    const row = this.positions(previous, state).find(
      (row) => row.top + row.height > local,
    );
    return row ? { id: row.id, offset: row.top - local } : null;
  }
  componentDidUpdate(_props: Props, _state: State, anchor: Anchor) {
    if (anchor && this.scroll && this.host.current) {
      const row = this.positions().find((row) => row.id === anchor.id);
      if (row) {
        const local =
          this.scroll.getBoundingClientRect().top -
          this.host.current.getBoundingClientRect().top;
        const delta = row.top - local - anchor.offset;
        if (Math.abs(delta) > 1) {
          this.scroll.scrollTop += delta;
          this.read();
        }
      }
    }
  }
  measure = (id: string, size: number) => {
    if (Math.abs((this.state.sizes.get(id) || this.props.estimate) - size) < 1)
      return;
    this.setState((state) => {
      const sizes = new Map(
        [...state.sizes].filter(([key]) =>
          this.props.rows.some((row) => row.id === key),
        ),
      );
      sizes.set(id, size);
      return { sizes };
    });
  };
  render() {
    const rows = this.positions();
    const total = rows.at(-1);
    const virtual = rows.length > 50;
    return (
      <div
        className="schedule-viewport"
        ref={this.host}
        style={
          virtual
            ? {
                height: (total?.top || 0) + (total?.height || 0),
                position: "relative",
              }
            : undefined
        }
      >
        {rows
          .filter(
            (row) =>
              !virtual ||
              row.id === this.state.focused ||
              (row.top + row.height >= this.state.top - 400 &&
                row.top <= this.state.top + this.state.height + 400),
          )
          .map((row) => (
            <MeasuredRow
              key={row.id}
              id={row.id}
              measure={this.measure}
              style={
                virtual
                  ? { position: "absolute", top: row.top, left: 0, right: 0 }
                  : undefined
              }
              onFocus={() => this.setState({ focused: row.id })}
              onBlur={() => this.setState({ focused: undefined })}
            >
              {row.node}
            </MeasuredRow>
          ))}
      </div>
    );
  }
}
function MeasuredRow({
  id,
  measure,
  style,
  children,
  onFocus,
  onBlur,
}: {
  id: string;
  measure: (id: string, size: number) => void;
  style?: React.CSSProperties;
  children: ReactNode;
  onFocus: () => void;
  onBlur: () => void;
}) {
  const ref = useRef<HTMLDivElement>(null);
  useEffect(() => {
    const observer = new ResizeObserver(() => {
      if (ref.current) measure(id, ref.current.getBoundingClientRect().height);
    });
    if (ref.current) observer.observe(ref.current);
    return () => observer.disconnect();
  }, [id, measure]);
  return (
    <div
      ref={ref}
      data-schedule-row={id}
      style={style}
      onFocus={onFocus}
      onBlur={onBlur}
    >
      {children}
    </div>
  );
}
