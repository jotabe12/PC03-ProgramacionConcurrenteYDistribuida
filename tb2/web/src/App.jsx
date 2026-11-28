// SPA: inicio de sesión, menú por rol y enrutamiento por hash (#/impacto, ...).
import { useEffect, useState } from "react";
import { api, loadSession, saveSession, setUnauthorizedHandler } from "./api.js";
import { LiveProvider, useLiveStatus } from "./live.jsx";
import { resetStore } from "./store.js";
import { ImpactPage, PatrolPage } from "./impacto.jsx";
import { PredictPage } from "./predict.jsx";
import { TrainPage, EvalPage } from "./modelo.jsx";
import { AdminPage } from "./admin.jsx";

const ROUTES = [
  { path: "impacto", label: "Impacto social", group: "Planificación", page: ImpactPage },
  { path: "patrullaje", label: "Plan de patrullaje", group: "Planificación", page: PatrolPage },
  { path: "prediccion", label: "Predicción por distrito", group: "Planificación", page: PredictPage },
  { path: "evaluacion", label: "Evaluación del modelo", group: "Modelo", page: EvalPage },
  { path: "entrenamiento", label: "Entrenamiento", group: "Modelo", page: TrainPage, admin: true },
  { path: "admin", label: "Clúster y logs", group: "Administración", page: AdminPage, admin: true },
];

function useHash() {
  const read = () => location.hash.replace(/^#\/?/, "") || "impacto";
  const [h, setH] = useState(read);
  useEffect(() => {
    const on = () => setH(read());
    addEventListener("hashchange", on);
    return () => removeEventListener("hashchange", on);
  }, []);
  return h;
}

function Logo() {
  return (
    <svg width="34" height="34" viewBox="0 0 32 32" aria-hidden="true">
      <rect width="32" height="32" rx="6" fill="#1d4a6e" />
      <path d="M8 22 L16 8 L24 22 Z" fill="none" stroke="#f2c14e" strokeWidth="3" strokeLinejoin="round" />
      <circle cx="16" cy="18" r="2" fill="#f2c14e" />
    </svg>
  );
}

function Login({ onLogin }) {
  const [username, setUser] = useState("");
  const [password, setPass] = useState("");
  const [error, setError] = useState(null);
  const [busy, setBusy] = useState(false);
  const submit = async (e) => {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      const r = await api("/api/v1/auth/login", { method: "POST", body: { username, password } });
      const s = { token: r.token, expiresAt: r.expiresAt, user: r.user };
      saveSession(s);
      onLogin(s);
    } catch (err) {
      setError(err.status === 429 ? `${err.message}.` : err.status === 401 ? "Usuario o contraseña incorrectos." : err.message);
    } finally {
      setBusy(false);
    }
  };
  return (
    <div className="login">
      <div className="side">
        <div>
          <div className="brand"><Logo /><strong>Patrullaje preventivo<small>Lima Metropolitana y Callao</small></strong></div>
          <h1 style={{ marginTop: 40 }}>Dónde y para qué delitos reforzar el patrullaje cada mes</h1>
          <p>Un Random Forest distribuido en Go, entrenado en un clúster de nodos con 1,8 millones de denuncias policiales (SIDPOL, 2018-2025), estima el riesgo de delitos de calle de los 50 distritos y lo cruza con la población del INEI.</p>
        </div>
        <ul>
          <li>Ranking de distritos y población cubierta por el patrullaje priorizado</li>
          <li>Predicción por distrito y mes, verificada con las denuncias reales de 2026</li>
          <li>Entrenamiento y métricas del clúster en tiempo real</li>
        </ul>
        <p className="team">CC65 Programación Concurrente y Distribuida, Grupo 3: Karim Samanamud, Tomás Pastor, Juan Diego Sánchez.</p>
      </div>
      <form onSubmit={submit} aria-label="Iniciar sesión">
        <h2>Iniciar sesión</h2>
        <label>Usuario<input type="text" autoComplete="username" value={username} onChange={(e) => setUser(e.target.value)} required autoFocus /></label>
        <label>Contraseña<input type="password" autoComplete="current-password" value={password} onChange={(e) => setPass(e.target.value)} required /></label>
        {error && <p className="error" role="alert">{error}</p>}
        <button className="btn" type="submit" disabled={busy}>{busy ? "Verificando…" : "Entrar"}</button>
        <p className="hint">Roles: «admin» entrena y ve el clúster; «analista» consulta el impacto, el plan y las predicciones.</p>
      </form>
    </div>
  );
}

function LiveBadge() {
  const s = useLiveStatus();
  return <span className="live"><i className={`dot ${s === "en vivo" ? "on" : s === "desconectado" ? "off" : ""}`} />{s === "en vivo" ? "Tiempo real conectado" : s === "desconectado" ? "Tiempo real desconectado" : "Conectando…"}</span>;
}

function Shell({ session, onLogout }) {
  const hash = useHash();
  const isAdmin = session.user.role === "admin";
  const routes = ROUTES.filter((r) => !r.admin || isAdmin);
  const route = routes.find((r) => r.path === hash) || routes[0];
  const Page = route.page;
  let lastGroup = null;
  useEffect(() => { document.title = `${route.label} · Patrullaje preventivo`; }, [route]);
  // Una ruta inexistente o sin permiso (p. ej. #/admin como analista) se corrige en la barra de direcciones.
  useEffect(() => { if (hash !== route.path) location.replace(`#/${route.path}`); }, [hash, route]);
  return (
    <div className="shell">
      <nav className="nav" aria-label="Menú principal">
        <div className="brand"><Logo /><strong>Patrullaje preventivo<small>Lima y Callao</small></strong></div>
        <ul>
          {routes.map((r) => {
            const head = r.group !== lastGroup ? <li className="group" key={`g-${r.group}`}>{r.group}</li> : null;
            lastGroup = r.group;
            return [head, <li key={r.path}><a href={`#/${r.path}`} aria-current={r === route ? "page" : undefined}>{r.label}</a></li>];
          })}
        </ul>
        <div className="foot">
          <LiveBadge />
          <div className="who"><span><b>{session.user.username}</b></span><span className="role">{session.user.role}</span></div>
          <a href="/swagger" target="_blank" rel="noreferrer" style={{ color: "#c9d8e6" }}>Documentación de la API</a>
          <button type="button" className="linkbtn" onClick={onLogout}>Cerrar sesión</button>
        </div>
      </nav>
      <main id="contenido"><Page key={route.path} /></main>
    </div>
  );
}

export default function App() {
  const [session, setSession] = useState(loadSession);
  useEffect(() => {
    setUnauthorizedHandler(() => { saveSession(null); resetStore(); setSession(null); });
  }, []);
  const logout = () => { saveSession(null); resetStore(); setSession(null); };
  if (!session) return <Login onLogin={(s) => { resetStore(); setSession(s); }} />;
  return (
    <LiveProvider token={session.token}>
      <Shell session={session} onLogout={logout} />
    </LiveProvider>
  );
}
