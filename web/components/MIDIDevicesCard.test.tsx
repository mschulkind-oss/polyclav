import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { allowEntryFor, allowMatches, MIDIDevicesCard } from "@/components/MIDIDevicesCard";
import { api } from "@/lib/api";
import type { MIDIDevicesResponse } from "@/lib/types";

vi.mock("@/lib/api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/api")>();
  return {
    ...actual,
    api: { midiDevices: vi.fn(), midiDevicesPut: vi.fn() },
  };
});

const midiDevices = vi.mocked(api.midiDevices);
const midiDevicesPut = vi.mocked(api.midiDevicesPut);

const CASIO = "CASIO USB-MIDI:CASIO USB-MIDI MIDI 1 36:0";

function respond(body: Partial<MIDIDevicesResponse>) {
  midiDevices.mockResolvedValue({ devices: [], allow: [], ...body });
}

beforeEach(() => {
  vi.clearAllMocks();
  midiDevicesPut.mockResolvedValue(new Response(null, { status: 200 }));
});

describe("allowEntryFor", () => {
  it("drops the volatile trailing ALSA address so an entry survives a replug", () => {
    expect(allowEntryFor(CASIO)).toBe("CASIO USB-MIDI:CASIO USB-MIDI MIDI 1");
  });

  it("leaves a name with no address suffix alone, trailing digits included", () => {
    expect(allowEntryFor("Yamaha P-125")).toBe("Yamaha P-125");
    expect(allowEntryFor("Launchkey MK4 61 MIDI In")).toBe("Launchkey MK4 61 MIDI In");
  });

  it("falls back to the full name rather than producing an empty entry", () => {
    expect(allowEntryFor("36:0")).toBe("36:0");
  });
});

describe("allowMatches", () => {
  it("is a case-insensitive substring test, and never a wildcard", () => {
    expect(allowMatches("casio usb-midi", CASIO)).toBe(true);
    expect(allowMatches("Yamaha", CASIO)).toBe(false);
    expect(allowMatches("", CASIO)).toBe(false);
  });
});

describe("MIDIDevicesCard", () => {
  it("warns loudly when nothing is selected — the fresh-install default", async () => {
    respond({ devices: [{ name: "Yamaha P-125", status: "unselected" }] });
    render(<MIDIDevicesCard />);
    expect(
      await screen.findByText(/No MIDI devices are selected — nothing will make sound\./),
    ).toBeInTheDocument();
    expect(screen.getByRole("checkbox")).not.toBeChecked();
    expect(screen.getByText("not selected")).toBeInTheDocument();
  });

  it("drops the warning and checks the box once a device is selected", async () => {
    respond({
      devices: [{ name: "Yamaha P-125", status: "notes" }],
      allow: ["Yamaha"],
    });
    render(<MIDIDevicesCard />);
    expect(await screen.findByText("sending notes")).toBeInTheDocument();
    expect(screen.getByRole("checkbox")).toBeChecked();
    expect(screen.queryByText(/nothing will make sound/)).not.toBeInTheDocument();
  });

  it("selects by the trimmed port name, not the address-suffixed one", async () => {
    respond({ devices: [{ name: CASIO, status: "unselected" }] });
    render(<MIDIDevicesCard />);
    fireEvent.click(await screen.findByRole("checkbox"));
    fireEvent.click(screen.getByRole("button", { name: "Apply (session)" }));
    await waitFor(() =>
      expect(midiDevicesPut).toHaveBeenCalledWith(["CASIO USB-MIDI:CASIO USB-MIDI MIDI 1"], false),
    );
  });

  it("deselecting removes every entry that matched, not just an exact name", async () => {
    // A hand-written substring entry covers this port. Unchecking has to
    // clear it, or the box would tick straight back on next render.
    respond({
      devices: [{ name: "Yamaha P-125", status: "notes" }],
      allow: ["yamaha"],
    });
    render(<MIDIDevicesCard />);
    fireEvent.click(await screen.findByRole("checkbox"));
    expect(screen.getByRole("checkbox")).not.toBeChecked();
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(midiDevicesPut).toHaveBeenCalledWith([], true));
  });

  it("keeps DAW and loopback ports checkable, with a warning chip", async () => {
    // The heuristics are advisory under an allowlist: deliberately
    // selecting a DAW port is a documented OSC workflow.
    respond({
      devices: [
        { name: "Launchkey MK4 61 DAW In", status: "daw" },
        { name: "Midi Through Port-0", status: "loopback" },
      ],
    });
    render(<MIDIDevicesCard />);
    expect(await screen.findByText("DAW control surface — not keys")).toBeInTheDocument();
    expect(screen.getByText("loopback port — not a keyboard")).toBeInTheDocument();
    for (const box of screen.getAllByRole("checkbox")) {
      expect(box).toBeEnabled();
    }
  });

  it("surfaces selected-but-unplugged entries so Save can't silently drop them", async () => {
    respond({
      devices: [{ name: "Yamaha P-125", status: "notes" }],
      allow: ["Yamaha", "CASIO USB-MIDI"],
    });
    render(<MIDIDevicesCard />);
    expect(await screen.findByText(/Also selected, but not connected right now/)).toHaveTextContent(
      '"CASIO USB-MIDI"',
    );
  });
});
