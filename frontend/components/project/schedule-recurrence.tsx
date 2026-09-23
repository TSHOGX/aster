"use client";
import { useEffect, useId, useMemo, useState } from "react";
import { api, type SchedulePreview } from "@/lib/project-api";
import { useLocale, useT } from "@/lib/i18n";
import {
  parseRecurrence,
  recurrenceCron,
  repeatLabels,
  scheduleTime,
  weekdayLabels,
  type Recurrence,
  type RepeatMode,
} from "@/lib/schedule-recurrence";
import { useDialog } from "./ui";

export function RecurrenceSummary({
  cron,
  timezone,
}: {
  cron: string;
  timezone: string;
}) {
  const tr = useT();
  const rule = parseRecurrence(cron);
  return (
    <span>
      {tr(repeatLabels[rule.mode])}
      {rule.mode === "weekly"
        ? ` · ${rule.weekdays.map((n) => tr(weekdayLabels[n])).join(", ")}`
        : ""}
      {["monthly", "dates"].includes(rule.mode)
        ? ` · ${rule.dates.join(", ")}`
        : ""}
      {!["manual", "advanced"].includes(rule.mode) ? ` · ${rule.time}` : ""}
      {rule.mode === "advanced" ? ` · ${cron}` : ""} · {timezone}
    </span>
  );
}

export function RecurrenceFields({
  rule,
  cron,
  timezone,
  onRule,
  onTimezone,
  onValidity,
}: {
  rule: Recurrence;
  cron: string | null;
  timezone: string;
  onRule: (r: Recurrence) => void;
  onTimezone: (s: string) => void;
  onValidity: (key: string, valid: boolean) => void;
}) {
  const tr = useT(),
    locale = useLocale(),
    ask = useDialog();
  const zones = useMemo(
    () =>
      ["UTC", ...Intl.supportedValuesOf("timeZone")].map((zone) => ({
        value: zone,
        label: `${zone.replaceAll("_", " ")} · ${new Intl.DateTimeFormat(locale, { timeZone: zone, timeZoneName: "longGeneric" }).formatToParts(new Date(0)).find((part) => part.type === "timeZoneName")?.value || zone}`,
      })),
    [locale],
  );
  const zoneID = useId(),
    errorID = useId();
  const [attempt, setAttempt] = useState(0);
  const [preview, setPreview] = useState<{
    key: string;
    value?: SchedulePreview;
    failed?: boolean;
  }>();
  const key = JSON.stringify([cron, timezone]);
  useEffect(() => {
    const controller = new AbortController();
    const timer = setTimeout(() => {
      if (cron === null) {
        onValidity(key, false);
        return;
      }
      void api<SchedulePreview>("/api/schedules/preview", {
        method: "POST",
        body: JSON.stringify({ cron, timezone }),
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
  }, [cron, timezone, key, attempt, onValidity]);
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
      <label>
        {tr("Repeat")}
        <select
          value={rule.mode}
          onChange={(e) => void switchMode(e.target.value as RepeatMode)}
        >
          {Object.entries(repeatLabels).map(([mode, label]) => (
            <option key={mode} value={mode}>
              {tr(label)}
            </option>
          ))}
        </select>
      </label>
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
        <label>
          {tr("Day of month")}
          <select
            value={rule.dates[0]}
            onChange={(e) => onRule({ ...rule, dates: [+e.target.value] })}
          >
            {Array.from({ length: 31 }, (_, i) => (
              <option key={i + 1}>{i + 1}</option>
            ))}
          </select>
        </label>
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
      {!["manual", "advanced"].includes(rule.mode) && (
        <label>
          {tr("Time")}
          <input
            type="time"
            required
            value={rule.time}
            onChange={(e) => onRule({ ...rule, time: e.target.value })}
          />
        </label>
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
      <label>
        {tr("Timezone")}
        <input
          required
          list={zoneID}
          value={timezone}
          maxLength={128}
          aria-describedby={errorID}
          onChange={(e) => onTimezone(e.target.value)}
        />
        <datalist id={zoneID}>
          {zones.map((zone) => (
            <option key={zone.value} value={zone.value}>
              {zone.label}
            </option>
          ))}
        </datalist>
      </label>
      <div className="schedule-preview" aria-live="polite">
        {cron !== null && <RecurrenceSummary cron={cron} timezone={timezone} />}
        <div id={errorID}>
          {cron === null && (
            <p role="alert">
              {tr("Choose at least one day and a valid time.")}
            </p>
          )}
          {current?.value?.errors?.cron && (
            <p role="alert">
              {tr("Enter a valid five-field rule with upcoming occurrences.")}
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
