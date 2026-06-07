package midi

import "testing"

func TestPickPortName(t *testing.T) {
	tests := []struct {
		name  string
		names []string
		match string
		role  Role
		want  int
	}{
		{"normal MIDI port picks MIDI", []string{"Launchkey MK4 61 MIDI In", "Launchkey MK4 61 DAW In"}, "launchkey", RoleMIDI, 0},
		{"normal DAW port picks DAW", []string{"Launchkey MK4 61 MIDI In", "Launchkey MK4 61 DAW In"}, "launchkey", RoleDAW, 1},
		{"normal MIDI uppercase needle still matches", []string{"Launchkey MK4 61 MIDI In", "Launchkey MK4 61 DAW In"}, "LAUNCHKEY", RoleMIDI, 0},
		{"duplicate names MIDI = first", []string{"Launchkey MK4 61", "Launchkey MK4 61"}, "launchkey", RoleMIDI, 0},
		{"duplicate names DAW = second", []string{"Launchkey MK4 61", "Launchkey MK4 61"}, "launchkey", RoleDAW, 1},
		{"only one port DAW = not found", []string{"Launchkey MK4 61 MIDI In"}, "launchkey", RoleDAW, -1},
		{"only one port MIDI works", []string{"Launchkey MK4 61 MIDI In"}, "launchkey", RoleMIDI, 0},
		{"no matching port returns -1", []string{"Some Other Synth", "Yamaha P-125"}, "launchkey", RoleMIDI, -1},
		{"no matching port DAW returns -1", []string{"Some Other Synth"}, "launchkey", RoleDAW, -1},
		{"empty list returns -1", []string{}, "launchkey", RoleMIDI, -1},
		{"reversed order — DAW first in list still picks correct by name", []string{"Launchkey MK4 61 DAW In", "Launchkey MK4 61 MIDI In"}, "launchkey", RoleMIDI, 1},
		{"reversed order — DAW first in list still picks DAW by name", []string{"Launchkey MK4 61 DAW In", "Launchkey MK4 61 MIDI In"}, "launchkey", RoleDAW, 0},
		{"MIDI 2 suffix counts as DAW", []string{"Launchkey MK4 61 MIDI In", "Launchkey MK4 61 MIDI 2"}, "launchkey", RoleDAW, 1},
		{"empty match = first port for MIDI", []string{"Some Synth", "Another Synth"}, "", RoleMIDI, 0},
		{"surrounded by non-matching ports — picks the right one", []string{"Yamaha P-125", "Launchkey MK4 61 MIDI In", "Launchkey MK4 61 DAW In", "FluidSynth"}, "launchkey", RoleDAW, 2},
		{"surrounded by non-matching ports — MIDI picks the right one", []string{"Yamaha P-125", "Launchkey MK4 61 MIDI In", "Launchkey MK4 61 DAW In", "FluidSynth"}, "launchkey", RoleMIDI, 1},
		{"duplicate-name fallback skips non-matching ports", []string{"Yamaha P-125", "Launchkey MK4 61", "FluidSynth", "Launchkey MK4 61"}, "launchkey", RoleDAW, 3},
		{"duplicate-name fallback MIDI = first matching index, not list index 0", []string{"Yamaha P-125", "Launchkey MK4 61", "Launchkey MK4 61"}, "launchkey", RoleMIDI, 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := PickPortName(tt.names, tt.match, tt.role)
			if got != tt.want {
				t.Errorf("PickPortName(%v, %q, %v) = %d, want %d", tt.names, tt.match, tt.role, got, tt.want)
			}
		})
	}
}
