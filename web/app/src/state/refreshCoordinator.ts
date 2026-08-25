export interface RefreshCoordinator {
  refresh: () => Promise<void>;
  refreshFresh: () => Promise<void>;
}

/**
 * Coalesces ordinary reads while allowing a mutation to demand one read that
 * starts after any older read has settled.
 */
export function createRefreshCoordinator(task: () => Promise<void>): RefreshCoordinator {
  let inFlight: Promise<void> | null = null;

  const refresh = (): Promise<void> => {
    if (inFlight) return inFlight;
    const started = Promise.resolve().then(task);
    inFlight = started;
    const clear = () => {
      if (inFlight === started) inFlight = null;
    };
    void started.then(clear, clear);
    return started;
  };

  const refreshFresh = async (): Promise<void> => {
    const older = inFlight;
    if (older) await older.catch(() => undefined);
    return refresh();
  };

  return { refresh, refreshFresh };
}
