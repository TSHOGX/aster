"use client";
import { useEffect, useId, useMemo, useState } from "react";
import { api, type SchedulePreview } from "@/lib/project-api";
import { useLocale, useT } from "@/lib/i18n";
import { Globe2, Repeat2 } from "lucide-react";
import { timezoneOptions } from "@/lib/timezone-options";
import {
  parseRecurrence,
 recurrenceRunAt,
 resolveLocalScheduleTime,
  recurrenceCron,
  repeatLabels,
  scheduleTime,
  schedulePreviewKey,
  weekdayLabels,
  type Recurrence,
  type RepeatMode,
} from "@/lib/schedule-recurrence";
import { useDialog } from "./ui";
import { SelectPicker } from "./select-picker";
import { TimePicker } from "./time-picker";

export function RecurrenceSummary({
  cron,
  timezone,
 runAt,
}: {
  cron: string;
  timezone: string;
 runAt?: string;
}) {
  const tr = useT();
  const rule = parseRecurrence(cron,runAt,timezone);
 const locale=useLocale();
 if(runAt) return <span>{tr("Once")} · {scheduleTime(runAt,timezone,locale)}</span>;
  return (
    <span>
      {rule.mode === "interval"
        ? tr("Every {minutes} minutes", { minutes: rule.intervalMinutes })
        : tr(repeatLabels[rule.mode])}
      {rule.mode === "weekly"
        ? ` · ${rule.weekdays.map((n) => tr(weekdayLabels[n])).join(", ")}`
        : ""}
      {["monthly", "dates"].includes(rule.mode)
        ? ` · ${rule.dates.join(", ")}`
        : ""}
      {!["manual", "advanced", "interval", "once"].includes(rule.mode)
        ? ` · ${rule.time}`
        : ""}
      {rule.mode === "advanced" ? ` · ${cron}` : ""} · {timezone}
    </span>
  );
}

export function UpcomingOccurrences({
  cron,
  timezone,
  enabled,
  nextAt,
}: {
  cron: string;
  timezone: string;
  enabled: boolean;
  nextAt?: string;
}) {
  const tr = useT(),
    locale = useLocale();
  const [opened, setOpened] = useState(false),
    [attempt, setAttempt] = useState(0);
  const [data, setData] = useState<{
    key: string;
    value?: SchedulePreview;
    failed?: boolean;
  }>();
  const key = JSON.stringify([cron, timezone, nextAt, attempt]);
  useEffect(() => {
    if (!opened || !cron) return;
    const controller = new AbortController();
    void api<SchedulePreview>("/api/schedules/preview", {
      method: "POST",
      body: JSON.stringify({
        cron,
        timezone,
        next_at: enabled ? nextAt : undefined,
      }),
      signal: controller.signal,
    })
      .then((value) => {
        if (!controller.signal.aborted) setData({ key, value });
      })
      .catch(() => {
        if (!controller.signal.aborted) setData({ key, failed: true });
      });
    return () => controller.abort();
  }, [opened, cron, timezone, nextAt, enabled, key]);
  if (!cron) return null;
  const current = data?.key === key ? data : undefined;
  return (
    <div>
      <button aria-expanded={opened} onClick={() => setOpened((v) => !v)}>
        {tr("Upcoming occurrences")}
      </button>
      {opened && (
        <div aria-live="polite">
          {!enabled && <p>{tr("Expected times after resuming")}</p>}
          {!current && <p>{tr("Loading…")}</p>}
          {(current?.failed || current?.value?.valid === false) && (
            <p>
              {tr("Could not confirm upcoming times. Retry before saving.")}{" "}
              <button onClick={() => setAttempt((n) => n + 1)}>
                {tr("Retry")}
              </button>
            </p>
          )}
          {current?.value?.valid && (
            <ol>
              {current.value.occurrences.map((time) => (
                <li key={time}>{scheduleTime(time, timezone, locale)}</li>
              ))}
            </ol>
          )}
          <small>
            {tr(
              "Missed occurrences are coalesced into one run. These are planned times, not guaranteed start times.",
            )}
          </small>
        </div>
      )}
    </div>
  );
}

export function RecurrenceFields({
  rule,
  cron,
  timezone,
  nextAt,
  onRule,
  onTimezone,
  onValidity,
  disabled = false,
}: {
  rule: Recurrence;
  cron: string | null;
  timezone: string;
  nextAt?: string;
  onRule: (r: Recurrence) => void;
  onTimezone: (s: string) => void;
  onValidity: (key: string, valid: boolean) => void;
  disabled?: boolean;
}) {
  const tr = useT(),
    locale = useLocale(),
    ask = useDialog();
  const [zoneReferenceTime] = useState(() => new Date());
  const zones = useMemo(
    () => timezoneOptions(locale, timezone, zoneReferenceTime),
    [locale, timezone, zoneReferenceTime],
  );
  const errorID = useId();
  const [attempt, setAttempt] = useState(0);
  const [preview, setPreview] = useState<{
    key: string;
    value?: SchedulePreview;
    failed?: boolean;
  }>();
  const runAt=recurrenceRunAt(rule,timezone);
 const candidates=rule.mode==="once" ? resolveLocalScheduleTime(rule.localTime,timezone) : [];
 const key = schedulePreviewKey(cron, timezone, nextAt,rule.mode==="once" ? runAt || "invalid" : "");
  useEffect(() => {
    const controller = new AbortController();
    const timer = setTimeout(() => {
      if (cron === null || rule.mode==="once" && !runAt) {
        onValidity(key, false);
        return;
      }
      void api<SchedulePreview>("/api/schedules/preview", {
        method: "POST",
        body: JSON.stringify({ cron, timezone, next_at: nextAt, ...(rule.mode==="once" ? {trigger_kind:"at",run_at:runAt} : {}) }),
        signal: controller.signal,
      })
        .then((value) => {
          if (!controller.signal.aborted) {
            setPreview({ key, value });
            onValidity(key, value.valid);
          }
        })
        .catch(() => {
          if (!controller.signal.aborted) {
            setPreview({ key, failed: true });
            onValidity(key, false);
          }
        });
    }, 250);
    return () => {
      clearTimeout(timer);
      controller.abort();
    };
  }, [cron, timezone, nextAt, key, attempt, onValidity,runAt,rule.mode]);
  const current = preview?.key === key ? preview : undefined;
  const toggle = (values: number[], n: number) =>
    values.includes(n) ? values.filter((v) => v !== n) : [...values, n];
  const switchMode = async (mode: RepeatMode) => {
    const next = { ...rule, mode };
    if (mode === "weekly" && !next.weekdays.length) next.weekdays = [1];
    if ((mode === "monthly" || mode === "dates") && !next.dates.length)
      next.dates = [1];
    if (
      rule.mode === "advanced" &&
      mode !== "advanced" &&
      !(await ask({
        title: tr("Replace time rule?"),
        description: `${rule.advanced} → ${recurrenceCron(next) || tr("Manual only")}`,
        choices: [{ label: tr("Replace time rule"), value: "replace" }],
      }))
    )
      return;
    onRule(next);
  };
  return (
    <fieldset className="schedule-settings">
      <legend>{tr("Schedule")}</legend>
      <SelectPicker
        label={tr("Repeat")}
        value={rule.mode}
        disabled={disabled}
        icon={<Repeat2 size={17} aria-hidden="true" />}
        options={Object.entries(repeatLabels).map(([value, label]) => ({
          value,
          label: tr(label),
        }))}
        onChange={(value) => void switchMode(value as RepeatMode)}
      />
      {rule.mode === "once" && <>
        <label>{tr("Date and time")}<input type="datetime-local" value={rule.localTime} required onChange={e=>onRule({...rule,localTime:e.target.value,atChoice:""})} /></label>
        {rule.localTime && candidates.length===0 && <p role="alert">{tr("This local time does not exist in the selected timezone.")}</p>}
        {candidates.length>1 && <><p>{tr("This time occurs twice. Choose which occurrence.")}</p><SelectPicker label={tr("Choose a UTC offset")} value={rule.atChoice} options={candidates.map(at=>({value:at,label:scheduleTime(at,timezone,locale)}))} onChange={atChoice=>onRule({...rule,atChoice})} /></>}
      </>}
      {rule.mode === "interval" && (
        <>
          <label>
            {tr("Repeat every (minutes)")}
            <input
              type="number"
              min={1}
              max={10080}
              step={1}
              required
              value={rule.intervalMinutes}
              aria-describedby={errorID}
              onChange={(e) =>
                onRule({ ...rule, intervalMinutes: e.target.value })
              }
            />
          </label>
          <p className="schedule-help">
            {tr(
              "First run after one interval. Enabling or changing the interval restarts the timer; other edits keep its cadence.",
            )}
          </p>
          <p className="schedule-help">
            {tr(
              "Elapsed minutes are unaffected by timezone or daylight saving. Preview times for a new timer are estimates until saved.",
            )}
          </p>
        </>
      )}
      {rule.mode === "weekly" && (
        <fieldset aria-describedby={errorID}>
          <legend>{tr("Days of the week")}</legend>
          <div className="schedule-day-buttons">
            {[1, 2, 3, 4, 5, 6, 0].map((n) => (
              <button
                type="button"
                key={n}
                aria-pressed={rule.weekdays.includes(n)}
                onClick={() =>
                  onRule({ ...rule, weekdays: toggle(rule.weekdays, n) })
                }
              >
                {tr(weekdayLabels[n])}
              </button>
            ))}
          </div>
        </fieldset>
      )}
      {rule.mode === "monthly" && (
        <SelectPicker
          label={tr("Day of month")}
          value={String(rule.dates[0])}
          disabled={disabled}
          options={Array.from({ length: 31 }, (_, i) => ({
            value: String(i + 1),
            label: String(i + 1),
          }))}
          onChange={(value) => onRule({ ...rule, dates: [+value] })}
        />
      )}
      {rule.mode === "dates" && (
        <fieldset aria-describedby={errorID}>
          <legend>{tr("Days of month")}</legend>
          <div className="schedule-day-buttons">
            {Array.from({ length: 31 }, (_, i) => i + 1).map((n) => (
              <button
                type="button"
                key={n}
                aria-pressed={rule.dates.includes(n)}
                onClick={() =>
                  onRule({ ...rule, dates: toggle(rule.dates, n) })
                }
              >
                {n}
              </button>
            ))}
          </div>
        </fieldset>
      )}
      {!["manual", "advanced", "interval", "once"].includes(rule.mode) && (
        <TimePicker
          value={rule.time}
          disabled={disabled}
          describedBy={errorID}
          onChange={(time) => onRule({ ...rule, time })}
        />
      )}
      {rule.mode === "advanced" && (
        <label>
          {tr("Advanced: Cron expression")}
          <input
            value={rule.advanced}
            maxLength={128}
            required
            aria-describedby={errorID}
            onChange={(e) => onRule({ ...rule, advanced: e.target.value })}
          />
          <small>
            {tr("Minute hour day month weekday. Example: 0 9 * * *")}
          </small>
        </label>
      )}
      {rule.mode === "weekdays" && (
        <p className="schedule-help">
          {tr(
            "Monday–Friday does not account for public holidays or make-up weekends.",
          )}
        </p>
      )}
      {["monthly", "dates"].includes(rule.mode) &&
        rule.dates.some((n) => n > 28) && (
          <p className="schedule-help">
            {tr("Months without the selected date are skipped.")}
          </p>
        )}
      <SelectPicker
        label={tr("Timezone")}
        value={timezone}
        options={zones}
        onChange={onTimezone}
        disabled={disabled}
        required
        describedBy={errorID}
        invalid={!!current?.value?.errors?.timezone}
        searchLabel={tr("Search timezones")}
        maxWidth={520}
        icon={<Globe2 size={17} aria-hidden="true" />}
      />
      <div className="schedule-preview" aria-live="polite">
        {cron !== null && <RecurrenceSummary cron={cron} timezone={timezone} />}
        <div id={errorID}>
          {cron === null && (
            <p role="alert">
              {tr(
                rule.mode === "interval"
                  ? "Enter a whole number from 1 to 10,080 minutes."
                  : "Choose at least one day and a valid time.",
              )}
            </p>
          )}
          {current?.value?.errors?.cron && (
            <p role="alert">
              {tr("Enter a valid five-field rule with upcoming occurrences.")}
            </p>
          )}
          {current?.value?.errors?.interval && (
            <p role="alert">
              {tr("Enter a whole number from 1 to 10,080 minutes.")}
            </p>
          )}
          {current?.value?.errors?.timezone && (
            <p role="alert">{tr("Choose a valid IANA timezone.")}</p>
          )}
          {current?.failed && (
            <p role="alert">
              {tr("Could not confirm upcoming times. Retry before saving.")}{" "}
              <button type="button" onClick={() => setAttempt((n) => n + 1)}>
                {tr("Retry")}
              </button>
            </p>
          )}
        </div>
        {!current && cron !== null && <p>{tr("Confirming upcoming times…")}</p>}
        {current?.value?.valid &&
          (cron === "" ? (
            <p>{tr("Runs only when you choose Run now.")}</p>
          ) : (
            <ol>
              {current.value.occurrences.map((time) => (
                <li key={time}>{scheduleTime(time, timezone, locale)}</li>
              ))}
            </ol>
          ))}
      </div>
    </fieldset>
  );
}
