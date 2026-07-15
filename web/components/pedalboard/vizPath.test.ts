import { expect, test } from "vitest";
import { pathMaxX, polyPath } from "@/components/pedalboard/vizPath";

test("samples every 2 units from 0..w inclusive, y to 2 decimals", () => {
  const d = polyPath(4, (x) => x / 3);
  expect(d).toBe("M0 0.00 L2 0.67 L4 1.33");
});

test("mapX remaps sample x into final coordinates", () => {
  const d = polyPath(
    4,
    () => 13,
    (x) => (x === 0 ? 24 : (x + 24).toFixed(1)),
  );
  expect(d).toBe("M24 13.00 L26.0 13.00 L28.0 13.00");
});

test("pathMaxX finds the largest x across M/L segments", () => {
  expect(pathMaxX("M0 13.00 L144 14.29 L26 13.00")).toBe(144);
  expect(pathMaxX("M0 13 L6.5 4.00 L19.5 22.00 L26 13")).toBe(26);
});
