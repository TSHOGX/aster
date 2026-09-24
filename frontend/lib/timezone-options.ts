export type TimezoneOption = {
  value: string;
  label: string;
  description: string;
};

// Preserve an existing valid IANA alias even when this browser's ICU lists a
// different canonical name (for example Asia/Kolkata versus Asia/Calcutta).
export function timezoneOptions(
  locale: string,
  selected: string,
  at: Date,
): TimezoneOption[] {
  const zones = new Set([
    "UTC",
    ...Intl.supportedValuesOf("timeZone"),
    selected,
  ]);
  const options: TimezoneOption[] = [];
  for (const zone of zones) {
    if (!zone) continue;
    try {
      const name =
        new Intl.DateTimeFormat(locale, {
          timeZone: zone,
          timeZoneName: "longGeneric",
        })
          .formatToParts(at)
          .find((part) => part.type === "timeZoneName")?.value || zone;
      const offset =
        new Intl.DateTimeFormat(locale, {
          timeZone: zone,
          timeZoneName: "longOffset",
        })
          .formatToParts(at)
          .find((part) => part.type === "timeZoneName")?.value || "";
      options.push({
        value: zone,
        label: zone,
        description: `${name} · ${offset}`,
      });
    } catch {
      /* Invalid saved values remain visible on the trigger until replaced. */
    }
  }
  return options.sort((a, b) =>
    a.value === "UTC"
      ? -1
      : b.value === "UTC"
        ? 1
        : a.value.localeCompare(b.value),
  );
}
