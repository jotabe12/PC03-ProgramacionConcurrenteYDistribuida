// Estado de la sesión que sobrevive al cambiar de página del menú.
// Cada página define sus "porciones" (slices) con un valor inicial y, si quiere,
// reductores para los eventos del WebSocket. LiveProvider entrega cada evento con
// dispatchLive() aunque la página no esté abierta, así el feed de predicciones o el
// avance del entrenamiento siguen al día al volver. resetStore() se llama al cerrar
// o iniciar sesión, para que un usuario no vea lo que consultó el anterior.
import { useSyncExternalStore } from "react";

const slices = new Map(); // clave -> { init, value, subs, subscribe, get }
const reducers = new Map(); // tipo de evento -> [{ key, fn }]

export function defineSlice(key, init, on = {}) {
  if (!slices.has(key)) {
    const s = { init, value: init, subs: new Set() };
    s.subscribe = (cb) => { s.subs.add(cb); return () => s.subs.delete(cb); };
    s.get = () => s.value;
    slices.set(key, s);
    for (const [type, fn] of Object.entries(on)) {
      if (!reducers.has(type)) reducers.set(type, []);
      reducers.get(type).push({ key, fn });
    }
  }
  return key;
}

export function setSlice(key, update) {
  const s = slices.get(key);
  const v = typeof update === "function" ? update(s.value) : update;
  if (v !== s.value) {
    s.value = v;
    s.subs.forEach((cb) => cb());
  }
}

// useSlice(clave): como useState, pero el valor vive mientras dure la sesión.
export function useSlice(key) {
  const s = slices.get(key);
  const value = useSyncExternalStore(s.subscribe, s.get);
  return [value, (u) => setSlice(key, u)];
}

export function dispatchLive(type, data, time) {
  (reducers.get(type) || []).forEach(({ key, fn }) => setSlice(key, (v) => fn(v, data, time)));
}

export function resetStore() {
  slices.forEach((s, key) => setSlice(key, s.init));
}
