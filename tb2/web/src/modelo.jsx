// Entrenamiento con parámetros desde la UI (admin) y evaluación del modelo.
import { useEffect, useState } from "react";
import { api, fmt, fmt1, ms, pct, CLASS_COLOR } from "./api.js";
import { Bars, Confusion } from "./charts.jsx";
import { useLive } from "./live.jsx";

const DEFAULTS = { trees: 32, workers: 0, nodes: 3, maxDepth: 12, minLeaf: 50, mtry: 3, sampleFrac: 0.3, featureSet: "tb2", classWeight: "none", mode: "final" };

const FEATURE_SETS = {
  tb2: "tb2: variables reales + patrón de los 12 meses previos (modelo del TB2)",
  base: "base: distrito, zona, mes y año",
  pc4: "pc4: incluye la franja imputada (solo para medir la fuga de la PC4)",
};

function Num({ label, name, form, set, min, max, step = 1, hint }) {
  return (
    <label className="f" title={hint}>{label}
      <input type="number" min={min} max={max} step={step} value={form[name]}
        onChange={(e) => set({ ...form, [name]: e.target.value === "" ? "" : Number(e.target.value) })} />
    </label>
  );
}

export function TrainPage() {
  const [form, setForm] = useState(DEFAULTS);
  const [busy, setBusy] = useState(false);
  const [job, setJob] = useState(null);
  const [error, setError] = useState(null);
  const [nodes, setNodes] = useState({});
  const [events, setEvents] = useState([]);

  useLive("training", (d, t) => {
    if (d.event === "progreso") {
      setNodes((n) => ({ ...n, [d.node]: d }));
    } else {
      if (d.event === "inicio") setNodes({});
      setEvents((e) => [{ ...d, t }, ...e].slice(0, 8));
    }
  });

  const submit = async (e) => {
    e.preventDefault();
    setBusy(true);
    setError(null);
    setJob(null);
    setNodes({});
    try {
      setJob(await api("/api/v1/train", { method: "POST", body: form }));
    } catch (err) {
      setError(err);
    } finally {
      setBusy(false);
    }
  };

  const changesActive = form.mode !== "final" || form.featureSet !== "tb2";
  return (
    <>
      <div className="head">
        <div>
          <h1>Entrenamiento distribuido</h1>
          <p>Configure el Random Forest y repártalo entre los nodos del clúster. Cada nodo entrena su bloque de árboles sobre su parte de los datos y reporta el avance en tiempo real.</p>
        </div>
      </div>
      <div className="grid g-side">
        <form className="panel" onSubmit={submit}>
          <h2>Parámetros</h2>
          <p className="sub">Por defecto: los valores de la PC3 con las variables del TB2.</p>
          <div className="controls" style={{ marginBottom: 12 }}>
            <Num label="Árboles" name="trees" form={form} set={setForm} min={1} max={512} hint="Total del bosque; se reparte en bloques entre los nodos" />
            <Num label="Nodos" name="nodes" form={form} set={setForm} min={1} max={16} hint="Cuántos nodos ML usar (los datos se reparten en N partes)" />
            <Num label="Goroutines por nodo" name="workers" form={form} set={setForm} min={0} max={64} hint="0 = todos los núcleos del nodo" />
            <Num label="Profundidad máx." name="maxDepth" form={form} set={setForm} min={1} max={24} />
            <Num label="Mín. por hoja" name="minLeaf" form={form} set={setForm} min={1} max={100000} />
            <Num label="mtry" name="mtry" form={form} set={setForm} min={1} max={11} hint="Variables candidatas por división" />
            <Num label="Fracción de muestra" name="sampleFrac" form={form} set={setForm} min={0.05} max={1} step={0.05} />
          </div>
          <div className="controls" style={{ marginBottom: 12 }}>
            <label className="f">Variables
              <select value={form.featureSet} onChange={(e) => setForm({ ...form, featureSet: e.target.value })}>
                {Object.entries(FEATURE_SETS).map(([k, v]) => <option key={k} value={k}>{v}</option>)}
              </select>
            </label>
            <label className="f">Pesos por clase
              <select value={form.classWeight} onChange={(e) => setForm({ ...form, classWeight: e.target.value })}>
                <option value="none">ninguno</option>
                <option value="sqrt">raíz del inverso de la frecuencia</option>
                <option value="balanced">inverso de la frecuencia</option>
              </select>
            </label>
            <label className="f">Partición temporal
              <select value={form.mode} onChange={(e) => setForm({ ...form, mode: e.target.value })}>
                <option value="final">final: entrena 2018-2025, evalúa 2026</option>
                <option value="validacion">validación: entrena 2018-2024, evalúa 2025</option>
              </select>
            </label>
          </div>
          {changesActive && <p className="note" style={{ marginBottom: 12 }}>Este entrenamiento reemplaza al modelo vigente. Para el panel de impacto, vuelva a entrenar con «tb2» en modo final al terminar.</p>}
          <div style={{ display: "flex", gap: 10 }}>
            <button className="btn" type="submit" disabled={busy}>{busy ? "Entrenando…" : "Entrenar"}</button>
            <button className="btn ghost" type="button" onClick={() => setForm(DEFAULTS)} disabled={busy}>Restablecer valores</button>
          </div>
          {error && <p className="error" style={{ marginTop: 12 }}>{error.message}</p>}
        </form>
        <section className="panel">
          <h2>Avance por nodo</h2>
          <p className="sub">Eventos «training» recibidos por WebSocket.</p>
          {Object.keys(nodes).length === 0 && !busy && <p className="hint">Inicie un entrenamiento para ver el avance de cada nodo.</p>}
          <div className="stack" style={{ gap: 12 }}>
            {Object.values(nodes).sort((a, b) => a.node.localeCompare(b.node)).map((n) => (
              <div key={n.node}>
                <div style={{ display: "flex", justifyContent: "space-between", fontSize: 13.5 }}>
                  <b>{n.node}</b><span className="muted">{n.phase}: {n.done} de {n.total} árboles, {fmt(n.records)} denuncias</span>
                </div>
                <div className="progress" aria-label={`${n.node}: ${n.done} de ${n.total}`}><i style={{ width: `${(100 * n.done) / Math.max(1, n.total)}%` }} /></div>
              </div>
            ))}
          </div>
          {events.length > 0 && (
            <ul className="feed" style={{ marginTop: 14 }}>
              {events.map((e, i) => (
                <li key={i}><span>{e.event === "inicio" ? `Inicio v${e.version}: ${e.trees} árboles en ${e.nodes.length} nodos (${e.featureSet}, ${e.mode})` :
                  e.event === "fin" ? `Fin v${e.version}: ${ms(e.durationMs)}, exactitud ${pct(e.accuracy)}` : `Error en ${e.node}: ${e.error}`}</span></li>
              ))}
            </ul>
          )}
        </section>
      </div>
      {job && (
        <section className="panel" style={{ marginTop: 16 }}>
          <h2>Resultado: modelo v{job.version}</h2>
          <p className="sub">{job.totalTrees} árboles, {job.nodeCount} nodos, {fmt(job.trainRecords)} denuncias de entrenamiento ({job.trainFrom}-{job.trainTo}), variables «{job.featureSet}». Tiempo total en el coordinador: {ms(job.durationMs)}.</p>
          <div className="tbl-wrap">
            <table>
              <thead><tr><th>Nodo</th><th className="n">Shard</th><th className="n">Árboles</th><th className="n">Denuncias</th><th className="n">Goroutines</th><th className="n">Carga CSV</th><th className="n">Desagregación</th><th className="n">Entrenamiento</th><th className="n">Ida y vuelta</th></tr></thead>
              <tbody>
                {job.nodes.map((n) => (
                  <tr key={n.nodeId}>
                    <td>{n.nodeId}</td><td className="n">{n.result.shard + 1}/{n.result.shards}</td><td className="n">{n.result.trees}</td>
                    <td className="n">{fmt(n.result.trainRecords)}</td><td className="n">{n.result.workers}</td><td className="n">{n.result.loadMs === 0 ? "ya cargado" : ms(n.result.loadMs)}</td>
                    <td className="n">{n.result.cachedMatrix ? "en caché" : ms(n.result.expandMs)}</td><td className="n">{ms(n.result.trainMs)}</td><td className="n">{ms(n.roundTripMs)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          {job.evaluation && (
            <div className="kpis" style={{ marginTop: 14 }}>
              <div className="kpi"><span>Exactitud {job.evaluation.evalYear}</span><b>{pct(job.evaluation.accuracy)}</b></div>
              <div className="kpi"><span>Top-3</span><b>{pct(job.evaluation.top3Accuracy)}</b></div>
              <div className="kpi"><span>F1 macro</span><b>{fmt1(100 * job.evaluation.macroF1)}</b><small>sobre 100</small></div>
              <div className="kpi"><span>Línea base</span><b>{pct(job.evaluation.baselineAccuracy)}</b><small>{job.evaluation.baselineClass}</small></div>
            </div>
          )}
        </section>
      )}
    </>
  );
}

export function EvalPage() {
  const [data, setData] = useState(null);
  const [error, setError] = useState(null);
  useEffect(() => { api("/api/v1/evaluacion").then(setData).catch(setError); }, []);
  if (error) return <p className="error">{error.message}</p>;
  if (!data) return <p className="hint">Cargando la evaluación…</p>;
  const ev = data.current;
  const m = data.model;
  return (
    <>
      <div className="head">
        <div>
          <h1>Evaluación del modelo</h1>
          <p>Partición temporal: el bosque se entrena con años anteriores y se mide con el año siguiente, sin mezclar. Cada denuncia cuenta una vez.</p>
        </div>
      </div>
      {!ev ? <p className="note">El modelo aún no tiene evaluación. Un administrador debe entrenarlo.</p> : (
        <div className="stack">
          <div className="kpis">
            <div className="kpi"><span>Exactitud en {ev.evalYear}</span><b>{pct(ev.accuracy)}</b><small>{fmt(ev.testRecords)} denuncias</small></div>
            <div className="kpi"><span>Top-3</span><b>{pct(ev.top3Accuracy)}</b><small>la modalidad real está entre las 3 más probables</small></div>
            <div className="kpi"><span>F1 macro</span><b>{fmt1(100 * ev.macroF1)}</b><small>promedio simple de las 6 clases</small></div>
            <div className="kpi"><span>Clase mayoritaria</span><b>{pct(ev.baselineAccuracy)}</b><small>siempre «{ev.baselineClass}»</small></div>
            <div className="kpi"><span>Persistencia</span><b>{pct(ev.persistenceAccuracy)}</b><small>la modalidad más frecuente de los 12 meses previos</small></div>
          </div>
          <p className="hint">Modelo v{m.version}: {m.totalTrees} árboles en {m.nodesReady.length} nodos, variables «{ev.featureSet}», modo {m.mode}.</p>
          <div className="grid g2">
            <section className="panel">
              <h2>F1 por modalidad</h2>
              <p className="sub">Equilibrio entre precisión y exhaustividad de cada clase.</p>
              <Bars max={1} rows={ev.perClass.map((c) => ({ label: c.class, value: c.f1, color: CLASS_COLOR[c.class], swatch: CLASS_COLOR[c.class],
                display: fmt1(100 * c.f1), title: `${c.class}: precisión ${pct(c.precision)}, exhaustividad ${pct(c.recall)}, ${fmt(c.real)} reales` }))} />
              <p className="hint" style={{ marginTop: 10 }}>Las modalidades poco frecuentes (estafa, extorsión, secuestro) casi nunca son la más probable de un distrito: por eso su F1 es bajo y el top-3 es la métrica útil para el patrullaje.</p>
            </section>
            <section className="panel">
              <h2>Importancia de las variables</h2>
              <p className="sub">Reducción de impureza de Gini acumulada en los árboles de todos los nodos.</p>
              <Bars max={Math.max(...ev.featureImportance.map((f) => f.importance))} rows={[...ev.featureImportance].sort((a, b) => b.importance - a.importance)
                .map((f) => ({ label: f.feature, value: f.importance, display: `${fmt1(100 * f.importance)} %` }))} />
            </section>
          </div>
          <section className="panel">
            <h2>Matriz de confusión</h2>
            <p className="sub">Denuncias de {ev.evalYear} según su modalidad real (filas) y la predicha por el bosque (columnas).</p>
            <Confusion classes={ev.classes} matrix={ev.confusion} />
          </section>
          <section className="panel">
            <h2>Historial de entrenamientos</h2>
            <p className="sub">Comparación de variables, pesos y partición (el más reciente arriba).</p>
            <div className="tbl-wrap">
              <table>
                <thead><tr><th>Versión</th><th>Variables</th><th>Pesos</th><th>Modo</th><th className="n">Nodos</th><th className="n">Goroutines</th><th className="n">Árboles</th><th className="n">Tiempo</th><th className="n">Exactitud</th><th className="n">Top-3</th><th className="n">F1 macro</th></tr></thead>
                <tbody>
                  {data.history.map((j) => (
                    <tr key={j.jobId}>
                      <td>v{j.version}</td><td>{j.featureSet}</td><td>{j.classWeight}</td><td>{j.mode} ({j.evalYear})</td>
                      <td className="n">{j.nodeCount}</td><td className="n">{j.workers || "todas"}</td><td className="n">{j.totalTrees}</td><td className="n">{ms(j.durationMs)}</td>
                      <td className="n">{pct(j.accuracy, 2)}</td><td className="n">{pct(j.top3Accuracy, 2)}</td><td className="n">{fmt1(100 * j.macroF1)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </section>
        </div>
      )}
    </>
  );
}
