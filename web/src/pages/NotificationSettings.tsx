import { useState } from "react";
import type { FormEvent } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useOutletContext } from "react-router";
import { request } from "../api";
import type { Session } from "../api";
import { Field } from "../components/Field";

type Rules = {
  events: string[];
  services: string[];
  origins: string[];
  severities: string[];
  immediate: boolean;
  quiet_start: string;
  quiet_end: string;
  timezone: string;
};
type Destination = {
  id: string;
  name: string;
  kind: string;
  enabled: boolean;
  revision: number;
  rules: Rules;
  secret_set: boolean;
};
type Delivery = {
  id: string;
  destination_name: string;
  state: string;
  test: boolean;
  attempts: number;
  last_error: string;
  created_at: string;
  due_at: string;
  expires_at: string;
  event_count: number;
  history: {
    number: number;
    outcome: string;
    detail: string;
    status_code: number;
  }[];
};
const eventLabels: Record<string, string> = {
  "finding.created": "New findings",
  "finding.escalated": "Severity increases",
  "finding.resolved": "Manual resolutions",
  "investigation.completed": "Completed investigations",
};
const defaults: Rules = {
  events: ["finding.created", "finding.escalated"],
  services: [],
  origins: ["live"],
  severities: ["notice", "warning"],
  immediate: false,
  quiet_start: "",
  quiet_end: "",
  timezone: Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC",
};

export function NotificationSettings() {
  const session = useOutletContext<Session>();
  const cache = useQueryClient();
  const [editing, setEditing] = useState<Destination | null | undefined>();
  const [preview, setPreview] = useState<unknown>();
  const [notice, setNotice] = useState("");
  const [cursor, setCursor] = useState("");
  const destinations = useQuery({
    queryKey: ["notification-destinations"],
    queryFn: ({ signal }) =>
      request<{ items: Destination[]; local_links: boolean }>(
        "/notifications/destinations",
        { signal },
      ),
  });
  const services = useQuery({
    queryKey: ["services"],
    queryFn: ({ signal }) =>
      request<{ items: { id: string; name: string; origin: string }[] }>(
        "/services",
        { signal },
      ),
  });
  const deliveries = useQuery({
    queryKey: ["notification-deliveries", cursor],
    queryFn: ({ signal }) =>
      request<{ items: Delivery[]; next_cursor: string }>(
        `/notifications/deliveries?cursor=${encodeURIComponent(cursor)}`,
        { signal },
      ),
    refetchInterval: (q) =>
      q.state.data?.items.some((d) =>
        ["pending", "sending", "retrying"].includes(d.state),
      )
        ? 2000
        : false,
  });
  const refresh = () => {
    void cache.invalidateQueries({ queryKey: ["notification-destinations"] });
    void cache.invalidateQueries({ queryKey: ["notification-deliveries"] });
  };
  const action = useMutation({
    mutationFn: ({ path, body }: { path: string; body: unknown }) =>
      request<unknown>(path, { body, csrf: session.csrf_token }),
    onSuccess: (_result, { path }) => {
      refresh();
      if (path.endsWith("/test")) {
        setNotice(
          "Synthetic test queued. Check delivery history for the receiver's acknowledgement.",
        );
        setCursor("");
      } else if (path.endsWith("/retry"))
        setNotice(
          "Retry queued within the original attempt and expiry limits.",
        );
      else setNotice("Destination deleted.");
    },
  });
  const previewAction = useMutation({
    mutationFn: (id: string) =>
      request<{ payload: unknown }>(
        `/notifications/destinations/${id}/preview`,
        { body: {}, csrf: session.csrf_token },
      ),
    onSuccess: (r) => setPreview(r.payload),
  });
  return (
    <section className="settings-section" id="notifications">
      <div className="notification-heading">
        <div>
          <h2>Notifications</h2>
          <p>
            Send factual alerts to Slack, Discord, or a signed webhook. No AI
            account required.
          </p>
        </div>
        <button
          type="button"
          onClick={() => {
            setEditing(null);
            setNotice("");
          }}
        >
          Add destination
        </button>
      </div>
      {destinations.data?.local_links ? (
        <p className="notification-note">
          Report links currently use localhost. Other channel members need a
          reachable Sherlock URL and a Sherlock login to open them.
        </p>
      ) : null}
      <p>
        Messages contain event summaries and authenticated links, without log
        excerpts. Demo alerts are opt-in. Each destination supports up to one
        request per second.
      </p>
      {destinations.error ||
      services.error ||
      deliveries.error ||
      action.error ||
      previewAction.error ? (
        <p role="alert" className="error">
          {
            (
              destinations.error ||
              services.error ||
              deliveries.error ||
              action.error ||
              previewAction.error
            )?.message
          }
        </p>
      ) : null}
      {notice ? <p role="status">{notice}</p> : null}
      <div className="notification-destinations">
        {destinations.data?.items.map((d) => (
          <article key={d.id} className="notification-destination">
            <div>
              <h3>{d.name}</h3>
              <p>
                {d.kind === "webhook" ? "Signed webhook" : d.kind} ·{" "}
                {d.enabled ? "Enabled" : "Disabled"} · Secret stored
              </p>
              <small>
                {d.rules.origins.join(", ")} ·{" "}
                {d.rules.immediate ? "Immediate" : "Grouped for 60 seconds"}
                {d.rules.quiet_start
                  ? ` · Quiet ${d.rules.quiet_start}–${d.rules.quiet_end} (${d.rules.timezone})`
                  : ""}
              </small>
            </div>
            <div className="row-actions">
              <button onClick={() => setEditing(d)}>Edit {d.name}</button>
              <button
                disabled={previewAction.isPending}
                onClick={() => previewAction.mutate(d.id)}
              >
                Preview {d.name}
              </button>
              <button
                disabled={!d.enabled || action.isPending}
                onClick={() => {
                  if (
                    window.confirm(
                      `Send a clearly labeled synthetic test to ${d.name}? This posts a real message, bypassing quiet hours and demo filters.`,
                    )
                  )
                    action.mutate({
                      path: `/notifications/destinations/${d.id}/test`,
                      body: { confirm: true },
                    });
                }}
              >
                Send test to {d.name}
              </button>
              <button
                disabled={action.isPending}
                onClick={() => {
                  if (
                    window.confirm(
                      `Delete ${d.name}? This removes its secret and cancels queued messages. Already dispatched messages cannot be recalled.`,
                    )
                  )
                    action.mutate({
                      path: `/notifications/destinations/${d.id}/delete`,
                      body: {},
                    });
                }}
              >
                Delete {d.name}
              </button>
            </div>
          </article>
        ))}
        {destinations.data?.items.length === 0 ? (
          <p>No notification destinations yet.</p>
        ) : null}
      </div>
      {editing !== undefined && services.data ? (
        <DestinationForm
          key={editing?.id ?? "new"}
          destination={editing}
          services={services.data?.items ?? []}
          csrf={session.csrf_token}
          onCancel={() => setEditing(undefined)}
          onSaved={() => {
            setEditing(undefined);
            refresh();
            setNotice(
              "Destination saved. Changes cancel queued messages; only future events use the new configuration.",
            );
          }}
        />
      ) : null}
      {preview !== undefined ? (
        <div className="notification-preview">
          <h3>Synthetic payload preview</h3>
          <p>Preview only—no message has been sent.</p>
          <pre>{JSON.stringify(preview, null, 2)}</pre>
          <button onClick={() => setPreview(undefined)}>Close preview</button>
        </div>
      ) : null}
      <h3>Delivery history</h3>
      <p>
        Timeouts can leave delivery uncertain; retries may duplicate a message.
        Up to eight attempts within 24 hours and the content's retention limit.
      </p>
      <button
        onClick={() => {
          setCursor("");
          void cache.invalidateQueries({
            queryKey: ["notification-deliveries"],
          });
        }}
      >
        Refresh deliveries
      </button>
      <div
        className="notification-history"
        aria-label="Notification delivery history"
      >
        {deliveries.data?.items.map((d) => (
          <details key={d.id}>
            <summary>
              <strong>{d.destination_name}</strong> ·{" "}
              {d.test ? "Synthetic test" : `${d.event_count} event(s)`} ·{" "}
              <span>{d.state}</span> · {new Date(d.created_at).toLocaleString()}
            </summary>
            <p>
              {d.last_error ||
                `Scheduled for ${new Date(d.due_at).toLocaleString()}`}
            </p>
            <p>
              {d.attempts}/8 attempts · Expires{" "}
              {new Date(d.expires_at).toLocaleString()}
            </p>
            <ol>
              {d.history.map((a) => (
                <li key={a.number}>
                  Attempt {a.number}: {a.outcome}
                  {a.status_code ? ` (HTTP ${a.status_code})` : ""}. {a.detail}
                </li>
              ))}
            </ol>
            {d.state === "failed" &&
            d.attempts < 8 &&
            new Date(d.expires_at).getTime() > Date.now() ? (
              <button
                disabled={action.isPending}
                onClick={() => {
                  if (
                    window.confirm(
                      "Retry this delivery? The receiver may already have accepted it, so this can post a duplicate.",
                    )
                  )
                    action.mutate({
                      path: `/notifications/deliveries/${d.id}/retry`,
                      body: { confirm: true },
                    });
                }}
              >
                Retry delivery
              </button>
            ) : null}
          </details>
        ))}
        {deliveries.data?.items.length === 0 ? (
          <p>
            No deliveries yet. Preview a destination, then send a test or run a
            qualifying scan.
          </p>
        ) : null}
      </div>
      {deliveries.data?.next_cursor ? (
        <button onClick={() => setCursor(deliveries.data!.next_cursor)}>
          Older deliveries
        </button>
      ) : null}
    </section>
  );
}
function DestinationForm({
  destination,
  services,
  csrf,
  onSaved,
  onCancel,
}: {
  destination: Destination | null;
  services: { id: string; name: string; origin: string }[];
  csrf: string;
  onSaved: () => void;
  onCancel: () => void;
}) {
  const [kind, setKind] = useState(destination?.kind ?? "slack");
  const rules = destination?.rules ?? defaults;
  const save = useMutation({
    mutationFn: (body: unknown) =>
      request(
        `/notifications/destinations${destination ? `/${destination.id}` : ""}`,
        { body, csrf },
      ),
    onSuccess: onSaved,
  });
  function submit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    const f = new FormData(e.currentTarget);
    save.mutate({
      name: f.get("name"),
      kind,
      enabled: f.has("enabled"),
      revision: destination?.revision ?? 0,
      url: f.get("url"),
      signing_key: kind === "webhook" ? f.get("signing_key") : "",
      rules: {
        events: f.getAll("events"),
        services: f.getAll("services"),
        origins: f.getAll("origins"),
        severities: f.getAll("severities"),
        immediate: f.has("immediate"),
        quiet_start: f.get("quiet_start"),
        quiet_end: f.get("quiet_end"),
        timezone: f.get("timezone"),
      },
    });
  }
  return (
    <form className="notification-form" onSubmit={submit}>
      <h3>{destination ? "Edit destination" : "New destination"}</h3>
      <Field
        name="name"
        label="Destination name"
        defaultValue={destination?.name ?? ""}
        required
        maxLength={80}
      />
      <label className="select-field">
        Destination type
        <select
          aria-label="Destination type"
          value={kind}
          disabled={!!destination}
          onChange={(e) => setKind(e.target.value)}
        >
          <option value="slack">Slack</option>
          <option value="discord">Discord</option>
          <option value="webhook">Signed generic webhook</option>
        </select>
      </label>
      <Field
        name="url"
        label="Webhook URL"
        type="password"
        autoComplete="new-password"
        required={!destination}
        maxLength={2048}
        hint={
          destination
            ? "Leave blank to keep the stored secret URL."
            : "Paste the full HTTPS webhook URL. It is encrypted and never returned after saving."
        }
      />
      {kind === "webhook" ? (
        <Field
          name="signing_key"
          label="Signing secret"
          type="password"
          autoComplete="new-password"
          required={!destination}
          minLength={32}
          maxLength={256}
          hint="32–256 characters shared with your receiver. Leave blank when editing to keep it."
        />
      ) : null}
      <fieldset>
        <legend>Events</legend>
        {Object.entries(eventLabels).map(([value, label]) => (
          <label className="checkbox-field" key={value}>
            <input
              type="checkbox"
              name="events"
              value={value}
              defaultChecked={rules.events.includes(value)}
            />
            {label}
          </label>
        ))}
      </fieldset>
      <fieldset>
        <legend>Data origins</legend>
        {["live", "demo"].map((value) => (
          <label className="checkbox-field" key={value}>
            <input
              type="checkbox"
              name="origins"
              value={value}
              defaultChecked={rules.origins.includes(value)}
            />
            {value === "live" ? "Live services" : "Allow simulated demo alerts"}
          </label>
        ))}
      </fieldset>
      <fieldset>
        <legend>Severity</legend>
        {["notice", "warning"].map((value) => (
          <label className="checkbox-field" key={value}>
            <input
              type="checkbox"
              name="severities"
              value={value}
              defaultChecked={rules.severities.includes(value)}
            />
            {value}
          </label>
        ))}
      </fieldset>
      <fieldset>
        <legend>Services (none selected means all)</legend>
        {services.map((v) => (
          <label className="checkbox-field" key={v.id}>
            <input
              type="checkbox"
              name="services"
              value={v.id}
              defaultChecked={rules.services.includes(v.id)}
            />
            {v.name} ({v.origin})
          </label>
        ))}
      </fieldset>
      <div className="notification-times">
        <Field
          name="quiet_start"
          label="Quiet hours start"
          type="time"
          defaultValue={rules.quiet_start}
        />
        <Field
          name="quiet_end"
          label="Quiet hours end"
          type="time"
          defaultValue={rules.quiet_end}
        />
      </div>
      <Field
        name="timezone"
        label="Quiet hours timezone"
        defaultValue={rules.timezone}
        required
        hint="IANA name, for example Europe/Stockholm. Leave both times blank for no quiet hours."
      />
      <label className="checkbox-field">
        <input
          name="immediate"
          type="checkbox"
          defaultChecked={rules.immediate}
        />
        Send events immediately instead of grouping for 60 seconds (quiet hours
        still apply)
      </label>
      <label className="checkbox-field">
        <input
          name="enabled"
          type="checkbox"
          defaultChecked={destination?.enabled ?? false}
        />
        Enable this destination
      </label>
      {save.error ? (
        <p role="alert" className="error">
          {save.error.message}
        </p>
      ) : null}
      <div className="form-actions">
        <button className="primary compact" disabled={save.isPending}>
          Save destination
        </button>
        <button type="button" onClick={onCancel}>
          Cancel editing destination
        </button>
      </div>
    </form>
  );
}
