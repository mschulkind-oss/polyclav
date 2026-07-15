/**
 * Accent LED (reference `.led`). Color and breathe animation come from
 * pedalboard.css: the LED reads the card's `--pb-accent`, and cards toggle
 * `.pb-bypassed` to dim it. The `on` prop covers LEDs rendered outside a
 * stateful card — `pb-led-off` (chrome.extra.css) reproduces the off look.
 */
export interface LedProps {
  on: boolean;
}

export function Led({ on }: LedProps) {
  return <span className={on ? "pb-led" : "pb-led pb-led-off"} aria-hidden="true" />;
}
