import { useState } from "react";
import type { FormEvent } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useOutletContext } from "react-router";
import { request } from "../api";
import type { Session } from "../api";
import { Drawer, StatusBadge } from "../components/WorkspaceUI";
import { Field } from "../components/Field";

type Binding = { id: string; service_id: string; name: string; origin: string; enabled: boolean; revision: number; per_minute: number; daily_limit: number };
type Service = { id: string; name: string; origin: string };
type Receipt = { id: string; event_id: string; binding_name: string; state: string; reason: string; title: string; created_at: string; start: string; end: string; investigation_id: string; investigation_state: string; ai_state: string; ai_reason: string };

export function InboundSettings() {
  const session = useOutletContext<Session>(), cache = useQueryClient();
  const [editing, setEditing] = useState<Binding | null | undefined>();
  const [cursor, setCursor] = useState("");
  const [notice, setNotice] = useState("");
  const bindings = useQuery({ queryKey: ["inbound-bindings"], queryFn: ({ signal }) => request<{ items: Binding[] }>("/inbound/bindings", { signal }) });
  const services = useQuery({ queryKey: ["services"], queryFn: ({ signal }) => request<{ items: Service[] }>("/services", { signal }) });
  const receipts = useQuery({ queryKey: ["inbound-receipts", cursor], queryFn: ({ signal }) => request<{ items: Receipt[]; next_cursor: string }>(`/inbound/receipts?cursor=${encodeURIComponent(cursor)}`, { signal }), refetchInterval: 5000 });
  const action = useMutation({ mutationFn: ({ path, body }: { path: string; body: unknown }) => request(path, { body, csrf: session.csrf_token }), onSuccess: () => {
    void cache.invalidateQueries({ queryKey: ["inbound-bindings"] });
    void cache.invalidateQueries({ queryKey: ["inbound-receipts"] });
    setNotice("Inbound binding saved. Automation and tool consent are unchanged."); setEditing(undefined);
  } });
  return <section className="settings-section inbound-settings">
    <div className="section-heading"><div><h2>Inbound alerts</h2><p>Receive signed alerts from an external monitoring system.</p></div><button className="primary compact" onClick={() => { action.reset(); setEditing(null); }}>Add inbound binding</button></div>
    <div className="notification-note"><p>Bindings start disabled. Dispatch also requires Automatic investigations, recurring consent, a selected monitored service and an allowed origin. Tools require their separate opt-in. Skipped receipts do not replay when settings change.</p><Link to="#automation">Review automation consent →</Link></div>
    {notice ? <p role="status">{notice}</p> : null}
    {bindings.error || services.error || receipts.error || (action.error && editing === undefined) ? <p role="alert">{(bindings.error ?? services.error ?? receipts.error ?? action.error)?.message}</p> : null}
    {bindings.isPending ? <p role="status">Loading inbound bindings…</p> : null}
    {bindings.data?.items.length === 0 ? <div className="empty-state"><h3>No inbound bindings</h3><p>Add a service binding and a dedicated signing secret. Enabling a binding does not enable automatic investigations.</p></div> : null}
    {bindings.data?.items.map(b => <article className="notification-note" key={b.id}>
      <h3>{b.name} <StatusBadge value={b.enabled ? "enabled" : "disabled"} /></h3>
      <p>{services.data?.items.find(s => s.id === b.service_id)?.name ?? b.service_id} · {b.origin === "demo" ? "Simulated" : "Live"} · {b.per_minute} requests/minute · {b.daily_limit} investigations/UTC day</p>
      <Field id={`endpoint-${b.id}`} label="Signed alert endpoint" readOnly value={`${window.location.origin}/api/v1/inbound/alerts/${b.id}`} />
      <div className="form-actions"><button onClick={() => { action.reset(); setEditing(b); }}>Edit binding</button><button disabled={action.isPending} onClick={() => action.mutate({ path: `/inbound/bindings/${b.id}`, body: { binding: { ...b, enabled: !b.enabled } } })}>{b.enabled ? "Disable binding" : "Enable binding"}</button><button disabled={action.isPending} onClick={() => { if (window.confirm("Delete this binding and its signing secret? Pending work will be cancelled.")) action.mutate({ path: `/inbound/bindings/${b.id}/delete`, body: {} }); }}>Delete binding</button></div>
    </article>)}
    <section className="settings-section"><h3>Receipt history</h3><p>External titles are untrusted context. Sherlock collects its own telemetry before making evidence-backed claims.</p>
      {receipts.isPending ? <p role="status">Loading receipts…</p> : null}
      {receipts.data?.items.length === 0 ? <p>No verified alerts received yet.</p> : null}
      {receipts.data?.items.map(r => <details className="automation-entry" key={r.id}><summary><strong>{r.binding_name} · {r.event_id}</strong> <StatusBadge value={["skipped", "cancelled", "failed"].includes(r.state) ? r.state : r.ai_state || r.investigation_state || r.state} /><time>{new Date(r.created_at).toLocaleString()}</time></summary>
        <p>{r.reason}</p>{r.title ? <p><strong>External title:</strong> {r.title}</p> : null}
        <p>Receipt: {r.id}</p><p>{new Date(r.start).toLocaleString()} – {new Date(r.end).toLocaleString()}</p>
        {r.ai_state ? <p>AI analysis: {r.ai_state}. {r.ai_reason}</p> : null}
        {r.investigation_id ? <Link to={`/investigations?id=${r.investigation_id}`}>Open inbound investigation →</Link> : null}
      </details>)}
      <div className="button-row">{cursor ? <button onClick={() => setCursor("")}>Latest receipts</button> : null}{receipts.data?.next_cursor ? <button onClick={() => setCursor(receipts.data.next_cursor)}>Older receipts</button> : null}</div>
    </section>
    <details className="notification-note"><summary>Signed request format</summary><p>Send the generic <code>inbound-v1</code> schema with event_id, title, start and end. Sign the Unix timestamp, a dot, and the exact UTF-8 body with HMAC-SHA256. Headers: X-Sherlock-Timestamp and X-Sherlock-Signature (v1= followed by lowercase hex). Signatures expire after five minutes. Reuse the same event ID and exact body for retries.</p><p>Choose a 5–60 minute incident ending in the last seven days; incident and comparison windows must total at most two hours. Native Alertmanager and Sherlock outbound envelopes are not supported. Binding scope, model, tools and destinations cannot be selected by the payload.</p></details>
    {editing !== undefined ? <Drawer title={editing ? "Edit inbound binding" : "Add inbound binding"} onClose={() => setEditing(undefined)}><BindingForm key={editing?.id ?? "new"} binding={editing} services={services.data?.items ?? []} pending={action.isPending} error={action.error?.message} save={body => action.mutate({ path: editing ? `/inbound/bindings/${editing.id}` : "/inbound/bindings", body })} /></Drawer> : null}
  </section>;
}
function BindingForm({ binding, services, pending, error, save }: { binding: Binding | null; services: Service[]; pending: boolean; error?: string; save: (body: unknown) => void }) {
  const [service, setService] = useState(binding?.service_id ?? "");
  function submit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault(); const f = new FormData(e.currentTarget);
    save({ binding: { id: binding?.id ?? "", service_id: service, origin: binding?.origin ?? services.find(s => s.id === service)?.origin, name: String(f.get("name")), revision: binding?.revision ?? 0, enabled: f.has("enabled"), per_minute: Number(f.get("per_minute")), daily_limit: Number(f.get("daily_limit")) }, signing_secret: String(f.get("signing_secret")) });
  }
  return <form onSubmit={submit}>
    <Field name="name" label="Binding name" defaultValue={binding?.name ?? ""} required maxLength={100} />
    <label className="select-field">Service and origin<select value={service} onChange={e => setService(e.target.value)} disabled={!!binding} required><option value="">Select a service</option>{services.map(s => <option key={s.id} value={s.id}>{s.name} · {s.origin === "demo" ? "Simulated" : "Live"}</option>)}</select></label>
    <Field name="signing_secret" label={binding ? "Replace signing secret" : "Signing secret"} type="password" autoComplete="new-password" required={!binding} minLength={32} maxLength={256} hint="Use a dedicated random secret of at least 32 characters. Stored encrypted and never returned. Leave blank when editing to retain it." />
    <Field name="per_minute" label="Requests per minute" type="number" min={1} max={60} defaultValue={binding?.per_minute ?? 10} required />
    <Field name="daily_limit" label="Investigations per UTC day" type="number" min={1} max={100} defaultValue={binding?.daily_limit ?? 10} required />
    <label className="checkbox-field"><input name="enabled" type="checkbox" defaultChecked={binding?.enabled ?? false} />Enable this inbound binding</label>
    <p className="field-hint">Saving changes cancels pending work for this binding. Service cooldown and AI cost budgets also apply. Automatic investigations and tools retain their separate consent controls.</p>
    {error ? <p role="alert">{error}</p> : null}<button className="primary compact" disabled={pending}>{pending ? "Saving…" : "Save inbound binding"}</button>
  </form>;
}
