package components

// Color is a Launchkey palette index (0..127). The MK4 firmware exposes the
// full 128-entry table over the wire; Components only names a curated 56,
// extracted from the Novation Components web app. Unknown indices render
// black on host previews; the device itself accepts all 128.
type Color uint8

const (
	ColorOff           Color = 0
	ColorDimWhite      Color = 1
	ColorNeutralWhite  Color = 2
	ColorBrightWhite   Color = 3
	ColorVibrantRed    Color = 5
	ColorVibrantOrange Color = 9
	ColorVibrantYellow Color = 13
	ColorVibrantGreen  Color = 25
	ColorVibrantCyan   Color = 33
	ColorVibrantBlue   Color = 41
	ColorVibrantPurple Color = 49
	ColorVibrantPink   Color = 57
)

// palette[i] is the 8-bit sRGB for Components color index i; (0,0,0) marks
// indices Components does not name. Use PaletteRGB to look up.
var palette = [128][3]uint8{
	0:   {79, 79, 79},
	1:   {140, 140, 140},
	2:   {185, 185, 185},
	3:   {243, 243, 243},
	4:   {255, 145, 140},
	5:   {255, 1, 0},
	6:   {221, 98, 97},
	7:   {179, 97, 97},
	9:   {255, 136, 27},
	10:  {210, 109, 17},
	11:  {175, 90, 13},
	13:  {250, 255, 6},
	14:  {211, 202, 27},
	15:  {172, 164, 24},
	16:  {197, 255, 135},
	17:  {172, 254, 41},
	18:  {118, 197, 34},
	19:  {90, 156, 7},
	24:  {147, 255, 177},
	25:  {75, 253, 88},
	26:  {51, 192, 78},
	27:  {55, 157, 71},
	28:  {149, 255, 215},
	29:  {0, 255, 194},
	30:  {46, 200, 156},
	31:  {37, 156, 121},
	32:  {117, 252, 251},
	33:  {0, 255, 255},
	34:  {41, 187, 187},
	35:  {0, 144, 144},
	36:  {125, 230, 255},
	37:  {24, 215, 255},
	38:  {10, 158, 189},
	39:  {7, 124, 149},
	40:  {108, 183, 255},
	41:  {0, 152, 254},
	42:  {25, 125, 201},
	43:  {0, 105, 175},
	45:  {31, 86, 218},
	46:  {38, 83, 193},
	47:  {38, 74, 160},
	48:  {160, 127, 255},
	49:  {150, 53, 255},
	50:  {129, 60, 195},
	51:  {111, 55, 165},
	52:  {249, 128, 255},
	53:  {242, 38, 254},
	54:  {191, 68, 197},
	55:  {115, 57, 160},
	56:  {255, 116, 174},
	57:  {255, 57, 169},
	58:  {220, 62, 151},
	59:  {191, 61, 133},
	79:  {115, 129, 255},
	108: {255, 172, 82},
	109: {250, 255, 162},
}

// PaletteRGB returns the 8-bit sRGB host-preview colour for a Components
// palette index. Unknown / unnamed indices return black; the device will
// still happily accept them (its own firmware table is broader).
func PaletteRGB(c Color) (r, g, b uint8) {
	if int(c) >= len(palette) {
		return 0, 0, 0
	}
	p := palette[c]
	return p[0], p[1], p[2]
}
