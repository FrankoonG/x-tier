import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useState,
  type ReactNode,
} from 'react';
import { useMediaQuery } from '../hooks/useMediaQuery';

/** What the user picked. `'system'` defers to the OS. */
export type ThemePreference = 'light' | 'dark' | 'system';
/** What is actually painted. Always concrete. */
export type ResolvedTheme = 'light' | 'dark';

export interface ThemeContextValue {
  /** The user's choice, including `'system'`. */
  preference: ThemePreference;
  /** The concrete theme in effect right now. */
  theme: ResolvedTheme;
  setPreference: (next: ThemePreference) => void;
  /** Cycles light -> dark -> system. */
  cycle: () => void;
}

const ThemeContext = createContext<ThemeContextValue | null>(null);

export interface ThemeProviderProps {
  children: ReactNode;
  /** Used when nothing is stored yet. Defaults to `'system'`. */
  defaultPreference?: ThemePreference;
  /**
   * localStorage key for persistence. Pass `null` to disable persistence
   * entirely (useful in tests and in the component lab).
   */
  storageKey?: string | null;
  /**
   * Element that carries `data-theme`. Defaults to `document.documentElement`,
   * which is what the token stylesheets are written against.
   */
  element?: HTMLElement | null;
}

function readStored(key: string | null): ThemePreference | null {
  if (!key || typeof localStorage === 'undefined') return null;
  try {
    const v = localStorage.getItem(key);
    return v === 'light' || v === 'dark' || v === 'system' ? v : null;
  } catch {
    // Storage can throw in private mode or under a restrictive policy. A theme
    // preference is never worth breaking the app for.
    return null;
  }
}

/**
 * Owns the `data-theme` attribute.
 *
 * The token stylesheets deliberately do not use `light-dark()`. That function
 * is ignored under `forced-colors`, and — more importantly here — dark-mode
 * browser extensions decide whether a site is already dark by sampling
 * computed background colours; they do not read `color-scheme`. Resolving the
 * preference once in JS and pinning a concrete attribute gives us a state that
 * is inspectable, testable in Playwright, and visible to those extensions.
 *
 * To avoid a flash of the wrong theme before hydration, inline
 * {@link themeInitScript} in `<head>`.
 */
export function ThemeProvider({
  children,
  defaultPreference = 'system',
  storageKey = 'stratum-theme',
  element,
}: ThemeProviderProps) {
  const [preference, setPreferenceState] = useState<ThemePreference>(
    () => readStored(storageKey) ?? defaultPreference,
  );

  const prefersDark = useMediaQuery('(prefers-color-scheme: dark)');
  const theme: ResolvedTheme =
    preference === 'system' ? (prefersDark ? 'dark' : 'light') : preference;

  useEffect(() => {
    const target = element ?? (typeof document !== 'undefined' ? document.documentElement : null);
    if (!target) return;
    target.setAttribute('data-theme', theme);
    // Mirrored onto `color-scheme` so form controls, scrollbars and the
    // canvas behind the page follow the theme rather than the OS.
    target.style.colorScheme = theme;
  }, [theme, element]);

  const setPreference = useCallback(
    (next: ThemePreference) => {
      setPreferenceState(next);
      if (!storageKey || typeof localStorage === 'undefined') return;
      try {
        localStorage.setItem(storageKey, next);
      } catch {
        /* see readStored */
      }
    },
    [storageKey],
  );

  const cycle = useCallback(() => {
    setPreference(
      preference === 'light' ? 'dark' : preference === 'dark' ? 'system' : 'light',
    );
  }, [preference, setPreference]);

  const value = useMemo<ThemeContextValue>(
    () => ({ preference, theme, setPreference, cycle }),
    [preference, theme, setPreference, cycle],
  );

  return <ThemeContext.Provider value={value}>{children}</ThemeContext.Provider>;
}

export function useTheme(): ThemeContextValue {
  const ctx = useContext(ThemeContext);
  if (!ctx) throw new Error('useTheme must be used inside a <ThemeProvider>.');
  return ctx;
}

/**
 * Inline this in `<head>`, before any stylesheet, to set the theme attribute
 * before first paint:
 *
 * ```html
 * <script>__STRATUM_THEME_INIT__</script>
 * ```
 *
 * Without it the page paints light, then corrects itself once React mounts —
 * a visible flash for dark-mode users.
 */
export const themeInitScript = `(function(){try{
var k='stratum-theme';var p=localStorage.getItem(k)||'system';
var d=p==='dark'||(p==='system'&&matchMedia('(prefers-color-scheme: dark)').matches);
var t=d?'dark':'light';
document.documentElement.setAttribute('data-theme',t);
document.documentElement.style.colorScheme=t;
}catch(e){}})();`;
