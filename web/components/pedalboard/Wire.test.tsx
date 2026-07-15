import { render } from "@testing-library/react";
import { expect, test } from "vitest";
import { Wire } from "@/components/pedalboard/Wire";

test("renders the dashed signal wire, hidden from the a11y tree", () => {
  const { container } = render(<Wire />);
  const wire = container.querySelector(".pb-wire");
  expect(wire).not.toBeNull();
  expect(wire).toHaveAttribute("aria-hidden", "true");
});
