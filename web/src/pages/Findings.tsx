import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useOutletContext } from "react-router";
import { InvestigateButton } from "./Investigations";
import { request } from "../api";
import type { Session } from "../api";
import { Heading, EvidenceList } from "./Sources";
import type { Evidence, Service } from "./Sources";
export type Coverage = { detector: string; status: string; reason: string };
export type Candidate = {
  detector: string;
  current: number;
  baseline: number;
  evidence_ids: string[];
  thresholds: Record<string, unknown>;
};
export type Finding = {
  id: string;
  service_id: string;
  origin: string;
  state: string;
  severity: string;
  title: string;
  description: string;
  details: Candidate;
  first_seen: string;
  last_seen: string;
  snoozed_until: string | null;
  feedback: string;
};
export type Run = {
  id: string;
  service_id: string;
  kind: string;
  state: string;
  snapshot: Service;
  start: string;
  end: string;
  created_at: string;
  requests: number;
  result: {
    analysis: { coverage: Coverage[]; candidates: Candidate[] };
    evidence_ids: string[];
    issues: string[];
  };
};
export function StoredEvidence({ ids }: { ids: string[] }) {
  const data = useQuery({
    queryKey: ["evidence", ...ids],
    queryFn: async () =>
      Promise.all(
        ids.map(async (id) => {
          try {
            return {
              id,
              evidence: await request<Evidence>(`/evidence/${id}`),
              error: "",
            };
          } catch (e) {
            return {
              id,
              evidence: null,
              error: e instanceof Error ? e.message : "Evidence unavailable",
            };
          }
        }),
      ),
  });
  return (
    <>
      {data.isPending ? <p role="status">Loading stored evidence…</p> : null}
      {data.data
        ?.filter((v) => v.error)
        .map((v) => (
          <p key={v.id} className="error">
            Evidence {v.id}: {v.error}
          </p>
        ))}
      <EvidenceList
        items={
          data.data?.flatMap((v) => (v.evidence ? [v.evidence] : [])) ?? []
        }
      />
    </>
  );
}
export function RunStatus({ id }: { id: string }) {
  const run = useQuery({
    queryKey: ["scan", id],
    queryFn: () => request<Run>(`/scans/${id}`),
    refetchInterval: (q) =>
      ["queued", "collecting", "evaluating", "reporting"].includes(
        q.state.data?.state ?? "queued",
      )
        ? 1000
        : false,
  });
  const r = run.data;
  return (
    <section className="preview-section" aria-live="polite">
      <h2>Scan {r?.state.replaceAll("_", " ") ?? "queued"}</h2>
      {run.error ? <p className="error">{run.error.message}</p> : null}
      {r?.result.issues?.map((v, i) => (
        <p key={i} className="error">
          {v}
        </p>
      ))}
      {r?.result.analysis.coverage?.map((c) => (
        <div className="coverage-row" key={c.detector}>
          <strong>{c.detector.replaceAll("_", " ")}</strong>
          <span>{c.status.replaceAll("_", " ")}</span>
          <p>{c.reason}</p>
        </div>
      ))}
      {r?.state === "completed" ? (
        <Link className="inline-link" to="/findings">
          Review findings →
        </Link>
      ) : null}
      {r?.result.evidence_ids?.length ? (
        <details>
          <summary>Collected evidence</summary>
          <StoredEvidence ids={r.result.evidence_ids} />
        </details>
      ) : null}
    </section>
  );
}
export function FindingsPage() {
  const session = useOutletContext<Session>(),
    cache = useQueryClient();
  const [filter, setFilter] = useState(""),
    [cursor, setCursor] = useState(""),
    [selected, setSelected] = useState("");
  const findings = useQuery({
    queryKey: ["findings", filter, cursor],
    queryFn: () =>
      request<{ items: Finding[]; next_cursor: string }>(
        `/findings?state=${filter}&cursor=${cursor}`,
      ),
    refetchInterval: 10000,
  });
  const action = useMutation({
    mutationFn: ({
      id,
      path,
      body,
    }: {
      id: string;
      path: string;
      body: unknown;
    }) =>
      request(`/findings/${id}/${path}`, { body, csrf: session.csrf_token }),
    onSuccess: () => cache.invalidateQueries({ queryKey: ["findings"] }),
  });
  return (
    <>
      <Heading
        title="Findings"
        description="Observed changes, with the evidence and limits behind them."
      />
      <div className="filter-row">
        <label>
          State
          <select
            value={filter}
            onChange={(e) => {
              setFilter(e.target.value);
              setCursor("");
            }}
          >
            <option value="">All states</option>
            {["new", "acknowledged", "snoozed", "resolved", "dismissed"].map(
              (v) => (
                <option key={v}>{v}</option>
              ),
            )}
          </select>
        </label>
        {cursor ? (
          <button className="text-button" onClick={() => setCursor("")}>
            First page
          </button>
        ) : null}
      </div>
      {findings.error || action.error ? (
        <p role="alert" className="error">
          {(findings.error ?? action.error)?.message}
        </p>
      ) : null}
      {findings.isPending ? (
        <p role="status">Loading findings…</p>
      ) : findings.data?.items.length ? (
        findings.data.items.map((f) => (
          <article className="finding" key={f.id}>
            <div className="finding-meta">
              <span>{f.severity}</span>
              <span>{f.state}</span>
              <span>{f.origin === "demo" ? "Simulated" : "Live"}</span>
              <time>{new Date(f.last_seen).toLocaleString()}</time>
            </div>
            <h2>{f.title}</h2>
            <p>{f.description}</p>
            {f.snoozed_until ? (
              <p>Snoozed until {new Date(f.snoozed_until).toLocaleString()}</p>
            ) : null}
            <div className="row-actions">
              <InvestigateButton serviceID={f.service_id} findingID={f.id} />
              <button
                onClick={() => setSelected(selected === f.id ? "" : f.id)}
              >
                {selected === f.id ? "Hide evidence" : "View evidence"}
              </button>
              {["new", "snoozed"].includes(f.state) ? (
                <button
                  disabled={action.isPending}
                  onClick={() =>
                    action.mutate({
                      id: f.id,
                      path: "state",
                      body: { state: "acknowledged" },
                    })
                  }
                >
                  Acknowledge
                </button>
              ) : null}
              {["new", "acknowledged"].includes(f.state) ? (
                <button
                  disabled={action.isPending}
                  onClick={() =>
                    action.mutate({
                      id: f.id,
                      path: "state",
                      body: { state: "snoozed", snooze_hours: 24 },
                    })
                  }
                >
                  Snooze 24h
                </button>
              ) : null}
              {["new", "acknowledged", "snoozed"].includes(f.state) ? (
                <>
                  <button
                    disabled={action.isPending}
                    onClick={() =>
                      action.mutate({
                        id: f.id,
                        path: "state",
                        body: { state: "resolved" },
                      })
                    }
                  >
                    Resolve
                  </button>
                  <button
                    disabled={action.isPending}
                    onClick={() =>
                      action.mutate({
                        id: f.id,
                        path: "state",
                        body: { state: "dismissed" },
                      })
                    }
                  >
                    Dismiss
                  </button>
                </>
              ) : null}
            </div>
            {selected === f.id ? (
              <div className="finding-detail">
                <p>
                  Current: {f.details.current.toFixed(3)} · Comparison:{" "}
                  {f.details.baseline.toFixed(3)}
                </p>
                <StoredEvidence ids={f.details.evidence_ids} />
                <details>
                  <summary>Thresholds used</summary>
                  <pre>{JSON.stringify(f.details.thresholds, null, 2)}</pre>
                </details>
                <div className="row-actions">
                  <span>Was this useful?</span>
                  {["useful", "not_useful"].map((v) => (
                    <button
                      key={v}
                      aria-pressed={f.feedback === v}
                      disabled={action.isPending}
                      onClick={() =>
                        action.mutate({
                          id: f.id,
                          path: "feedback",
                          body: { feedback: v },
                        })
                      }
                    >
                      {v === "useful" ? "Useful" : "Not useful"}
                    </button>
                  ))}
                </div>
              </div>
            ) : null}
          </article>
        ))
      ) : (
        <section className="empty-state">
          <h2>No findings in this view.</h2>
          <p>
            A scan needs enough current and comparison data to emit a finding.
          </p>
          <Link className="inline-link" to="/services">
            Review services and run a scan →
          </Link>
        </section>
      )}
      {findings.data?.next_cursor ? (
        <button
          className="text-button"
          onClick={() => setCursor(findings.data!.next_cursor)}
        >
          Next page →
        </button>
      ) : null}
    </>
  );
}
