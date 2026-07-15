import { fireEvent, render, screen, within } from "@testing-library/react";
import { beforeEach, expect, test } from "vitest";
import MockupPage from "@/app/mockup/page";

beforeEach(() => {
  window.localStorage.clear();
});

/**
 * Renders the page inside a .pb-root wrapper (the layout's job in the app)
 * and returns the four screen sections in tab order: board, editor, synth,
 * macros. jsdom applies no stylesheets, so inactive screens stay queryable —
 * activity is asserted via the pb-active class.
 */
function renderPage() {
  const utils = render(
    <div className="pb-root">
      <MockupPage />
    </div>,
  );
  const screens = Array.from(utils.container.querySelectorAll<HTMLElement>("section.pb-screen"));
  return { ...utils, screens };
}

test("composes the chrome: brand, tabs, patch bar, footer; board starts active", () => {
  const { container, screens } = renderPage();
  expect(container.querySelector(".pb-wordmark")?.textContent).toBe("polyclav");
  expect(container.querySelector(".pb-badge")?.textContent).toBe("Flat Modern");
  const nav = screen.getByRole("navigation", { name: "Screens" });
  expect(
    within(nav)
      .getAllByRole("button")
      .map((b) => b.textContent),
  ).toEqual(["Pedalboard", "Pedal editor", "Synth", "Macros"]);
  const pads = screen.getByRole("listbox", { name: "Patches" });
  expect(within(pads).getAllByRole("option")).toHaveLength(5);
  expect(screens).toHaveLength(4);
  expect(screens[0]).toHaveClass("pb-active");
  for (const s of screens.slice(1)) {
    expect(s).not.toHaveClass("pb-active");
  }
  expect(container.querySelector(".pb-footer")?.textContent).toBe(
    "polyclav · flat modern · component playground · static mock data",
  );
});

test("tabs switch the active screen", () => {
  const { screens } = renderPage();
  const nav = screen.getByRole("navigation", { name: "Screens" });
  fireEvent.click(within(nav).getByRole("button", { name: "Macros" }));
  expect(screens[3]).toHaveClass("pb-active");
  expect(screens[0]).not.toHaveClass("pb-active");
  fireEvent.click(within(nav).getByRole("button", { name: "Synth" }));
  expect(screens[2]).toHaveClass("pb-active");
  expect(screens[3]).not.toHaveClass("pb-active");
});

test("clicking a strip on the board opens the pedal editor screen", () => {
  const { screens } = renderPage();
  fireEvent.click(within(screens[0]).getByRole("button", { name: "Open Chorus in editor" }));
  expect(screens[1]).toHaveClass("pb-active");
  expect(screens[0]).not.toHaveClass("pb-active");
  // breadcrumb goes back to the board
  fireEvent.click(within(screens[1]).getByRole("button", { name: "Pedalboard" }));
  expect(screens[0]).toHaveClass("pb-active");
});

test("editor edits reflect back into the board's delay strip (shared state)", () => {
  const { screens } = renderPage();
  const delayStrip = within(screens[0]).getByRole("button", { name: "Open Delay in editor" });
  const stripTime = () => delayStrip.querySelector(".pb-r-time .pb-p-val")?.textContent;
  expect(stripTime()).toBe("380 ms");
  // ArrowUp steps 1/100 of the 1–1000 ms range: 380 → 389.99 → "390 ms"
  fireEvent.keyDown(within(screens[1]).getByRole("slider", { name: "Time" }), { key: "ArrowUp" });
  expect(stripTime()).toBe("390 ms");
  fireEvent.click(within(screens[1]).getByRole("button", { name: "Reset to defaults" }));
  expect(stripTime()).toBe("380 ms");
});

test("board knob edits flow into the editor knobs (same shared state)", () => {
  const { screens } = renderPage();
  const delayStrip = within(screens[0]).getByRole("button", { name: "Open Delay in editor" });
  const boardTime = within(delayStrip).getByRole("slider", { name: "Time" });
  const editorTime = within(screens[1]).getByRole("slider", { name: "Time" });
  expect(editorTime.getAttribute("aria-valuenow")).toBe("380");
  // ArrowUp steps 1/100 of the 1–1000 ms range: 380 → 389.99
  fireEvent.keyDown(boardTime, { key: "ArrowUp" });
  expect(editorTime.getAttribute("aria-valuenow")).toBe("389.99");
  expect(delayStrip.querySelector(".pb-r-time .pb-p-val")?.textContent).toBe("390 ms");
  // and the drag never opened the editor screen
  expect(screens[0]).toHaveClass("pb-active");
  // the editor's Reset also pulls the board knob back
  fireEvent.click(within(screens[1]).getByRole("button", { name: "Reset to defaults" }));
  expect(boardTime.getAttribute("aria-valuenow")).toBe("380");
});

test("bus knob edits land in the shared mock state", () => {
  const { screens } = renderPage();
  const comp = within(screens[0]).getByRole("slider", { name: "Comp" });
  expect(comp.getAttribute("aria-valuenow")).toBe("35");
  fireEvent.keyDown(comp, { key: "ArrowUp" });
  expect(comp.getAttribute("aria-valuenow")).toBe("36");
  expect(comp.getAttribute("aria-valuetext")).toBe("36%");
});

test("the editor stomp and the board's delay stomp share bypass state", () => {
  const { screens } = renderPage();
  const chip = () => screens[1].querySelector(".pb-screen-head .pb-chip")?.textContent;
  expect(chip()).toBe("Parked — settings kept");
  fireEvent.click(within(screens[1]).getByRole("button", { name: "Parked" }));
  expect(chip()).toBe("Engaged");
  const delayStrip = within(screens[0]).getByRole("button", { name: "Open Delay in editor" });
  expect(delayStrip).not.toHaveClass("pb-bypassed");
  // stomping on the board parks the editor again
  fireEvent.click(within(delayStrip).getByRole("button", { name: "On" }));
  expect(chip()).toBe("Parked — settings kept");
  expect(delayStrip).toHaveClass("pb-bypassed");
});

test("selecting a non-native patch gates the synth screen", () => {
  const { screens } = renderPage();
  expect(within(screens[2]).queryByRole("note")).toBeNull();
  fireEvent.click(screen.getByRole("option", { name: "Rhodes Mk I" }));
  const note = within(screens[2]).getByRole("note");
  expect(note.textContent).toContain("Rhodes Mk I is a soundfont");
  expect(screens[2].querySelector(".pb-synth-body")).toHaveClass("pb-gated");
  fireEvent.click(screen.getByRole("option", { name: "Minimoog" }));
  expect(within(screens[2]).queryByRole("note")).toBeNull();
});

test("A+ / A− drive --pb-scale on the enclosing pb-root (default 1.1)", () => {
  const { container } = renderPage();
  const root = container.querySelector<HTMLElement>(".pb-root");
  expect(root?.style.getPropertyValue("--pb-scale")).toBe("1.1");
  fireEvent.click(screen.getByRole("button", { name: "Larger UI" }));
  expect(root?.style.getPropertyValue("--pb-scale")).toBe("1.2");
  fireEvent.click(screen.getByRole("button", { name: "Smaller UI" }));
  expect(root?.style.getPropertyValue("--pb-scale")).toBe("1.1");
});
