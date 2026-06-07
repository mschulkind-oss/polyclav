package main

import "testing"

func TestMatchPortIndex(t *testing.T) {
	tests := []struct {
		name    string
		names   []string
		match   string
		wantDAW bool
		want    int
	}{
		{
			name:    "non-DAW match picks first non-DAW port",
			names:   []string{"Launchkey MK4 LKMK4 MIDI", "Launchkey MK4 LKMK4 DAW"},
			match:   "Launchkey",
			wantDAW: false,
			want:    0,
		},
		{
			name:    "DAW match picks DAW port",
			names:   []string{"Launchkey MK4 LKMK4 MIDI", "Launchkey MK4 LKMK4 DAW"},
			match:   "Launchkey",
			wantDAW: true,
			want:    1,
		},
		{
			name:    "case insensitive",
			names:   []string{"LAUNCHKEY mk4 daw"},
			match:   "launchkey",
			wantDAW: true,
			want:    0,
		},
		{
			name:    "no match returns -1",
			names:   []string{"Some Other Synth"},
			match:   "Launchkey",
			wantDAW: false,
			want:    -1,
		},
		{
			name:    "non-DAW requested but only DAW available",
			names:   []string{"Launchkey MK4 DAW"},
			match:   "Launchkey",
			wantDAW: false,
			want:    -1,
		},
		{
			name:    "empty names returns -1",
			names:   []string{},
			match:   "Launchkey",
			wantDAW: false,
			want:    -1,
		},
		{
			name:    "skips non-matching before finding the right one",
			names:   []string{"Other", "Launchkey DAW", "Launchkey MIDI"},
			match:   "Launchkey",
			wantDAW: false,
			want:    2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := matchPortIndex(tt.names, tt.match, tt.wantDAW)
			if got != tt.want {
				t.Errorf("matchPortIndex(%v, %q, %v) = %d; want %d", tt.names, tt.match, tt.wantDAW, got, tt.want)
			}
		})
	}
}
