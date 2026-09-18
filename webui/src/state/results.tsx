import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useRef,
  useState,
  type ReactNode,
} from "react";
import type { ViewPayload } from "../lib/session";

// ResultsStore collects every notifications/view payload broadcast on this
// session's /ui/events stream. Payloads are split by the server-computed
// `result` flag: true = data gathered by a tool/task call (the workbench), 
// false = MCP feedback (the notifications feed). The chat timeline still shows
// everything.

interface ResultsStoreValue {
  views: ViewPayload[];
  results: ViewPayload[];
  notifications: ViewPayload[];
  // dashboard: results the user pinned as the composed main UI
  dashboard: ViewPayload[];
  // unpinned: results not pinned to the dashboard (the accumulating feed)
  unpinned: ViewPayload[];
  pin: (seq: number) => void;
  unpin: (seq: number) => void;
  clearPins: () => void;
  append: (v: ViewPayload) => void;
  clear: () => void;
}

const ResultsContext = createContext<ResultsStoreValue | null>(null);

// useResults reads the live results pane store.
export function useResults(): ResultsStoreValue {
  const ctx = useContext(ResultsContext);
  if (!ctx) throw new Error("useResults requires <ResultsProvider>");
  return ctx;
}

const MAX_VIEWS = 100;

export function ResultsProvider({
  token,
  children,
}: {
  token: string;
  children: ReactNode;
}) {
  const [views, setViews] = useState<ViewPayload[]>([]);
  const [pinned, setPinned] = useState<Set<number>>(new Set());
  const latest = useRef<ViewPayload | null>(null);
  const nextSeq = useRef(0);

  const append = useCallback((v: ViewPayload) => {
    v._seq = nextSeq.current++;
    latest.current = v;
    setViews((prev) => [...prev.slice(-(MAX_VIEWS - 1)), v]);
  }, []);

  const clear = useCallback(() => {
    latest.current = null;
    setViews([]);
  }, []);

  const pin = useCallback((seq: number) => {
    setPinned((prev) => {
      const next = new Set(prev);
      next.add(seq);
      return next;
    });
  }, []);

  const unpin = useCallback((seq: number) => {
    setPinned((prev) => {
      const next = new Set(prev);
      next.delete(seq);
      return next;
    });
  }, []);

  const clearPins = useCallback(() => {
    setPinned(new Set());
  }, []);

  useEffect(() => {
    const es = new EventSource(`/ui/events?session=${encodeURIComponent(token)}`);
    es.onmessage = (ev) => {
      try {
        const msg = JSON.parse(ev.data as string) as {
          method?: string;
          params?: ViewPayload;
        };
        if (msg.method === "notifications/view" && msg.params) append(msg.params);
      } catch {
        // non-JSON or non-notification lines are ignored
      }
    };
    return () => es.close();
  }, [token, append]);

  const results = views.filter((v) => v.result !== false);
  const notifications = views.filter((v) => v.result === false);
  const dashboard = results.filter((v) => v._seq != null && pinned.has(v._seq));
  const unpinned = results.filter((v) => v._seq == null || !pinned.has(v._seq));

  return (
    <ResultsContext.Provider
      value={{
        views,
        results,
        notifications,
        dashboard,
        unpinned,
        pin,
        unpin,
        clearPins,
        append,
        clear,
      }}
    >
      {children}
    </ResultsContext.Provider>
  );
}