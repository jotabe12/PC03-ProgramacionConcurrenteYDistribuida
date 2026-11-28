// Predicción interactiva para un distrito y mes + flujo en vivo de predicciones.
import { useEffect, useState } from "react";
import { api, fmt, fmt1, ms, MONTHS, CLASSES, CLASS_COLOR, STREET, title } from "./api.js";
import { Bars } from "./charts.jsx";
import { defineSlice, useSlice } from "./store.js";

// Se conservan al cambiar de página: el formulario, el último resultado y el feed
// (que sigue llenándose por WebSocket aunque la página no esté abierta).
const FORM = defineSlice("predict.form", { district: "SAN JUAN DE LURIGANCHO", mes: 10, anio: 2026 });
const RESULT = defineSlice("predict.result", null);
const FEED = defineSlice("predict.feed", [], {
  prediction: (f, d, t) => [{ ...d, t }, ...f].slice(0, 12),
});

export function useDistricts() {
  const [list, setList] = useState([]);
  useEffect(() => {
    api("/api/v1/districts").then((d) => setList(d.districts)).catch(() => setList([]));
  }, []);
  return list;
}

export function PredictPage() {
  const districts = useDistricts();
  const [form, setForm] = useSlice(FORM);
  const [res, setRes] = useSlice(RESULT);
  const [feed] = useSlice(FEED);
  const { district, mes, anio } = form;
  const setDistrict = (v) => setForm((f) => ({ ...f, district: v }));
  const setMes = (v) => setForm((f) => ({ ...f, mes: v }));
  const setAnio = (v) => setForm((f) => ({ ...f, anio: v }));
  const [error, setError] = useState(null);
  const [busy, setBusy] = useState(false);

  // Solo se consulta al pulsar «Predecir»: entrar a la página no genera predicciones.
  const run = async (e) => {
    if (e) e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      setRes(await api("/api/v1/predict", { method: "POST", body: { district, mes, anio } }));
    } catch (err) {
      setError(err);
    } finally {
      setBusy(false);
    }
  };

  return (
    <>
      <div className="head">
        <div>
          <h1>Predicción por distrito</h1>
          <p>Probabilidad de cada modalidad de delito para un distrito y mes, según el bosque distribuido. La segunda vez que se repite una consulta, la respuesta sale de la caché de Redis.</p>
        </div>
      </div>
      <form className="controls" onSubmit={run} style={{ marginBottom: 16 }}>
        <label className="f">Distrito
          <select value={district} onChange={(e) => setDistrict(e.target.value)} style={{ minWidth: 240 }}>
            {districts.length === 0 && <option value={district}>{title(district)}</option>}
            {districts.map((d) => <option key={d.district} value={d.district}>{title(d.district)} ({d.zone})</option>)}
          </select>
        </label>
        <label className="f">Mes
          <select value={mes} onChange={(e) => setMes(+e.target.value)}>
            {MONTHS.map((m, i) => <option key={m} value={i + 1}>{m}</option>)}
          </select>
        </label>
        <label className="f">Año
          <input type="number" min="2018" max="2033" value={anio} onChange={(e) => setAnio(+e.target.value)} />
        </label>
        <button className="btn" type="submit" disabled={busy}>{busy ? "Consultando…" : "Predecir"}</button>
      </form>
      {error && <p className="error">{error.message}{error.hint ? `. ${error.hint}` : ""}</p>}
      <div className="grid g-side">
        {!res ? (
          <section className="panel">
            <h2>Sin consulta todavía</h2>
            <p className="hint">Elija el distrito, el mes y el año, y pulse «Predecir». El resultado se conserva al cambiar de página.</p>
          </section>
        ) : (
          <div className="stack">
            <section className="panel">
              <h2>{title(res.district)}, {MONTHS[res.mes - 1]} de {res.anio}</h2>
              <p className="sub">
                {res.zone}, {fmt(res.population)} habitantes (INEI){" "}
                {res.fromCache ? <span className="tag cache">desde caché Redis</span> : <span className="tag">{res.nodesUsed} nodos, {res.treesUsed} árboles</span>}{" "}
                <span className="tag">respuesta en {ms(res.latencyMs)}</span>
              </p>
              <div className="kpis" style={{ marginBottom: 14 }}>
                {res.top3.map((c, i) => (
                  <div className="kpi" key={c.class}>
                    <span>{i === 0 ? "Más probable" : `Opción ${i + 1}`}</span>
                    <b>{fmt1(100 * c.probability)} %</b>
                    <small><i className="swatch" style={{ background: CLASS_COLOR[c.class] }} />{c.class}</small>
                  </div>
                ))}
              </div>
              <h3>Probabilidad por modalidad</h3>
              <p className="sub">Modelo (barra) frente a lo ocurrido en el distrito en los 12 meses previos ({res.recentWindow}).</p>
              <Bars max={1} rows={CLASSES.map((c) => {
                const p = res.probabilities.find((x) => x.class === c)?.probability ?? 0;
                const h = res.recentMix.find((x) => x.class === c)?.probability ?? 0;
                return { label: c, value: p, color: CLASS_COLOR[c], swatch: CLASS_COLOR[c], display: `${fmt1(100 * p)} %`,
                  title: `${c}: modelo ${fmt1(100 * p)} %, últimos 12 meses ${fmt1(100 * h)} %${STREET.has(c) ? " (delito de calle)" : ""}` };
              })} />
              <p className="hint" style={{ marginTop: 10 }}>
                Delito de calle más probable: <b>{res.topStreetCrime}</b> ({fmt1(100 * res.topStreetProbability)} %). Modelo v{res.modelVersion}, variables «{res.featureSet}».
              </p>
            </section>
            <section className="panel">
              <h3>Franja horaria orientativa</h3>
              <p className="note" style={{ margin: "6px 0 12px" }}>{res.franjaNote}</p>
              <Bars max={1} color="var(--seq-300)" rows={res.franjaSupuesta.map((f) => ({ label: f.franja, value: f.probability, display: `${fmt1(100 * f.probability)} %` }))} />
            </section>
          </div>
        )}
          <section className="panel">
            <h2>Predicciones en vivo</h2>
            <p className="sub">Consultas de todos los usuarios, recibidas por WebSocket.</p>
            {feed.length === 0 ? <p className="hint">Aún no llegan consultas en esta sesión. Las predicciones de cualquier usuario aparecen aquí al momento.</p> : (
              <ul className="feed">
                {feed.map((f, i) => (
                  <li key={i}>
                    <span><b>{title(f.district)}</b> {String(f.mes).padStart(2, "0")}/{f.anio}<br /><span className="muted">{f.user}: {f.topClass}</span></span>
                    <span className="num" style={{ textAlign: "right" }}>{ms(f.latencyMs)}<br />{f.fromCache ? <span className="tag cache">caché</span> : <span className="tag">clúster</span>}</span>
                  </li>
                ))}
              </ul>
            )}
          </section>
      </div>
    </>
  );
}
