import { render } from "@testing-library/react";
import { expect, test } from "vitest";
import MockupLayout, { metadata } from "@/app/mockup/layout";

test("wraps children in the pb-root token/scale root", () => {
  const { container } = render(
    <MockupLayout>
      <span>playground</span>
    </MockupLayout>,
  );
  const root = container.querySelector(".pb-root");
  expect(root).not.toBeNull();
  expect(root?.textContent).toBe("playground");
});

test("titles the browser tab for the mockup", () => {
  expect(metadata.title).toBe("polyclav — design mockup");
});
