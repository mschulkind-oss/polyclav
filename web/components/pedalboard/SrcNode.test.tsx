import { render, screen } from "@testing-library/react";
import { expect, test } from "vitest";
import { SrcNode } from "@/components/pedalboard/SrcNode";

test("renders the synth source card with led, glyph and voice count", () => {
  const { container } = render(<SrcNode />);
  const card = container.querySelector(".pb-srcnode");
  expect(card).not.toBeNull();
  expect(screen.getByRole("heading", { name: "Synth" })).toBeInTheDocument();
  expect(card?.querySelector(".pb-led")).not.toBeNull();
  expect(card?.querySelector(".pb-src-glyph")).not.toBeNull();
  expect(screen.getByText("8 voices · poly")).toHaveClass("pb-src-sub");
});
