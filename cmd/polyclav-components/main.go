package main

import (
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/mschulkind-oss/polyclav/internal/launchkey/components"
	"gitlab.com/gomidi/midi/v2/drivers"
	"gitlab.com/gomidi/midi/v2/drivers/rtmididrv"
)

func main() {
	if len(os.Args) < 2 {
		printUsage(os.Stderr)
		os.Exit(2)
	}
	cmd := os.Args[1]
	args := os.Args[2:]
	var err error
	switch cmd {
	case "encode":
		err = runEncode(args)
	case "decode":
		err = runDecode(args)
	case "upload":
		err = runUpload(args)
	case "help", "-h", "--help":
		printUsage(os.Stdout)
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n\n", cmd)
		printUsage(os.Stderr)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func printUsage(w io.Writer) {
	fmt.Fprintln(w, "polyclav-components encode <toml-path> [--out FILE] [--product VARIANT]")
	fmt.Fprintln(w, "  Encode a TOML mode definition into SysEx.")
	fmt.Fprintln(w, "polyclav-components decode <hex-bytes> [--file PATH]")
	fmt.Fprintln(w, "  Parse a SysEx payload and print a summary.")
	fmt.Fprintln(w, "polyclav-components upload --slot <n> --type pots|pads|faders [--port <name>] [--activate] <file>")
	fmt.Fprintln(w, "  Upload a SysEx .syx file to the Launchkey MK4. Use '-' to read from stdin.")
	fmt.Fprintln(w, "  --activate switches the device to the mode after upload (sends a Scene-Launch wrap on the DAW port).")
	fmt.Fprintln(w, "polyclav-components help")
	fmt.Fprintln(w, "  Show this help.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "VARIANT options: launchkey25_mk4, launchkey37_mk4, launchkey49_mk4, launchkey61_mk4 (default).")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "TOML schema example:")
	fmt.Fprintln(w, "  surface = \"pots\"")
	fmt.Fprintln(w, "  slot = 0")
	fmt.Fprintln(w, "  name = \"MyPots\"")
	fmt.Fprintln(w, "  on_color = 26")
	fmt.Fprintln(w, "  enable_transposition = false")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "  [[controls]]")
	fmt.Fprintln(w, "  kind = \"cc\"")
	fmt.Fprintln(w, "  index = 56")
	fmt.Fprintln(w, "  channel = 1")
	fmt.Fprintln(w, "  cc = 1")
	fmt.Fprintln(w, "  top = 127")
	fmt.Fprintln(w, "  bottom = 0")
	fmt.Fprintln(w, "  behaviour = \"momentary\"")
	fmt.Fprintln(w, "  on_color = 26")
}

type tomlMode struct {
	Surface             string            `toml:"surface"`
	Slot                uint8             `toml:"slot"`
	Name                string            `toml:"name"`
	OnColor             uint8             `toml:"on_color"`
	ExternalColor       uint8             `toml:"external_color"`
	EnableTransposition bool              `toml:"enable_transposition"`
	DefaultOctave       uint8             `toml:"default_octave"`
	Controls            []tomlControl     `toml:"controls"`
	ControlNames        map[string]string `toml:"control_names"`
}

type tomlControl struct {
	Kind        string `toml:"kind"`
	Index       uint8  `toml:"index"`
	Channel     int8   `toml:"channel"`
	Behaviour   string `toml:"behaviour"`
	OffColor    uint8  `toml:"off_color"`
	OnColor     uint8  `toml:"on_color"`
	CC          uint8  `toml:"cc"`
	Top         uint16 `toml:"top"`
	Bottom      uint16 `toml:"bottom"`
	Sensitivity uint8  `toml:"sensitivity"`
	Note        uint8  `toml:"note"`
	Velocity    uint8  `toml:"velocity"`
}

func runEncode(args []string) error {
	fs := flag.NewFlagSet("encode", flag.ContinueOnError)
	out := fs.String("out", "", "")
	product := fs.String("product", "launchkey61_mk4", "")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("encode: missing <toml-path>")
	}
	data, err := os.ReadFile(fs.Arg(0))
	if err != nil {
		return fmt.Errorf("read file: %w", err)
	}
	var tm tomlMode
	if err := toml.Unmarshal(data, &tm); err != nil {
		return fmt.Errorf("decode TOML: %w", err)
	}
	mode, err := buildMode(&tm)
	if err != nil {
		return err
	}
	pid, err := productFor(*product)
	if err != nil {
		return err
	}
	bytes, err := mode.Encode(pid)
	if err != nil {
		return fmt.Errorf("encode mode: %w", err)
	}
	if *out == "" {
		fmt.Println(formatHex(bytes))
	} else {
		if err := os.WriteFile(*out, bytes, 0644); err != nil {
			return fmt.Errorf("write file: %w", err)
		}
		fmt.Printf("wrote %d bytes to %s\n", len(bytes), *out)
	}
	return nil
}

func buildMode(t *tomlMode) (*components.CustomMode, error) {
	var surface components.SurfaceType
	switch t.Surface {
	case "pots":
		surface = components.SurfacePots
	case "pads":
		surface = components.SurfacePads
	case "faders":
		surface = components.SurfaceFaders
	case "pedal":
		surface = components.SurfacePedal
	case "modwheel":
		surface = components.SurfaceModWheel
	default:
		return nil, fmt.Errorf("unknown surface %q", t.Surface)
	}
	mode := components.CustomMode{
		Surface:             surface,
		Slot:                t.Slot,
		Name:                t.Name,
		OnColor:             components.Color(t.OnColor),
		ExternalColor:       components.Color(t.ExternalColor),
		EnableTransposition: t.EnableTransposition,
		DefaultOctave:       t.DefaultOctave,
		Controls:            make([]components.Control, len(t.Controls)),
	}
	for i, tc := range t.Controls {
		var kind components.ControlType
		switch tc.Kind {
		case "off":
			kind = components.ControlOff
		case "note":
			kind = components.ControlNote
		case "cc":
			kind = components.ControlCC
		case "cc14":
			kind = components.ControlCC14
		case "pc":
			kind = components.ControlProgramChange
		default:
			return nil, fmt.Errorf("control %d: unknown kind %q", i, tc.Kind)
		}
		var behaviour components.Behaviour
		switch tc.Behaviour {
		case "momentary":
			behaviour = components.BehaviourMomentary
		case "toggle":
			behaviour = components.BehaviourToggle
		case "momentary_aftertouch":
			behaviour = components.BehaviourMomentaryAftertouch
		case "toggle_aftertouch":
			behaviour = components.BehaviourToggleAftertouch
		default:
			return nil, fmt.Errorf("control %d: unknown behaviour %q", i, tc.Behaviour)
		}
		ch := tc.Channel
		if ch > 0 {
			ch = ch - 1
		}
		mode.Controls[i] = components.Control{
			Index:       tc.Index,
			Type:        kind,
			OffColor:    components.Color(tc.OffColor),
			OnColor:     components.Color(tc.OnColor),
			Channel:     ch,
			Behaviour:   behaviour,
			CCNumber:    tc.CC,
			TopValue:    tc.Top,
			BottomValue: tc.Bottom,
			Sensitivity: tc.Sensitivity,
			Note:        tc.Note,
			Velocity:    tc.Velocity,
		}
	}
	if len(t.ControlNames) > 0 {
		mode.ControlNames = make(map[uint8]string, len(t.ControlNames))
		for k, v := range t.ControlNames {
			idx, err := strconv.ParseUint(k, 10, 8)
			if err != nil {
				return nil, fmt.Errorf("parse control name key %q: %w", k, err)
			}
			mode.ControlNames[uint8(idx)] = v
		}
	}
	return &mode, nil
}

func productFor(name string) (components.ProductID, error) {
	switch name {
	case "launchkey25_mk4", "launchkey37_mk4", "launchkey49_mk4", "launchkey61_mk4":
		return components.SysExProduct, nil
	default:
		return components.ProductID{}, fmt.Errorf("unknown product %q (try launchkey61_mk4)", name)
	}
}

func formatHex(b []byte) string {
	var sb strings.Builder
	for i, byte := range b {
		if i > 0 {
			sb.WriteByte(' ')
		}
		sb.WriteString(fmt.Sprintf("%02x", byte))
	}
	return sb.String()
}

func runDecode(args []string) error {
	fs := flag.NewFlagSet("decode", flag.ContinueOnError)
	file := fs.String("file", "", "")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	var data []byte
	var err error
	if *file != "" {
		raw, err := os.ReadFile(*file)
		if err != nil {
			return fmt.Errorf("read file: %w", err)
		}
		data, err = parseInput(string(raw))
		if err != nil {
			data = raw
		}
	} else {
		if fs.NArg() < 1 {
			return errors.New("decode: missing <hex-bytes>")
		}
		input := strings.Join(fs.Args(), " ")
		data, err = parseInput(input)
		if err != nil {
			return err
		}
	}
	if len(data) < 11 {
		return errors.New("data too short (minimum 11 bytes)")
	}
	if data[0] != 0xF0 {
		return fmt.Errorf("invalid start byte: expected 0xF0, got 0x%02X", data[0])
	}
	if data[1] != 0x00 || data[2] != 0x20 || data[3] != 0x29 {
		return fmt.Errorf("invalid manufacturer ID: expected 00 20 29, got %02X %02X %02X", data[1], data[2], data[3])
	}
	if data[len(data)-1] != 0xF7 {
		return fmt.Errorf("invalid end byte: expected 0xF7, got 0x%02X", data[len(data)-1])
	}
	cmd := data[6]
	cmdName := "unknown"
	if cmd == 0x05 {
		cmdName = "write custom mode"
	}
	payloadLen := len(data) - 12
	if payloadLen < 0 {
		payloadLen = 0
	}
	fmt.Printf("SysEx (%d bytes)\n", len(data))
	fmt.Printf("├─ Manufacturer: Novation (00 20 29)\n")
	fmt.Printf("├─ Product ID:   pid[0]=0x%02X pid[1]=0x%02X\n", data[4], data[5])
	fmt.Printf("├─ Command:      0x%02X (%s)\n", cmd, cmdName)
	fmt.Printf("├─ Sub-command:  0x%02X\n", data[7])
	fmt.Printf("├─ Master ch:    0x%02X\n", data[8])
	fmt.Printf("├─ Surface:      0x%02X (%s)\n", data[9], surfaceName(data[9]))
	fmt.Printf("└─ Mode index:   0x%02X\n", data[10])
	fmt.Printf("Payload: %d TLV bytes (decode not implemented)\n", payloadLen)
	return nil
}

func parseInput(s string) ([]byte, error) {
	cleaned := stripWhitespace(s)
	if cleaned == "" {
		return nil, errors.New("empty input")
	}
	if len(cleaned)%2 != 0 {
		return nil, errors.New("hex string must have even length")
	}
	return hex.DecodeString(cleaned)
}

func stripWhitespace(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == ',' {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func surfaceName(b byte) string {
	switch b {
	case 0:
		return "pots"
	case 1:
		return "pads"
	case 2:
		return "pedal"
	case 3:
		return "faders"
	case 4:
		return "modwheel"
	default:
		return "unknown"
	}
}

func runUpload(args []string) error {
	fs := flag.NewFlagSet("upload", flag.ContinueOnError)
	slot := fs.Int("slot", -1, "Custom-mode slot (0..7)")
	typeFlag := fs.String("type", "", "Surface type: pots, pads, or faders")
	port := fs.String("port", "Launchkey", "MIDI port name substring to match")
	activate := fs.Bool("activate", false, "After upload, send Scene-Launch wrap on DAW port to switch the device to this mode")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("upload: missing <file> (use '-' for stdin)")
	}
	if *slot < 0 || *slot > 7 {
		return fmt.Errorf("upload: --slot must be in range [0,7], got %d", *slot)
	}
	var cc byte
	switch *typeFlag {
	case "pads":
		cc = 29
	case "pots":
		cc = 30
	case "faders":
		cc = 31
	default:
		return fmt.Errorf("upload: --type must be one of pots, pads, faders (got %q)", *typeFlag)
	}
	var data []byte
	var err error
	if fs.Arg(0) == "-" {
		data, err = io.ReadAll(os.Stdin)
	} else {
		data, err = os.ReadFile(fs.Arg(0))
	}
	if err != nil {
		return fmt.Errorf("read file: %w", err)
	}
	if len(data) < 11 {
		return errors.New("upload: data too short to be a SysEx payload (need >= 11 bytes)")
	}
	if data[0] != 0xF0 {
		return fmt.Errorf("upload: not a SysEx payload (first byte 0x%02X, expected 0xF0)", data[0])
	}
	if data[len(data)-1] != 0xF7 {
		return fmt.Errorf("upload: not a SysEx payload (last byte 0x%02X, expected 0xF7)", data[len(data)-1])
	}
	if len(data) > 1024 {
		return fmt.Errorf("upload: SysEx payload suspiciously large (%d bytes; expected < 1024)", len(data))
	}
	drv, err := rtmididrv.New()
	if err != nil {
		return fmt.Errorf("rtmidi driver: %w", err)
	}
	defer drv.Close()
	outs, err := drv.Outs()
	if err != nil {
		return fmt.Errorf("enumerate midi outs: %w", err)
	}
	mainOut := pickMainOut(outs, *port)
	if mainOut == nil {
		names := make([]string, len(outs))
		for i, o := range outs {
			names[i] = o.String()
		}
		return fmt.Errorf("upload: no MIDI (non-DAW) output port matching %q (available: %s)", *port, strings.Join(names, ", "))
	}
	if err := mainOut.Open(); err != nil {
		return fmt.Errorf("open %q: %w", mainOut.String(), err)
	}
	defer mainOut.Close()
	if err := mainOut.Send(data); err != nil {
		return fmt.Errorf("send SysEx to %q: %w", mainOut.String(), err)
	}
	fmt.Printf("uploaded %d bytes to %s (slot %d, %s)\n", len(data), mainOut.String(), *slot, *typeFlag)
	if *activate {
		dawOut := pickDAWOut(outs, *port)
		if dawOut == nil {
			names := make([]string, len(outs))
			for i, o := range outs {
				names[i] = o.String()
			}
			return fmt.Errorf("upload: --activate requires a DAW output port matching %q (available: %s)", *port, strings.Join(names, ", "))
		}
		if err := dawOut.Open(); err != nil {
			return fmt.Errorf("open DAW %q: %w", dawOut.String(), err)
		}
		defer dawOut.Close()
		time.Sleep(50 * time.Millisecond)
		if err := dawOut.Send([]byte{0x9F, 0x0B, 0x7F}); err != nil {
			return fmt.Errorf("activation step scene-launch on: %w", err)
		}
		if err := dawOut.Send([]byte{0xB6, cc, byte(*slot)}); err != nil {
			return fmt.Errorf("activation step select-mode cc: %w", err)
		}
		if err := dawOut.Send([]byte{0x9F, 0x0B, 0x00}); err != nil {
			return fmt.Errorf("activation step scene-launch off: %w", err)
		}
		fmt.Printf("activated slot %d (%s) via %s\n", *slot, *typeFlag, dawOut.String())
	}
	return nil
}

// matchPortIndex returns the index of the first name in `names` whose
// lowercased value contains lowercased `match`, with the DAW substring
// requirement set by `wantDAW` (true => name must also contain "daw";
// false => name must NOT contain "daw"). Returns -1 if no name matches.
func matchPortIndex(names []string, match string, wantDAW bool) int {
	needle := strings.ToLower(match)
	for i, n := range names {
		ln := strings.ToLower(n)
		if !strings.Contains(ln, needle) {
			continue
		}
		hasDAW := strings.Contains(ln, "daw")
		if hasDAW == wantDAW {
			return i
		}
	}
	return -1
}

func pickMainOut(outs []drivers.Out, match string) drivers.Out {
	names := make([]string, len(outs))
	for i, o := range outs {
		names[i] = o.String()
	}
	if idx := matchPortIndex(names, match, false); idx >= 0 {
		return outs[idx]
	}
	return nil
}

func pickDAWOut(outs []drivers.Out, match string) drivers.Out {
	names := make([]string, len(outs))
	for i, o := range outs {
		names[i] = o.String()
	}
	if idx := matchPortIndex(names, match, true); idx >= 0 {
		return outs[idx]
	}
	return nil
}
