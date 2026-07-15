import { render } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { Led } from "@/components/pedalboard/Led";

describe("Led", () => {
  it("renders the accent LED when on", () => {
    const { container } = render(<Led on />);
    const led = container.querySelector(".pb-led");
    expect(led).not.toBeNull();
    expect(led?.className).not.toContain("pb-led-off");
  });

  it("adds the off modifier when off", () => {
    const { container } = render(<Led on={false} />);
    expect(container.querySelector(".pb-led.pb-led-off")).not.toBeNull();
  });

  it("is hidden from the accessibility tree (state lives on the stomp)", () => {
    const { container } = render(<Led on />);
    expect(container.querySelector(".pb-led")).toHaveAttribute("aria-hidden", "true");
  });
});
