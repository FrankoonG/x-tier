/* Screen-local reads from the versioned domain API. */
import { useCallback, useEffect, useRef, useState } from 'react';
import { describeFailure, type FailureView } from '../api/errors';

export interface DomainRead<T> {
  data: T | null;
  failure: FailureView | null;
  loading: boolean;
  pristine: boolean;
  reload: () => Promise<void>;
}

export function useDomainRead<T>(
  key: string,
  load: () => Promise<T>,
  deps: unknown[] = [],
): DomainRead<T> {
  const [data, setData] = useState<T | null>(null);
  const [failure, setFailure] = useState<FailureView | null>(null);
  const [loading, setLoading] = useState(true);
  const [pristine, setPristine] = useState(true);
  const seq = useRef(0);
  const loadRef = useRef(load);
  loadRef.current = load;

  const reload = useCallback(async () => {
    const ticket = ++seq.current;
    setLoading(true);
    try {
      const result = await loadRef.current();
      if (ticket !== seq.current) return;
      setData(result);
      setFailure(null);
    } catch (err) {
      if (ticket !== seq.current) return;
      setFailure(describeFailure(err));
    } finally {
      if (ticket === seq.current) {
        setLoading(false);
        setPristine(false);
      }
    }
  }, [key]);

  useEffect(() => {
    void reload();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [reload, ...deps]);

  return { data, failure, loading, pristine, reload };
}
