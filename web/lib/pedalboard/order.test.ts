import { expect, test } from "vitest";
import { moveBy, moveRelative, normalizeOrder } from "@/lib/pedalboard/order";

const BASE = ["drive", "chorus", "trem", "delay", "comp", "reverb"];

test("moveRelative drops before the target", () => {
  expect(moveRelative(BASE, "reverb", "chorus", false)).toEqual([
    "drive",
    "reverb",
    "chorus",
    "trem",
    "delay",
    "comp",
  ]);
});

test("moveRelative after the last target lands at the end", () => {
  expect(moveRelative(BASE, "drive", "reverb", true)).toEqual([
    "chorus",
    "trem",
    "delay",
    "comp",
    "reverb",
    "drive",
  ]);
});

test("moveRelative onto itself is a no-op", () => {
  expect(moveRelative(BASE, "trem", "trem", false)).toBe(BASE);
});

test("moveBy shifts and clamps at the ends", () => {
  expect(moveBy(BASE, "drive", 1)).toEqual(["chorus", "drive", "trem", "delay", "comp", "reverb"]);
  expect(moveBy(BASE, "drive", -1)).toBe(BASE); // already first
  expect(moveBy(BASE, "reverb", 1)).toBe(BASE); // already last
  expect(moveBy(BASE, "comp", -2)).toEqual(["drive", "chorus", "comp", "trem", "delay", "reverb"]);
});

test("normalizeOrder keeps known ids, drops unknown, appends missing", () => {
  expect(normalizeOrder(["reverb", "ghost", "drive"], BASE)).toEqual([
    "reverb",
    "drive",
    "chorus",
    "trem",
    "delay",
    "comp",
  ]);
  expect(normalizeOrder([], BASE)).toEqual(BASE);
});
