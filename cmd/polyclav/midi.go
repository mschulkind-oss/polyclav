package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/mschulkind-oss/polyclav/internal/config"
	"github.com/mschulkind-oss/polyclav/internal/midi"
)

// runMIDI dispatches `polyclav midi <subcommand>`. Only `list` exists
// today; the shape leaves room for more without a new top-level verb.
func runMIDI(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "Usage: polyclav midi list")
		return 2
	}
	switch args[0] {
	case "list":
		return runMIDIList(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "polyclav midi: unknown subcommand %q\n\nUsage: polyclav midi list\n", args[0])
		return 2
	}
}

// runMIDIList prints every currently-connected MIDI input port with its
// live classification (sends notes / unselected / DAW / loopback), so
// there is zero guessing about what names/substrings to put in
// [midi].allow_devices or --midi-allow — the single most common friction
// point before this existed (previously `aconnect -l`, ALSA-specific and
// not obviously the right tool).
func runMIDIList(args []string) int {
	fs := flag.NewFlagSet("midi list", flag.ExitOnError)
	configPath := fs.String("config", "", "path to config.toml (default: XDG config dir)")
	_ = fs.Parse(args)

	names, err := midi.PortNames()
	if err != nil {
		fmt.Fprintf(os.Stderr, "polyclav midi list: %v\n", err)
		return 1
	}

	// Best-effort: classify against the real config if one exists, so the
	// report reflects what the running daemon would actually do. A
	// missing/unparsable config falls back to the empty allowlist —
	// which is also the real default, so the report stays honest: nothing
	// sends notes until a device is selected. This is a read-only
	// report, not a startup gate, so a bad config never errors here.
	path := *configPath
	if path == "" {
		if cfgDir, cerr := os.UserConfigDir(); cerr == nil {
			path = defaultConfigPath(cfgDir)
		}
	}
	var allow []string
	if path != "" {
		if cfg, cerr := config.Load(path); cerr == nil {
			allow = cfg.MIDI.AllowDevices
		}
	}

	infos := midi.ClassifyPorts(names, allow)
	if len(infos) == 0 {
		fmt.Println("No MIDI input ports found.")
		return 0
	}
	fmt.Println("MIDI input ports:")
	for _, info := range infos {
		fmt.Printf("  %-10s %s\n", midiStatusLabel(info.Status), info.Name)
	}
	fmt.Println()
	if len(allow) == 0 {
		fmt.Println("NOTHING IS SELECTED: [midi].allow_devices is empty, so no keyboard")
		fmt.Println("sends notes. It is an allowlist — name a device to hear it.")
		fmt.Println()
	}
	fmt.Println("Put a stable substring of the names above in config.toml's")
	fmt.Println(`[midi].allow_devices, or --midi-allow "name one,name two" for a`)
	fmt.Println("one-off override. Matching skips the trailing ALSA address, so it")
	fmt.Println("survives a replug/reboot even if that address changes.")
	return 0
}

// midiStatusLabel is padded to the %-10s column runMIDIList prints, so
// every label must stay within 10 characters.
func midiStatusLabel(s midi.PortStatus) string {
	switch s {
	case midi.PortSendingNotes:
		return "ok"
	case midi.PortUnselected:
		return "off"
	case midi.PortDAWOnly:
		return "daw"
	case midi.PortLoopback:
		return "loopback"
	default:
		return "?"
	}
}
