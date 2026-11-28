// Panel de impacto social y plan de patrullaje.
import { useEffect, useMemo, useState } from "react";
import { api, fmt, fmt1, pctRaw, MONTHS, MONTHS_SHORT, title, CLASS_COLOR } from "./api.js";
import { Bars, Heatmap } from "./charts.jsx";

const YEARS = [2025, 2026, 2027];

export function useImpact(mes, anio, top, orden) {
  const [state, setState] = useState({ data: null, error: null, loading: true });
  useEffect(() => {
    let alive = true;
    setState((s) => ({ ...s, loading: true, error: null }));
    api(`/api/v1/impacto?mes=${mes}&anio=${anio}&top=${top}&orden=${orden}`)
      .then((data) => alive && setState({ data, error: null, loading: false }))
      .catch((error) => alive && setState({ data: null, error, loading: false }));
    return () => { alive = false; };
  }, [mes, anio, top, orden]);
  return state;
}

export function PeriodControls({ mes, setMes, anio, setAnio, top, setTop, orden, setOrden }) {
  return (
    <div className="controls">
      <label className="f">Mes
        <select value={mes} onChange={(e) => setMes(+e.target.value)}>
          {MONTHS.map((m, i) => <option key={m} value={i + 1}>{m}</option>)}
        </select>
      </label>
      <label className="f">Año
        <select value={anio} onChange={(e) => setAnio(+e.target.value)}>
          {YEARS.map((y) => <option key={y} value={y}>{y}</option>)}
        </select>
      </label>
      <label className="f">Distritos priorizados: {top}
        <input type="range" min="3" max="20" value={top} onChange={(e) => setTop(+e.target.value)} />
      </label>
      <div className="f" style={{ display: "grid", gap: 4, fontSize: 13, color: "var(--ink-2)" }}>Ordenar por
        <div className="seg" role="group" aria-label="Ordenar por">
          <button type="button" aria-pressed={orden === "casos"} onClick={() => setOrden("casos")}>casos esperados</button>
          <button type="button" aria-pressed={orden === "tasa"} onClick={() => setOrden("tasa")}>tasa por 100 mil</button>
        </div>
      </div>
    </div>
  );
}

function Answer({ d }) {
  const s = d.summary;
  const mes = MONTHS[d.mes - 1];
  const bt = d.backtest;
  return (
    <section className="answer" aria-label="Respuesta a la pregunta de impacto">
      <p className="q">{d.question}</p>
      <p className="a">
        En {mes} de {d.anio}, conviene priorizar <strong>{s.topN} distritos</strong>: concentran el{" "}
        <strong>{pctRaw(s.expectedCoveragePct, 0)}</strong> de los delitos de calle esperados y cubren a{" "}
        <strong>{fmt1(s.populationTop / 1e6)} millones</strong> de habitantes ({pctRaw(s.populationPct, 0)} de Lima y Callao).
      </p>
      <div className="facts">
        <span><b>{title(d.rows[0].district)}</b>primer distrito priorizado</span>
        <span><b>{fmt(s.expectedStreetTop)}</b>delitos de calle esperados en esos distritos</span>
        {bt && <span><b>{pctRaw(bt.realCoveragePct, 1)}</b>de los delitos de calle reales del mes cayeron en ellos (óptimo posible {pctRaw(bt.oracleCoveragePct, 1)})</span>}
        {bt && <span><b>{bt.overlapAtN} de {s.topN}</b>coinciden con el top real del mes</span>}
        {!bt && <span><b>pronóstico</b>mes sin datos reales todavía (historia {d.historyFrom} a {d.historyTo})</span>}
      </div>
    </section>
  );
}

function RankTable({ rows, showActual }) {
  return (
    <div className="tbl-wrap">
      <table>
        <thead>
          <tr>
            <th className="n">#</th><th>Distrito</th><th>Zona</th><th className="n">Calle esperados</th><th className="n">Tasa x 100 mil</th>
            <th className="n">Población</th><th>Delito de calle probable</th><th>Franja sugerida (supuesto)</th>
            {showActual && <th className="n">Calle reales</th>}{showActual && <th className="n">Puesto real</th>}
          </tr>
        </thead>
        <tbody>
          {rows.map((r) => (
            <tr key={r.district}>
              <td className="n">{r.rank}</td>
              <td>{title(r.district)}</td>
              <td>{r.zone}</td>
              <td className="n">{fmt(r.expectedStreet)}</td>
              <td className="n">{fmt1(r.ratePer100k)}</td>
              <td className="n">{fmt(r.population)}</td>
              <td><i className="swatch" style={{ background: CLASS_COLOR[r.topStreetCrime] }} />{r.topStreetCrime} <span className="muted">{fmt1(100 * r.topStreetProbability)} %</span></td>
              <td><span className="tag assume" title="Supuesto horario de la PC3: el CSV no registra la hora">{r.franjaSugerida}</span></td>
              {showActual && <td className="n">{fmt(r.actualStreet)}</td>}
              {showActual && <td className="n">{r.actualRank}</td>}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

// Mapa de calor distrito × mes: delitos de calle esperados por el modelo (12 consultas, cacheadas en Redis).
function YearHeat({ anio, orden }) {
  const [months, setMonths] = useState(null);
  const [all, setAll] = useState(false);
  const [error, setError] = useState(null);
  useEffect(() => {
    let alive = true;
    setMonths(null);
    Promise.all(MONTHS.map((_, i) => api(`/api/v1/impacto?mes=${i + 1}&anio=${anio}&top=10&orden=${orden}`)))
      .then((res) => alive && setMonths(res))
      .catch((e) => alive && setError(e));
    return () => { alive = false; };
  }, [anio, orden]);
  const rows = useMemo(() => {
    if (!months) return [];
    const byD = new Map();
    months.forEach((m, i) => m.rows.forEach((r) => {
      const e = byD.get(r.district) || { key: r.district, label: title(r.district), v: Array(12).fill(null), real: Array(12).fill(null), sum: 0 };
      e.v[i] = orden === "tasa" ? r.ratePer100k : r.expectedStreet;
      e.real[i] = r.actualStreet ?? null;
      e.sum += e.v[i];
      byD.set(r.district, e);
    }));
    return [...byD.values()].sort((a, b) => b.sum - a.sum);
  }, [months, orden]);
  if (error) return <p className="error">{error.message}</p>;
  if (!months) return <p className="hint">Calculando los 12 meses en el clúster…</p>;
  const shown = all ? rows : rows.slice(0, 15);
  const cols = MONTHS_SHORT.map((m, i) => ({ key: i, label: m }));
  return (
    <div>
      <Heatmap rows={shown} cols={cols} value={(r, c) => r.v[c.key]}
        format={orden === "tasa" ? fmt1 : fmt}
        tooltip={(r, c, v) => `${r.label}, ${MONTHS[c.key]} ${anio}: ${orden === "tasa" ? fmt1(v) + " por 100 mil" : fmt(v) + " delitos de calle esperados"}` +
          (r.real[c.key] != null ? ` (reales: ${fmt(r.real[c.key])})` : "")} />
      <button type="button" className="btn ghost" style={{ marginTop: 10 }} onClick={() => setAll(!all)}>
        {all ? "Mostrar los 15 con más riesgo" : `Mostrar los ${rows.length} distritos`}
      </button>
    </div>
  );
}

export function ImpactPage() {
  const [mes, setMes] = useState(3);
  const [anio, setAnio] = useState(2026);
  const [top, setTop] = useState(10);
  const [orden, setOrden] = useState("casos");
  const { data, error, loading } = useImpact(mes, anio, top, orden);
  return (
    <>
      <div className="head">
        <div>
          <h1>Impacto social</h1>
          <p>Dónde concentrar el patrullaje preventivo de delitos de calle en Lima Metropolitana y Callao, con el riesgo que estima el Random Forest distribuido y la población del INEI.</p>
        </div>
      </div>
      <div className="stack">
        <PeriodControls {...{ mes, setMes, anio, setAnio, top, setTop, orden, setOrden }} />
        {error && <p className="error">{error.message}{error.hint ? `. ${error.hint}` : ""}</p>}
        {loading && !data && <p className="hint">Consultando a los nodos del clúster…</p>}
        {data && (
          <>
            <Answer d={data} />
            <div className="grid g-side">
              <section className="panel">
                <h2>Distritos priorizados</h2>
                <p className="sub">{orden === "tasa" ? "Delitos de calle esperados por cada 100 mil habitantes" : "Delitos de calle esperados en el mes"} {data.fromCache ? <span className="tag cache">desde caché Redis</span> : <span className="tag">calculado por {data.nodesUsed} nodos, {data.treesUsed} árboles</span>}</p>
                <Bars rows={data.rows.slice(0, top).map((r) => ({
                  label: `${r.rank}. ${title(r.district)}`, key: r.district,
                  value: orden === "tasa" ? r.ratePer100k : r.expectedStreet,
                  display: orden === "tasa" ? fmt1(r.ratePer100k) : fmt(r.expectedStreet),
                  title: `${title(r.district)}: ${fmt(r.expectedStreet)} esperados, ${fmt1(r.ratePer100k)} por 100 mil, ${fmt(r.population)} hab.`,
                }))} />
              </section>
              <section className="panel">
                <h2>Cómo se calcula</h2>
                <ul className="sub" style={{ paddingLeft: 18, display: "grid", gap: 6 }}>
                  <li>Para cada distrito, el clúster estima la probabilidad de cada modalidad en el mes (Random Forest de {data.treesUsed} árboles en {data.nodesUsed} nodos).</li>
                  <li>Delitos de calle esperados = promedio mensual de denuncias del distrito ({data.historyFrom} a {data.historyTo}) × probabilidad de hurto, robo, extorsión o secuestro.</li>
                  <li>Tasa = esperados ÷ población proyectada del INEI × 100 000.</li>
                  {data.backtest && <li>Verificación: el mes ya ocurrió, así que se compara con las denuncias reales.</li>}
                </ul>
                <p className="note"><b>Franja horaria:</b> el CSV no registra la hora del hecho. La franja sugerida usa el supuesto de la PC3 y no es una predicción del modelo.</p>
              </section>
            </div>
            <section className="panel">
              <h2>Ranking completo</h2>
              <p className="sub">Los {data.rows.length} distritos ordenados por {orden === "tasa" ? "tasa" : "casos esperados"}{data.backtest ? "; a la derecha, lo que ocurrió en realidad" : ""}.</p>
              <RankTable rows={data.rows.slice(0, Math.max(top, 20))} showActual={!!data.backtest} />
            </section>
            <section className="panel">
              <h2>Riesgo mes a mes en {anio}</h2>
              <p className="sub">{orden === "tasa" ? "Tasa de delitos de calle esperada por 100 mil habitantes" : "Delitos de calle esperados"} por distrito y mes. Pase el cursor por una celda para ver el dato real cuando existe.</p>
              <YearHeat anio={anio} orden={orden} />
            </section>
          </>
        )}
      </div>
    </>
  );
}

export function PatrolPage() {
  const [mes, setMes] = useState(10);
  const [anio, setAnio] = useState(2026);
  const [top, setTop] = useState(8);
  const [orden, setOrden] = useState("casos");
  const [state, setState] = useState({ data: null, error: null });
  useEffect(() => {
    let alive = true;
    setState({ data: null, error: null });
    api(`/api/v1/patrullaje?mes=${mes}&anio=${anio}&top=${top}&orden=${orden}`)
      .then((data) => alive && setState({ data, error: null }))
      .catch((error) => alive && setState({ data: null, error }));
    return () => { alive = false; };
  }, [mes, anio, top, orden]);
  const d = state.data;
  return (
    <>
      <div className="head">
        <div>
          <h1>Plan de patrullaje</h1>
          <p>Hoja de trabajo para el mes elegido: distritos en orden de prioridad, el delito de calle a vigilar y la franja sugerida.</p>
        </div>
        <button type="button" className="btn ghost no-print" onClick={() => window.print()}>Imprimir plan</button>
      </div>
      <div className="stack">
        <PeriodControls {...{ mes, setMes, anio, setAnio, top, setTop, orden, setOrden }} />
        {state.error && <p className="error">{state.error.message}</p>}
        {!d && !state.error && <p className="hint">Preparando el plan…</p>}
        {d && (
          <>
            <p className="note"><b>Plan de {MONTHS[mes - 1]} de {anio}.</b> {d.summary.topN} distritos, {fmt(d.summary.expectedStreetTop)} delitos de calle esperados ({pctRaw(d.summary.expectedCoveragePct, 0)} del total) y {fmt1(d.summary.populationTop / 1e6)} millones de habitantes. La franja es un supuesto (el CSV no trae la hora).</p>
            <div className="plan">
              {d.rows.map((r) => (
                <article key={r.district} style={{ borderLeftColor: CLASS_COLOR[r.topStreetCrime] }}>
                  <h3><span>{r.rank}. {title(r.district)}</span><span className="tag">{r.zone}</span></h3>
                  <dl>
                    <dt>Vigilar</dt><dd><b>{r.topStreetCrime}</b> ({fmt1(100 * r.topStreetProbability)} %)</dd>
                    <dt>Esperados</dt><dd className="num">{fmt(r.expectedStreet)} delitos de calle</dd>
                    <dt>Tasa</dt><dd className="num">{fmt1(r.ratePer100k)} por 100 mil</dd>
                    <dt>Población</dt><dd className="num">{fmt(r.population)}</dd>
                    <dt>Franja (supuesto)</dt><dd><span className="tag assume">{r.franjaSugerida}</span></dd>
                    {r.actualStreet != null && <><dt>Real</dt><dd className="num">{fmt(r.actualStreet)} (puesto {r.actualRank})</dd></>}
                  </dl>
                </article>
              ))}
            </div>
          </>
        )}
      </div>
    </>
  );
}
