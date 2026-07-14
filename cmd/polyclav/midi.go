package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

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
// live classification (sends notes / DAW-only / ignored / restricted),
// so there is zero guessing about what names/substrings to put in
// [midi].ignore_devices, --midi-ignore, or [midi].port_match — the
// single most common friction point before this existed (previously
// `aconnect -l`, ALSA-specific and not obviously the right tool).
func runMIDIList(args []string) int {
	fs := flag.NewFlagSet("midi list", flag.ExitOnError)
	configPath := fs.String("config", "", "path to polyclav.toml (default: XDG config dir)")
	_ = fs.Parse(args)

	names, err := midi.PortNames()
	if err != nil {
		fmt.Fprintf(os.Stderr, "polyclav midi list: %v\n", err)
		return 1
	}

	// Best-effort: classify against the real config if one exists, so the
	// report reflects what the running daemon would actually do. A
	// missing/unparsable config just falls back to the "everything sends
	// notes" default (match="", ignore=nil) rather than erroring — this
	// is a read-only report, not a startup gate.
	path := *configPath
	if path == "" {
		if cfgDir, cerr := os.UserConfigDir(); cerr == nil {
			path = filepath.Join(cfgDir, "polyclav", "polyclav.toml")
		}
	}
	var match string
	var ignore []string
	if path != "" {
		if cfg, cerr := config.Load(path); cerr == nil {
			match = cfg.MIDI.PortMatch
			ignore = cfg.MIDI.IgnoreDevices
		}
	}

	infos := midi.ClassifyPorts(names, match, ignore)
	if len(infos) == 0 {
		fmt.Println("No MIDI input ports found.")
		return 0
	}
	fmt.Println("MIDI input ports:")
	for _, info := range infos {
		fmt.Printf("  %-10s %s\n", midiStatusLabel(info.Status), info.Name)
	}
	fmt.Println()
	fmt.Println("Use a stable substring of the names above in polyclav.toml's")
	fmt.Println(`[midi].ignore_devices, or --midi-ignore "name one,name two" for a`)
	fmt.Println("one-off override. Matching ignores the trailing ALSA address, so it")
	fmt.Println("survives a replug/reboot even if that address changes.")
	return 0
}

func midiStatusLabel(s midi.PortStatus) string {
	switch s {
	case midi.PortSendingNotes:
		return "ok"
	case midi.PortDAWOnly:
		return "daw"
	case midi.PortIgnored:
		return "ignored"
	case midi.PortRestricted:
		return "restricted"
	default:
		return "?"
	}
}
