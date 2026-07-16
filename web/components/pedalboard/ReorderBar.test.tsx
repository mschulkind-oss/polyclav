import { fireEvent, render, screen } from "@testing-library/react";
import { expect, test, vi } from "vitest";
import { ReorderBar } from "@/components/pedalboard/ReorderBar";
import { CHAIN } from "@/lib/pedalboard/model";

const ENABLED = Object.fromEntries(CHAIN.map((p) => [p.id, true]));
const IDS = CHAIN.map((p) => p.id);

function fakeDataTransfer() {
  const store: Record<string, string> = {};
  return {
    effectAllowed: "",
    setData: (k: string, v: string) => {
      store[k] = v;
    },
    getData: (k: string) => store[k] ?? "",
  };
}

test("renders one chip per pedal in chain order", () => {
  render(<ReorderBar pedals={CHAIN} enabled={ENABLED} onReorder={() => {}} />);
  const labels = screen.getAllByRole("button").map((c) => c.textContent?.replace(/[^A-Za-z]/g, ""));
  expect(labels).toEqual(CHAIN.map((p) => p.label));
});

test("ArrowRight nudges a pedal one slot later", () => {
  const onReorder = vi.fn();
  render(<ReorderBar pedals={CHAIN} enabled={ENABLED} onReorder={onReorder} />);
  fireEvent.keyDown(screen.getAllByRole("button")[0], { key: "ArrowRight" });
  const expected = IDS.slice();
  expected.splice(1, 0, expected.splice(0, 1)[0]);
  expect(onReorder).toHaveBeenCalledWith(expected);
});

test("dragging a chip onto another reorders", () => {
  const onReorder = vi.fn();
  render(<ReorderBar pedals={CHAIN} enabled={ENABLED} onReorder={onReorder} />);
  const chips = screen.getAllByRole("button");
  const dt = fakeDataTransfer();
  fireEvent.dragStart(chips[IDS.indexOf("reverb")], { dataTransfer: dt });
  fireEvent.drop(chips[IDS.indexOf("drive")], { dataTransfer: dt, clientX: 0 });
  // dropped on the left half of "drive" → reverb moves before drive.
  expect(onReorder).toHaveBeenCalledWith(["reverb", "drive", "chorus", "trem", "delay", "comp"]);
});
