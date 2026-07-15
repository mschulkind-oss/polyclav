import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { PatchBar } from "@/components/pedalboard/PatchBar";
import { PAD_SLOTS, PATCHES } from "@/lib/pedalboard/model";

describe("PatchBar", () => {
  it("renders one option per patch plus inert empty slots", () => {
    const { container } = render(<PatchBar patches={PATCHES} activeIx={0} onSelect={() => {}} />);
    expect(screen.getAllByRole("option")).toHaveLength(PATCHES.length);
    expect(container.querySelectorAll(".pb-pad")).toHaveLength(PAD_SLOTS);
    const empties = container.querySelectorAll(".pb-pad-empty");
    expect(empties).toHaveLength(PAD_SLOTS - PATCHES.length);
    for (const empty of empties) {
      expect(empty.tagName).toBe("SPAN"); // not a button — nothing to click
      expect(empty).toHaveAttribute("aria-hidden", "true");
    }
  });

  it("marks only the active pad selected, filled with its patch color", () => {
    render(<PatchBar patches={PATCHES} activeIx={2} onSelect={() => {}} />);
    const options = screen.getAllByRole("option");
    options.forEach((option, ix) => {
      expect(option).toHaveAttribute("aria-selected", ix === 2 ? "true" : "false");
    });
    expect(options[2]?.className).toContain("pb-pad-active");
    expect(options[2]?.getAttribute("style")).toContain(PATCHES[2].color);
  });

  it("clicking a pad selects it by index", () => {
    const onSelect = vi.fn();
    render(<PatchBar patches={PATCHES} activeIx={0} onSelect={onSelect} />);
    fireEvent.click(screen.getByRole("option", { name: "Drawbar Organ" }));
    expect(onSelect).toHaveBeenCalledTimes(1);
    expect(onSelect).toHaveBeenCalledWith(3);
  });

  it("shows the active patch name and its type chip", () => {
    render(<PatchBar patches={PATCHES} activeIx={1} onSelect={() => {}} />);
    expect(screen.getByText("Rhodes Mk I")).toHaveClass("pb-patch-name");
    expect(screen.getByText("soundfont")).toHaveClass("pb-chip");
  });
});
