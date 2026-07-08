import type { Metadata } from "next";
import type { ReactNode } from "react";
import "./globals.css";

export const metadata: Metadata = {
  title: "polyclav",
  description: "polyclav dashboard — patches, params, mastering, velocity, config",
};

export default function RootLayout({ children }: { children: ReactNode }) {
  return (
    <html lang="en">
      <body>
        <nav className="topnav">
          <a href="/app/">Dashboard</a>
          <a href="/app/midi-probe/">MIDI Probe</a>
        </nav>
        {children}
      </body>
    </html>
  );
}
