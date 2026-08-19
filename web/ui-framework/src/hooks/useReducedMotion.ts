import { useMediaQuery } from './useMediaQuery';

/**
 * Whether the user has asked for reduced motion — reactively.
 *
 * This exists because the equivalent hook in `motion`/framer-motion is NOT
 * reactive: it reads the media query once at mount and never updates, with a
 * literal `TODO` where the listener should be. That is invisible in a demo and
 * a real defect in an operations panel, which is typically left open for hours
 * across a user changing their OS accessibility settings — exactly the moment
 * the preference needs to take effect.
 *
 * NOTE ON USAGE
 * -------------
 * Most components should NOT call this. The framework's motion tokens already
 * respond to the preference in CSS via `--stratum-motion-scale` /
 * `-distance` / `-zoom`, which halves durations and zeroes translation and
 * scale without any JavaScript. Reach for this hook only where a decision
 * cannot be expressed in CSS — for example skipping a JS-driven scroll
 * animation, or choosing not to start a canvas render loop at all.
 */
export function useReducedMotion(): boolean {
  return useMediaQuery('(prefers-reduced-motion: reduce)');
}
