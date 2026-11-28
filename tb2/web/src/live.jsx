// Conexión WebSocket única para toda la app. Los componentes se suscriben a un
// canal (training, prediction, metrics, logs) con useLive(). Reconecta con espera
// exponencial y expone el estado para el indicador del menú.
import { createContext, useContext, useEffect, useRef, useState } from "react";
import { dispatchLive } from "./store.js";

const LiveCtx = createContext(null);

export function LiveProvider({ token, children }) {
  const [status, setStatus] = useState("conectando");
  const listeners = useRef(new Map());
  const [hello, setHello] = useState(null);

  useEffect(() => {
    if (!token) return undefined;
    let ws = null;
    let closed = false;
    let wait = 1000;
    let timer = null;
    const connect = () => {
      const proto = location.protocol === "https:" ? "wss" : "ws";
      try {
        // El token viaja como subprotocolo: el navegador no permite cabeceras en WebSocket.
        ws = new WebSocket(`${proto}://${location.host}/ws`, ["jwt", token]);
      } catch (_) {
        setStatus("desconectado");
        return;
      }
      setStatus("conectando");
      ws.onopen = () => { setStatus("en vivo"); wait = 1000; };
      ws.onmessage = (ev) => {
        let msg;
        try { msg = JSON.parse(ev.data); } catch (_) { return; }
        if (msg.type === "hello") setHello(msg.data);
        dispatchLive(msg.type, msg.data, msg.time); // estado de sesión (páginas cerradas incluidas)
        const set = listeners.current.get(msg.type);
        if (set) set.forEach((fn) => fn(msg.data, msg.time));
      };
      ws.onclose = () => {
        if (closed) return;
        setStatus("desconectado");
        timer = setTimeout(connect, wait);
        wait = Math.min(wait * 2, 15000);
      };
    };
    connect();
    return () => {
      closed = true;
      clearTimeout(timer);
      if (ws) ws.close();
    };
  }, [token]);

  const value = useRef({
    subscribe(type, fn) {
      if (!listeners.current.has(type)) listeners.current.set(type, new Set());
      listeners.current.get(type).add(fn);
      return () => listeners.current.get(type).delete(fn);
    },
  }).current;

  return <LiveCtx.Provider value={{ ...value, status, hello }}>{children}</LiveCtx.Provider>;
}

export function useLiveStatus() {
  return useContext(LiveCtx).status;
}

// useLive(tipo, callback): el callback más reciente recibe cada evento del canal.
export function useLive(type, fn) {
  const ctx = useContext(LiveCtx);
  const ref = useRef(fn);
  ref.current = fn;
  useEffect(() => ctx.subscribe(type, (d, t) => ref.current(d, t)), [ctx, type]);
}
