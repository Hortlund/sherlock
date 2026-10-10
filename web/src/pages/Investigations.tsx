import { AIAnalysis } from "./AI";
import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useNavigate, useOutletContext, useSearchParams } from "react-router";
import { request } from "../api";
import type { Session } from "../api";
import type { Run } from "./Findings";
import { StoredEvidence } from "./Findings";
import { Heading, useServices } from "./Sources";
type Report = {
  summary: string;
  observations: {
    text: string;
    evidence_ids: string[];
    at: string;
    window: string;
  }[];
  unknowns: string[];
  checks: string[];
  adequate: boolean;
};
type Investigation = Run & { result: Run["result"] & { report?: Report } };
const active = (state: string) =>
  ["queued", "collecting", "evaluating", "reporting"].includes(state);
export function InvestigateButton({
  serviceID,
  findingID,
}: {
  serviceID: string;
  findingID?: string;
}) {
  const session = useOutletContext<Session>(),
    navigate = useNavigate();
  const action = useMutation({
    mutationFn: () =>
      request<{ id: string }>(`/services/${serviceID}/investigations`, {
        body: { finding_id: findingID ?? "" },
        csrf: session.csrf_token,
      }),
    onSuccess: (r) => navigate(`/investigations?id=${r.id}`),
  });
  return (
    <>
      <button disabled={action.isPending} onClick={() => action.mutate()}>
        Investigate
      </button>
      {action.error ? (
        <span role="alert" className="error-text">
          {action.error.message}
        </span>
      ) : null}
    </>
  );
}
function InvestigationDetail({ id }: { id: string }) {
  const session = useOutletContext<Session>(),
    cache = useQueryClient();
  const [expanded, setExpanded] = useState<string[] | null>(null);
  const query = useQuery({
    queryKey: ["investigation", id],
    queryFn: () => request<Investigation>(`/investigations/${id}`),
    refetchInterval: (q) =>
      active(q.state.data?.state ?? "queued") ? 1000 : false,
  });
  // A terminal state gets its own fetch, even if the preceding events poll
  // completed just before the run transitioned. No polling race or effect needed.
  const terminalState =
    query.data && !active(query.data.state) ? query.data.state : "active";
  const events = useQuery({
    queryKey: ["investigation-events", id, terminalState],
    queryFn: () =>
      request<{
        items: { id: number; stage: string; message: string; at: string }[];
      }>(`/investigations/${id}/events`),
    refetchInterval: active(query.data?.state ?? "queued") ? 1000 : false,
  });
  const cancel = useMutation({
    mutationFn: () =>
      request(`/investigations/${id}/cancel`, {
        body: {},
        csrf: session.csrf_token,
      }),
    onSuccess: () =>
      cache.invalidateQueries({ queryKey: ["investigation", id] }),
  });
  if (query.isPending) return <p role="status">Loading investigation…</p>;
  if (query.error)
    return (
      <p role="alert" className="error">
        {query.error.message}
      </p>
    );
  const r = query.data,
    report = r.result.report;
  return (
    <section className="investigation-detail">
      <div className="page-heading">
        <div>
          <p className="breadcrumb">
            {r.snapshot.origin === "demo"
              ? "Simulated telemetry"
              : "Live telemetry"}{" "}
            · {r.snapshot.environment}
          </p>
          <h2>{r.snapshot.name}</h2>
          <p>
            {new Date(r.start).toLocaleString()} –{" "}
            {new Date(r.end).toLocaleString()}
          </p>
        </div>
        <span className="source-status">{r.state.replaceAll("_", " ")}</span>
      </div>
      <div className="run-budget">
        {r.requests} / 12 telemetry requests · 90-second execution budget ·
        mapping v{r.snapshot.revision}
      </div>
      <ol className="progress-list" aria-label="Investigation progress">
        {events.data?.items.map((ev) => (
          <li key={ev.id}>
            <strong>{ev.stage}</strong>
            <span>{ev.message}</span>
          </li>
        ))}
      </ol>
      {events.error ? <p className="error">{events.error.message}</p> : null}
      {active(r.state) ? (
        <button
          className="text-button"
          disabled={cancel.isPending}
          onClick={() => cancel.mutate()}
        >
          Cancel investigation
        </button>
      ) : null}
      {cancel.error ? <p className="error">{cancel.error.message}</p> : null}
      {r.result.issues?.map((v, i) => (
        <p key={i} className="error">
          {v}
        </p>
      ))}
      {report ? (
        <>
          <section className="report-section">
            <p className="eyebrow">Deterministic report</p>
            <h2>What the evidence shows</h2>
            <p className="report-summary">{report.summary}</p>
            {report.observations.map((o, i) => (
              <div className="report-observation" key={i}>
                <span>
                  {o.window} · {new Date(o.at).toLocaleString()}
                </span>
                <p>{o.text}</p>
                <button
                  className="text-button"
                  onClick={() =>
                    setExpanded(
                      expanded === o.evidence_ids ? null : o.evidence_ids,
                    )
                  }
                >
                  Inspect supporting evidence ({o.evidence_ids.length})
                </button>
              </div>
            ))}
          </section>
          <section className="report-section">
            <h2>What remains unknown</h2>
            <ul>
              {report.unknowns.map((u, i) => (
                <li key={i}>{u}</li>
              ))}
            </ul>
          </section>
          <section className="report-section">
            <h2>Suggested manual checks</h2>
            <ol>
              {report.checks.map((c, i) => (
                <li key={i}>{c}</li>
              ))}
            </ol>
          </section>
          {expanded ? (
            <section className="report-section">
              <h2>Supporting evidence</h2>
              <StoredEvidence ids={expanded} />
            </section>
          ) : null}
        </>
      ) : !active(r.state) ? (
        <p>No report was produced for this run.</p>
      ) : (
        <p role="status">
          Collecting and validating evidence. You can leave this page and
          return; progress is stored.
        </p>
      )}
      {r.state === "completed" ? (
        <AIAnalysis
          key={r.id}
          investigationID={r.id}
          serviceID={r.service_id}
        />
      ) : null}
    </section>
  );
}
export function InvestigationsPage() {
  const [params, setParams] = useSearchParams(),
    [cursor, setCursor] = useState(""),
    [service, setService] = useState("");
  const services = useServices(),
    session = useOutletContext<Session>();
  const list = useQuery({
    queryKey: ["investigations", cursor],
    queryFn: () =>
      request<{ items: Investigation[]; next_cursor: string }>(
        `/investigations?cursor=${cursor}`,
      ),
    refetchInterval: 5000,
  });
  const start = useMutation({
    mutationFn: (body: unknown) =>
      request<{ id: string }>(`/services/${service}/investigations`, {
        body,
        csrf: session.csrf_token,
      }),
    onSuccess: (r) => setParams({ id: r.id }),
  });
  return (
    <>
      <Heading
        title="Investigations"
        description="Reproducible observations. Explicit unknowns. No invented causes."
      />
      {params.get("id") ? (
        <>
          <button className="text-button" onClick={() => setParams({})}>
            ← All investigations
          </button>
          <InvestigationDetail key={params.get("id")} id={params.get("id")!} />
        </>
      ) : (
        <>
          <form
            className="investigation-launch"
            onSubmit={(e) => {
              e.preventDefault();
              const data = new FormData(e.currentTarget);
              const end = data.get("end");
              start.mutate(
                end
                  ? {
                      end: new Date(String(end)).toISOString(),
                      start: new Date(
                        new Date(String(end)).getTime() -
                          Number(data.get("minutes")) * 60000,
                      ).toISOString(),
                    }
                  : { minutes: Number(data.get("minutes")) },
              );
            }}
          >
            <label className="select-field">
              Service
              <select
                value={service}
                onChange={(e) => setService(e.target.value)}
                required
              >
                <option value="">Choose a service</option>
                {services.data?.items.map((s) => (
                  <option key={s.id} value={s.id}>
                    {s.name} ({s.origin})
                  </option>
                ))}
              </select>
            </label>
            <label className="select-field">
              Window end (optional)
              <input type="datetime-local" name="end" />
            </label>
            <label className="select-field">
              Incident window
              <select name="minutes">
                <option value="30">30 minutes</option>
                <option value="15">15 minutes</option>
                <option value="60">60 minutes</option>
              </select>
            </label>
            <button
              className="primary compact"
              disabled={start.isPending || !service}
            >
              Start investigation
            </button>
          </form>
          <p className="table-note">
            Without a selected end time, the chosen window ends at the last
            five-minute boundary.
          </p>
          {start.error || list.error ? (
            <p role="alert" className="error">
              {(start.error ?? list.error)?.message}
            </p>
          ) : null}
          {list.data?.items.length ? (
            <div className="table-wrap">
              <table>
                <thead>
                  <tr>
                    <th>Service</th>
                    <th>State</th>
                    <th>Created</th>
                    <th>Report</th>
                  </tr>
                </thead>
                <tbody>
                  {list.data.items.map((r) => (
                    <tr key={r.id}>
                      <td>
                        <strong>{r.snapshot.name}</strong>
                        <small>
                          {r.snapshot.origin === "demo" ? "Simulated" : "Live"}{" "}
                          · mapping v{r.snapshot.revision}
                        </small>
                      </td>
                      <td>{r.state.replaceAll("_", " ")}</td>
                      <td>{new Date(r.created_at).toLocaleString()}</td>
                      <td>
                        <button
                          className="text-button"
                          onClick={() => setParams({ id: r.id })}
                        >
                          Open investigation
                        </button>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          ) : (
            <section className="empty-state">
              <h2>No investigations yet.</h2>
              <p>
                Start from a service or finding to collect a bounded,
                evidence-based report.
              </p>
            </section>
          )}
          {cursor ? (
            <button className="text-button" onClick={() => setCursor("")}>
              First page
            </button>
          ) : null}
          {list.data?.next_cursor ? (
            <button
              className="text-button"
              onClick={() => setCursor(list.data!.next_cursor)}
            >
              Next page →
            </button>
          ) : null}
        </>
      )}
    </>
  );
}
