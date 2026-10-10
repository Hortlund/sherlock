import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useOutletContext } from "react-router";
import { request } from "../api";
import type { Session } from "../api";
import { Field } from "../components/Field";
type Config = {
  cadence_minutes: number;
  current_minutes: number;
  baseline_minutes: number;
  retention_days: number;
  cooldown_hours: number;
  min_occurrences: number;
  min_warnings: number;
  warning_multiplier: number;
  min_traffic: number;
  error_delta: number;
  error_multiplier: number;
  disabled: string[];
};
export function OperationalSettings() {
  const session = useOutletContext<Session>(),
    cache = useQueryClient();
  const config = useQuery({
    queryKey: ["settings"],
    queryFn: () => request<Config>("/settings"),
  });
  const health = useQuery({
    queryKey: ["system"],
    queryFn: () =>
      request<{
        database: string;
        queued: number;
        running: number;
        failed: number;
      }>("/system"),
    refetchInterval: 10000,
  });
  const save = useMutation({
    mutationFn: (body: Config) =>
      request("/settings", { body, csrf: session.csrf_token }),
    onSuccess: () => cache.invalidateQueries({ queryKey: ["settings"] }),
  });
  if (config.isPending) return <p>Loading monitoring settings…</p>;
  if (config.error) return <p className="error">{config.error.message}</p>;
  const c = config.data;
  const fields: [
    keyof Omit<Config, "disabled">,
    string,
    number,
    number,
    number,
  ][] = [
    ["cadence_minutes", "Scan cadence (minutes)", 5, 60, 1],
    ["current_minutes", "Current window (minutes)", 5, 60, 1],
    ["baseline_minutes", "Comparison window (minutes)", 15, 115, 1],
    ["retention_days", "Evidence retention (days)", 1, 30, 1],
    ["cooldown_hours", "Finding recurrence cooldown (hours)", 1, 168, 1],
    ["min_occurrences", "Minimum new-pattern occurrences", 2, 1000, 1],
    ["min_warnings", "Minimum warning count", 3, 1000, 1],
    ["warning_multiplier", "Warning rate multiplier", 2, 100, 0.1],
    ["min_traffic", "Minimum requests per window", 20, 1000000000, 1],
    [
      "error_delta",
      "Minimum error increase (percentage points)",
      0.1,
      100,
      0.1,
    ],
    ["error_multiplier", "Error proportion multiplier", 1.1, 100, 0.1],
  ];
  return (
    <>
      <section className="settings-section">
        <h2>Monitoring and evidence</h2>
        <p>
          Current and comparison windows together must fit within two hours. New
          retention settings apply to newly collected evidence.
        </p>
        <form
          key={JSON.stringify(c)}
          onSubmit={(e) => {
            e.preventDefault();
            const f = new FormData(e.currentTarget);
            const next = { ...c };
            for (const [key] of fields)
              next[key] =
                Number(f.get(key)) / (key === "error_delta" ? 100 : 1);
            next.disabled = [
              "new_exception",
              "warning_burst",
              "error_rate",
            ].filter((v) => !f.has(v));
            save.mutate(next);
          }}
        >
          {fields.map(([key, label, min, max, step]) => (
            <Field
              key={key}
              name={key}
              label={label}
              type="number"
              min={min}
              max={max}
              step={step}
              defaultValue={key === "error_delta" ? c[key] * 100 : c[key]}
              required
            />
          ))}
          <fieldset>
            <legend>Enabled detectors</legend>
            {["new_exception", "warning_burst", "error_rate"].map((d) => (
              <label className="checkbox-field" key={d}>
                <input
                  name={d}
                  type="checkbox"
                  defaultChecked={!c.disabled.includes(d)}
                />
                {d.replaceAll("_", " ")}
              </label>
            ))}
          </fieldset>
          {save.error ? (
            <p role="alert" className="error">
              {save.error.message}
            </p>
          ) : null}
          {save.isSuccess ? (
            <p role="status">Monitoring settings saved.</p>
          ) : null}
          <button className="primary compact" disabled={save.isPending}>
            Save monitoring settings
          </button>
        </form>
      </section>
      <section className="settings-section">
        <h2>System health</h2>
        {health.data ? (
          <p>
            Database {health.data.database} · {health.data.queued} queued ·{" "}
            {health.data.running} running · {health.data.failed} failed runs
          </p>
        ) : (
          <p>{health.error?.message ?? "Checking health…"}</p>
        )}
        <p>
          Up to 4 workers and 2 concurrent requests per connection. Each run has
          a 90-second deadline and a 12-request limit, retained across retries.
        </p>
      </section>
    </>
  );
}
