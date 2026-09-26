import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import LaunchkeyDebugPage from "./page";

describe("Launchkey debugger", () => {
  afterEach(() => {
    Reflect.deleteProperty(navigator, "requestMIDIAccess");
  });

  it("listens to both MIDI and DAW ports without opening outputs or sending messages", async () => {
    const handlers = new Map<string, (event: { data: Uint8Array; timeStamp: number }) => void>();
    const input = (name: string) => ({
      name,
      addEventListener: vi.fn(
        (_type: string, cb: (event: { data: Uint8Array; timeStamp: number }) => void) =>
          handlers.set(name, cb),
      ),
      removeEventListener: vi.fn(),
    });
    const midi = input("Launchkey MK4 61 MIDI In");
    const daw = input("Launchkey MK4 61 DAW In");
    const send = vi.fn();
    const access = {
      inputs: new Map([
        ["midi", midi],
        ["daw", daw],
      ]),
      outputs: { send },
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
    };
    const request = vi.fn().mockResolvedValue(access);
    Object.defineProperty(navigator, "requestMIDIAccess", { configurable: true, value: request });
    const { container } = render(<LaunchkeyDebugPage />);
    fireEvent.click(screen.getByRole("button", { name: "Connect MIDI inputs" }));
    await waitFor(() => expect(handlers.size).toBe(2));
    await act(async () => {
      handlers.get(daw.name)?.({ data: Uint8Array.of(0x90, 96, 100), timeStamp: 0 });
      handlers.get(midi.name)?.({ data: Uint8Array.of(0x93, 60, 90), timeStamp: 0 });
    });
    expect(container.querySelector('[data-control="pad-top-1"]')).toHaveClass("held");
    expect(container.querySelector('[data-control="key-60"]')).toHaveClass("held");
    expect(request).toHaveBeenCalledWith({ sysex: false });
    expect(send).not.toHaveBeenCalled();
  });
  it("renders all 61 keys, both pad rows and the photographed controls", () => {
    const { container } = render(<LaunchkeyDebugPage />);
    expect(container.querySelectorAll(".lk-key")).toHaveLength(61);
    expect(container.querySelectorAll(".lk-pad-row [data-control]")).toHaveLength(16);
    expect(container.querySelector('[data-control="fader-button-9"]')).toBeInTheDocument();
    expect(container.querySelector('[data-control="track-left"]')).toBeInTheDocument();
  });

  it("replays a local report and lights a press until release", async () => {
    const { container } = render(<LaunchkeyDebugPage />);
    const report = {
      controls: [
        {
          events: [
            {
              timestamp: "2026-09-26T18:15:00.000Z",
              port_role: "daw",
              kind: "note_on",
              channel: 1,
              raw_values: { note: 96, velocity: 99 },
            },
            {
              timestamp: "2026-09-26T18:15:00.100Z",
              port_role: "daw",
              kind: "note_off",
              channel: 1,
              raw_values: { note: 96 },
            },
          ],
        },
      ],
    };
    const file = new File([JSON.stringify(report)], "inventory.json", { type: "application/json" });
    Object.defineProperty(file, "text", { value: async () => JSON.stringify(report) });
    fireEvent.change(screen.getByLabelText("Load inventory JSON"), { target: { files: [file] } });
    const next = await screen.findByRole("button", { name: "Next event" });
    fireEvent.click(next);
    expect(container.querySelector('[data-control="pad-top-1"]')).toHaveClass("held");
    fireEvent.click(next);
    await waitFor(() =>
      expect(container.querySelector('[data-control="pad-top-1"]')).not.toHaveClass("held"),
    );
  });
});
