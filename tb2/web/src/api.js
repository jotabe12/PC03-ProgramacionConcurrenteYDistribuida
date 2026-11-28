// Cliente de la API: agrega el token JWT, interpreta los errores de la API y
// avisa a la app cuando el token vence (401) para volver al inicio de sesión.
const KEY = "tb2.sesion";

export function loadSession() {
  try {
    const s = JSON.parse(sessionStorage.getItem(KEY) || "null");
    if (s && s.token && new Date(s.expiresAt) > new Date()) return s;
  } catch (_) { /* almacenamiento no disponible */ }
  return null;
}

export function saveSession(s) {
  try {
    if (s) sessionStorage.setItem(KEY, JSON.stringify(s));
    else sessionStorage.removeItem(KEY);
  } catch (_) { /* sin almacenamiento: la sesión vive solo en memoria */ }
}

let onUnauthorized = () => {};
export function setUnauthorizedHandler(fn) { onUnauthorized = fn; }

export class ApiError extends Error {
  constructor(status, body) {
    super((body && body.error) || `Error ${status}`);
    this.status = status;
    this.code = body && body.code;
    this.hint = body && body.hint;
  }
}

export async function api(path, { method = "GET", body, token } = {}) {
  const headers = { Accept: "application/json" };
  if (body !== undefined) headers["Content-Type"] = "application/json";
  const s = token ? { token } : loadSession();
  if (s && s.token) headers.Authorization = `Bearer ${s.token}`;
  let res;
  try {
    res = await fetch(path, { method, headers, body: body === undefined ? undefined : JSON.stringify(body) });
  } catch (e) {
    throw new ApiError(0, { error: "No hay conexión con el servidor. Revise que los contenedores estén levantados.", code: "NETWORK" });
  }
  const text = await res.text();
  let data = null;
  try { data = text ? JSON.parse(text) : null; } catch (_) { data = { error: text }; }
  if (!res.ok) {
    if (res.status === 401 && !path.endsWith("/auth/login")) onUnauthorized();
    throw new ApiError(res.status, data);
  }
  return data;
}

export const CLASSES = [
  "Violencia contra la mujer e IGF", "Hurto", "Robo", "Estafa", "Extorsión", "Secuestro",
];
// Color fijo por modalidad (paleta categórica validada, orden fijo: nunca por ranking).
export const CLASS_COLOR = {
  "Violencia contra la mujer e IGF": "var(--series-1)",
  "Hurto": "var(--series-2)",
  "Robo": "var(--series-3)",
  "Estafa": "var(--series-4)",
  "Extorsión": "var(--series-5)",
  "Secuestro": "var(--series-6)",
};
export const STREET = new Set(["Hurto", "Robo", "Extorsión", "Secuestro"]);
export const MONTHS = ["enero", "febrero", "marzo", "abril", "mayo", "junio", "julio", "agosto", "septiembre", "octubre", "noviembre", "diciembre"];
export const MONTHS_SHORT = ["ene", "feb", "mar", "abr", "may", "jun", "jul", "ago", "sep", "oct", "nov", "dic"];

const nf = new Intl.NumberFormat("es-PE");
const nf1 = new Intl.NumberFormat("es-PE", { maximumFractionDigits: 1, minimumFractionDigits: 1 });
export const fmt = (v) => (v == null || Number.isNaN(v) ? "—" : nf.format(Math.round(v)));
export const fmt1 = (v) => (v == null || Number.isNaN(v) ? "—" : nf1.format(v));
const nfd = (d) => new Intl.NumberFormat("es-PE", { maximumFractionDigits: d, minimumFractionDigits: d });
export const pct = (v, d = 1) => (v == null ? "—" : `${nfd(d).format(100 * v)} %`);
export const pctRaw = (v, d = 1) => (v == null ? "—" : `${nfd(d).format(v)} %`);
export const ms = (v) => (v == null ? "—" : v < 10 ? `${nfd(2).format(v)} ms` : `${fmt(v)} ms`);
// Nombres de distrito en tipo oración: "SAN MARTIN DE PORRES" -> "San Martin de Porres".
const SMALL = new Set(["de", "del", "la", "las", "los", "el", "y"]);
export const title = (s) => (s || "").toLowerCase().split(" ").map((w, i) =>
  i > 0 && SMALL.has(w) ? w : w.replace(/(^|[(-])([a-záéíóúñ])/g, (m, a, b) => a + b.toUpperCase())).join(" ");
