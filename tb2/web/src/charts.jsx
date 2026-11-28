// Gráficos propios en HTML/SVG (sin librerías): barras horizontales, mapa de
// calor, matriz de confusión y líneas en vivo. Cada marca tiene tooltip (title)
// y los nombres van escritos: el color nunca es el único portador de identidad.
import { fmt, fmt1 } from "./api.js";

// Barras horizontales (una serie). rows: [{label, value, display, color, title}]
export function Bars({ rows, max, color = "var(--seq-500)", format = fmt }) {
  const top = max ?? Math.max(1e-9, ...rows.map((r) => r.value));
  return (
    <div className="bars" role="list">
      {rows.map((r) => (
        <div className="row" role="listitem" key={r.key ?? r.label} title={r.title ?? `${r.label}: ${r.display ?? format(r.value)}`}>
          <span className="lab">{r.swatch && <i className="swatch" style={{ background: r.swatch }} />}{r.label}</span>
          <span className="track">
            <span className="fill" style={{ width: `${Math.max(0.5, (100 * r.value) / top)}%`, background: r.color ?? color, display: "block" }} />
          </span>
          <span className="val">{r.display ?? format(r.value)}</span>
        </div>
      ))}
    </div>
  );
}

// Rampa secuencial azul (una sola tonalidad, de claro a oscuro).
const RAMP = ["#e8f1fc", "#cce3fd", "#9dc8f7", "#6aabf1", "#338ced", "#2370cd", "#1757ac", "#0e3d7e"];
export function seqColor(t) {
  const i = Math.max(0, Math.min(RAMP.length - 1, Math.round(t * (RAMP.length - 1))));
  return RAMP[i];
}
const inkOn = (t) => (t > 0.55 ? "#fff" : "var(--ink)");

// Mapa de calor: filas × columnas con valor por celda.
export function Heatmap({ rows, cols, value, label, tooltip, format = fmt }) {
  let hi = 0;
  rows.forEach((r) => cols.forEach((c) => { const v = value(r, c); if (v != null && v > hi) hi = v; }));
  return (
    <div className="tbl-wrap">
      <table className="heat">
        <thead>
          <tr><th />{cols.map((c) => <th key={c.key}>{c.label}</th>)}</tr>
        </thead>
        <tbody>
          {rows.map((r) => (
            <tr key={r.key}>
              <th className="rowh" title={r.label}>{r.label}</th>
              {cols.map((c) => {
                const v = value(r, c);
                const t = v == null || hi === 0 ? 0 : v / hi;
                return (
                  <td key={c.key} className="cell" title={tooltip ? tooltip(r, c, v) : `${r.label}, ${c.label}: ${format(v)}`}
                    style={{ background: v == null ? "var(--surface-2)" : seqColor(t), color: inkOn(t) }}>
                    {v == null ? "" : label ? label(v) : format(v)}
                  </td>
                );
              })}
            </tr>
          ))}
        </tbody>
      </table>
      <div className="scale"><span>menos</span><i /><span>más</span></div>
    </div>
  );
}

// Matriz de confusión: filas = modalidad real, columnas = predicha; color por % de la fila.
export function Confusion({ classes, matrix }) {
  const short = (c) => (c.startsWith("Violencia") ? "Violencia" : c);
  return (
    <div className="tbl-wrap">
      <table className="heat">
        <thead>
          <tr><th className="rowh">real \ predicha</th>{classes.map((c) => <th key={c}>{short(c)}</th>)}<th>total</th></tr>
        </thead>
        <tbody>
          {matrix.map((row, i) => {
            const tot = row.reduce((a, b) => a + b, 0) || 1;
            return (
              <tr key={classes[i]}>
                <th className="rowh">{short(classes[i])}</th>
                {row.map((v, j) => {
                  const t = v / tot;
                  return (
                    <td key={j} className="cell" style={{ width: 70, background: seqColor(t), color: inkOn(t), outline: i === j ? "2px solid var(--ink)" : "none", outlineOffset: -2 }}
                      title={`Real ${classes[i]}, predicha ${classes[j]}: ${fmt(v)} denuncias (${fmt1(100 * t)} % de la fila)`}>
                      {fmt1(100 * t)}
                    </td>
                  );
                })}
                <td className="cell" style={{ width: 70, color: "var(--ink-2)" }}>{fmt(tot)}</td>
              </tr>
            );
          })}
        </tbody>
      </table>
      <p className="hint" style={{ marginTop: 6 }}>Cada celda: % de las denuncias reales de esa fila. La diagonal (borde) son los aciertos.</p>
    </div>
  );
}

// Líneas en vivo (una escala). series: [{name, color, points: [{t, v}]}]
export function Lines({ series, height = 170, unit = "", yMax }) {
  const W = 560, H = height, L = 40, R = 10, T = 10, B = 22;
  const all = series.flatMap((s) => s.points);
  if (all.length < 2) return <p className="hint">Esperando datos en vivo…</p>;
  const t0 = Math.min(...all.map((p) => p.t)), t1 = Math.max(...all.map((p) => p.t));
  const hi = yMax ?? Math.max(1, ...all.map((p) => p.v)) * 1.1;
  const x = (t) => L + ((t - t0) / Math.max(1, t1 - t0)) * (W - L - R);
  const y = (v) => T + (1 - v / hi) * (H - T - B);
  const ticks = [0, hi / 2, hi];
  return (
    <div>
      {series.length > 1 && (
        <div className="legend">{series.map((s) => <span key={s.name}><i className="swatch" style={{ background: s.color }} />{s.name}</span>)}</div>
      )}
      <svg className="chart" viewBox={`0 0 ${W} ${H}`} width="100%" role="img" aria-label={series.map((s) => s.name).join(", ")}>
        <g className="grid">
          {ticks.map((v) => (
            <g key={v}>
              <line x1={L} x2={W - R} y1={y(v)} y2={y(v)} />
              <text x={L - 6} y={y(v) + 4} textAnchor="end">{v >= 10 ? Math.round(v) : v.toFixed(1)}{unit}</text>
            </g>
          ))}
        </g>
        <text x={L} y={H - 6}>hace {Math.round((t1 - t0) / 1000)} s</text>
        <text x={W - R} y={H - 6} textAnchor="end">ahora</text>
        {series.map((s) => (
          <g key={s.name}>
            <polyline fill="none" stroke={s.color} strokeWidth="2" strokeLinejoin="round"
              points={s.points.map((p) => `${x(p.t).toFixed(1)},${y(p.v).toFixed(1)}`).join(" ")} />
            {s.points.length > 0 && (() => {
              const p = s.points[s.points.length - 1];
              return <circle cx={x(p.t)} cy={y(p.v)} r="4" fill={s.color} stroke="#fff" strokeWidth="2"><title>{`${s.name}: ${fmt1(p.v)}${unit}`}</title></circle>;
            })()}
          </g>
        ))}
      </svg>
    </div>
  );
}
