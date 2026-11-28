// Panel de administración: nodos, CPU, memoria, latencias y logs en tiempo real.
import { useEffect, useRef, useState } from "react";
import { api, fmt, fmt1, ms } from "./api.js";
import { Lines } from "./charts.jsx";
import { useLive } from "./live.jsx";
import { defineSlice, useSlice } from "./store.js";

const NODE_COLORS = ["var(--series-1)", "var(--series-2)", "var(--series-3)", "var(--series-4)"];
const WINDOW = 90; // muestras (3 minutos a 2 s)

// La última foto y la serie de 3 minutos se acumulan aunque el panel esté cerrado,
// así las gráficas no empiezan de cero cada vez que se vuelve a esta página.
const METRICS = defineSlice("admin.metrics", { snap: null, cpu: {}, lat: [] }, {
  metrics: (h, d) => {
    const t = Date.now();
    const cpu = { ...h.cpu };
    (d.nodes || []).forEach((n) => { cpu[n.id] = [...(cpu[n.id] || []), { t, v: n.cpuPct }].slice(-WINDOW); });
    cpu.coordinator = [...(cpu.coordinator || []), { t, v: d.coordinator.cpuPct }].slice(-WINDOW);
    const lc = d.latencyCluster || {};
    const lat = [...h.lat, { t, p50: lc.p50Ms || 0, p95: lc.p95Ms || 0, rate: d.predictPerSec || 0 }].slice(-WINDOW);
    return { snap: d, cpu, lat };
  },
});

export function AdminPage() {
  const [{ snap, cpu, lat }] = useSlice(METRICS);
  const hist = { cpu, lat };
  const [logs, setLogs] = useState([]);
  const [level, setLevel] = useState("");
  const [metrics, setMetrics] = useState(null);
  const box = useRef(null);

  useLive("logs", (e) => setLogs((l) => [...l, e].slice(-400)));

  useEffect(() => {
    api(`/api/v1/admin/logs?limit=200${level ? `&level=${level}` : ""}`).then((d) => setLogs(d.entries)).catch(() => {});
  }, [level]);
  useEffect(() => {
    const load = () => api("/api/v1/metrics").then(setMetrics).catch(() => {});
    load();
    const id = setInterval(load, 10000);
    return () => clearInterval(id);
  }, []);
  useEffect(() => { if (box.current) box.current.scrollTop = box.current.scrollHeight; }, [logs]);

  const nodeIds = Object.keys(hist.cpu).filter((k) => k !== "coordinator").sort();
  const cpuSeries = [
    ...nodeIds.map((id, i) => ({ name: id, color: NODE_COLORS[i % NODE_COLORS.length], points: hist.cpu[id] })),
    { name: "coordinador", color: "#6b7785", points: hist.cpu.coordinator || [] },
  ];
  const shown = level ? logs.filter((l) => l.level === level || (level === "" && l.level !== "acceso")) : logs.filter((l) => l.level !== "acceso");
  const st = metrics && metrics.storage;
  return (
    <>
      <div className="head">
        <div>
          <h1>Clúster y logs</h1>
          <p>Estado de los nodos ML y del coordinador, actualizado cada 2 segundos por WebSocket.</p>
        </div>
      </div>
      <div className="stack">
        <div className="kpis">
          <div className="kpi"><span>Nodos conectados</span><b>{snap ? `${snap.nodes.length} de ${snap.nodesExpected}` : "—"}</b><small>{snap ? `${snap.nodes.filter((n) => n.ready).length} con el modelo vigente` : "esperando datos"}</small></div>
          <div className="kpi"><span>Predicciones por segundo</span><b>{snap ? fmt1(snap.predictPerSec) : "—"}</b><small>{snap ? `${fmt(snap.predictTotal)} en total` : ""}</small></div>
          <div className="kpi"><span>Latencia p95 (clúster)</span><b>{snap && snap.latencyCluster ? ms(snap.latencyCluster.p95Ms) : "—"}</b><small>{snap && snap.latencyCluster ? `p50 ${ms(snap.latencyCluster.p50Ms)}, ${fmt1(snap.latencyCluster.pctUnder100ms)} % bajo 100 ms` : "sin consultas aún"}</small></div>
          <div className="kpi"><span>Aciertos de caché</span><b>{snap ? `${fmt1(snap.cacheHitPct)} %` : "—"}</b><small>Redis</small></div>
          <div className="kpi"><span>Rechazos de seguridad</span><b>{snap ? fmt(snap.denied + snap.rateLimited) : "—"}</b><small>{snap ? `${fmt(snap.denied)} sin permiso (401/403), ${fmt(snap.rateLimited)} por cuota o intentos (429)` : ""}</small></div>
        </div>
        <section className="panel">
          <h2>Nodos ML</h2>
          <p className="sub">CPU en % de un núcleo (100 = un núcleo completo), memoria residente y avance del entrenamiento.</p>
          <div className="tbl-wrap">
            <table>
              <thead><tr><th>Nodo</th><th>Estado</th><th className="n">CPU</th><th className="n">Memoria</th><th className="n">Goroutines</th><th className="n">Núcleos</th><th className="n">Árboles</th><th className="n">Denuncias</th><th className="n">Último latido</th></tr></thead>
              <tbody>
                {(snap ? snap.nodes : []).map((n) => (
                  <tr key={n.id}>
                    <td><b>{n.id}</b></td>
                    <td>{n.training ? `${n.phase} (${n.treesDone}/${n.treesTotal})` : n.ready ? <span className="ok">listo, modelo v{n.version}</span> : n.trained ? "modelo anterior" : "sin modelo"}</td>
                    <td className="n">{fmt1(n.cpuPct)} %</td><td className="n">{fmt(n.rssMB)} MB</td><td className="n">{n.goroutines}</td><td className="n">{n.numCPU}</td>
                    <td className="n">{n.trees}</td><td className="n">{fmt(n.trainRecords)}</td><td className="n">hace {fmt1(n.lastSeenS)} s</td>
                  </tr>
                ))}
              </tbody>
            </table>
            {!snap && <p className="hint">Esperando la primera foto del clúster…</p>}
          </div>
        </section>
        <div className="grid g2">
          <section className="panel">
            <h2>Uso de CPU</h2>
            <p className="sub">Últimos 3 minutos. Entrene un modelo para ver subir los nodos.</p>
            <Lines series={cpuSeries} unit=" %" />
          </section>
          <section className="panel">
            <h2>Latencia de predicción (clúster)</h2>
            <p className="sub">Percentiles de las últimas consultas que fueron al clúster; el límite del enunciado es 100 ms.</p>
            <Lines series={[
              { name: "p50", color: "var(--series-1)", points: hist.lat.map((p) => ({ t: p.t, v: p.p50 })) },
              { name: "p95", color: "var(--series-2)", points: hist.lat.map((p) => ({ t: p.t, v: p.p95 })) },
            ]} unit=" ms" />
          </section>
        </div>
        <div className="grid g-side">
          <section className="panel">
            <div style={{ display: "flex", justifyContent: "space-between", alignItems: "flex-end", gap: 10, flexWrap: "wrap" }}>
              <div>
                <h2>Logs del clúster</h2>
                <p className="sub" style={{ marginBottom: 8 }}>Coordinador y nodos; los nuevos llegan por WebSocket.</p>
              </div>
              <label className="f">Nivel
                <select value={level} onChange={(e) => setLevel(e.target.value)}>
                  <option value="">eventos</option><option value="warn">advertencias</option><option value="error">errores</option><option value="acceso">accesos HTTP</option>
                </select>
              </label>
            </div>
            <div className="logs" ref={box} aria-live="polite">
              {shown.map((l) => (
                <div key={l.seq} className={l.level}>
                  <span className="muted">{new Date(l.time).toLocaleTimeString("es-PE")}</span> <span className="src">{l.source}</span> {l.text}
                </div>
              ))}
              {shown.length === 0 && <div className="muted">Sin líneas para este filtro.</div>}
            </div>
          </section>
          <section className="panel">
            <h2>Bases de datos</h2>
            <p className="sub">{st ? st.mode : "—"}</p>
            {st && (
              <table>
                <tbody>
                  <tr><td>MongoDB</td><td>{st.mongo}</td></tr>
                  <tr><td>Redis</td><td>{st.redis} ({fmt(st.cacheKeys)} claves)</td></tr>
                  {Object.entries(st.collections || {}).map(([k, v]) => <tr key={k}><td className="muted">{k}</td><td className="n">{fmt(v)}</td></tr>)}
                </tbody>
              </table>
            )}
            {metrics && (
              <p className="hint" style={{ marginTop: 10 }}>WebSocket: {metrics.websocket.clients} clientes, {fmt(metrics.websocket.messagesSent)} mensajes enviados, {fmt(metrics.websocket.messagesDropped)} descartados por clientes lentos.</p>
            )}
          </section>
        </div>
      </div>
    </>
  );
}
