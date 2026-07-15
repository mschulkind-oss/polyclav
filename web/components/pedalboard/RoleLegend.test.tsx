import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { GateDot, ROLE_NAMES, RoleGlyph } from "@/components/pedalboard/RoleGlyph";
import { RoleLegend } from "@/components/pedalboard/RoleLegend";
import type { Role } from "@/lib/pedalboard/model";

const ALL_ROLES: Role[] = ["time", "shape", "blend", "level"];

describe("RoleGlyph", () => {
  it("renders a decorative pb-glyph svg for every role", () => {
    for (const role of ALL_ROLES) {
      const { container, unmount } = render(<RoleGlyph role={role} />);
      const svg = container.querySelector("svg.pb-glyph");
      expect(svg, role).not.toBeNull();
      expect(svg).toHaveAttribute("viewBox", "0 0 10 10");
      expect(svg).toHaveAttribute("aria-hidden", "true");
      unmount();
    }
  });

  it("draws distinct shapes per role", () => {
    const markup = ALL_ROLES.map((role) => render(<RoleGlyph role={role} />).container.innerHTML);
    expect(new Set(markup).size).toBe(ALL_ROLES.length);
  });
});

describe("GateDot", () => {
  it("renders the gate marker with its tooltip", () => {
    render(<GateDot />);
    expect(screen.getByTitle("Gate — 0 is a true bypass")).toHaveClass("pb-gate-dot");
  });
});

describe("RoleLegend", () => {
  it("explains all four roles plus the gate marker", () => {
    const { container } = render(<RoleLegend />);
    const legend = screen.getByLabelText("Control legend");
    for (const name of Object.values(ROLE_NAMES)) {
      expect(legend).toHaveTextContent(name);
    }
    expect(legend).toHaveTextContent("gate — knob at 0 is a true bypass");
    expect(container.querySelectorAll(".pb-lg-item")).toHaveLength(5);
    expect(container.querySelectorAll("svg.pb-glyph")).toHaveLength(4);
    expect(container.querySelectorAll(".pb-gate-dot")).toHaveLength(1);
  });
});
