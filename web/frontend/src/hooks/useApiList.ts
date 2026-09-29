import { useCallback, useEffect, useRef, useState } from "react";
import type { Dispatch, SetStateAction } from "react";

interface ApiList<T> {
  /** null until the first successful load, then always an array. */
  items: T[] | null;
  setItems: Dispatch<SetStateAction<T[] | null>>;
  loading: boolean;
  error: string | null;
  reload: () => void;
  /** Re-fetch with no spinner, leaving what is on screen if it fails — for useResume. */
  refresh: () => void;
  /** Take a whole set the server pushed, which is newer than any fetch still in flight. */
  replace: (items: T[]) => void;
}

// Refetches whenever the URL changes; pass url=null to defer (e.g. a missing route param).
export function useApiList<T>(
  url: string | null,
  errorMessage: string,
): ApiList<T> {
  const [items, setItems] = useState<T[] | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const seq = useRef(0);
  const load = useCallback(
    (silent: boolean) => {
      if (!url) return;
      const id = ++seq.current;
      if (!silent) {
        setLoading(true);
        setError(null);
      }
      fetch(url)
        .then((r) => {
          if (!r.ok) throw new Error(errorMessage);
          return r.json();
        })
        .then((data: T[] | null) => {
          if (seq.current !== id) return;
          setItems(data || []);
          setError(null);
        })
        .catch(() => {
          if (seq.current === id && !silent) setError(errorMessage);
        })
        .finally(() => {
          // Clears a spinner this request superseded, whether or not it was the one that raised it.
          if (seq.current === id) setLoading(false);
        });
    },
    [url, errorMessage],
  );

  const reload = useCallback(() => load(false), [load]);
  const refresh = useCallback(() => load(true), [load]);
  const replace = useCallback((next: T[]) => {
    seq.current++;
    setItems(next);
    setLoading(false);
    setError(null);
  }, []);

  useEffect(() => {
    reload();
    return () => {
      seq.current++;
    };
  }, [reload]);

  return { items, setItems, loading, error, reload, refresh, replace };
}
