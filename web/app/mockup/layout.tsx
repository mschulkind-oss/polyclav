import type { Metadata } from "next";
import type { ReactNode } from "react";

// Order matters: the foundation sheet first, then every builder's
// .extra.css (they layer on top and may override for specificity).
import "@/components/pedalboard/pedalboard.css";
import "@/components/pedalboard/chrome.extra.css";
import "@/components/pedalboard/composer.extra.css";
import "@/components/pedalboard/synth.extra.css";

export const metadata: Metadata = {
  title: "polyclav — design mockup",
};

/**
 * Layout for the hidden design playground at /app/mockup. Owns the design
 * system's stylesheets and the .pb-root scale/token root; the page sets
 * --pb-scale on it via the A−/A+ control. Deliberately NOT linked from the
 * main app's nav.
 */
export default function MockupLayout({ children }: { children: ReactNode }) {
  return <div className="pb-root">{children}</div>;
}
