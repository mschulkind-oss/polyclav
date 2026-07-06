// editor.go holds the phase-C editing endpoints (docs/WEB_UI.md phase C,
// docs/VELOCITY_CURVES.md "Live tweaking"): validated TOML write-back on
// PUT /api/config, and the live velocity-curve editor behind
// GET/PUT /api/velocity — including its explicit-save path that writes a
// clearly-marked managed [midi.velocity] block into polyclav.toml.
package web

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/mschulkind-oss/polyclav/internal/config"
	"github.com/mschulkind-oss/polyclav/internal/velocity"
)

// ---- PUT /api/config ----------------------------------------------------

// configValidationError marks a config.Load / config.Validate rejection
// of a candidate config (a 422 for the client), as opposed to an I/O
// failure (a 500). The message is the human-readable multi-line startup
// error the daemon itself would print.
type configValidationError struct{ msg string }

func (e *configValidationError) Error() string { return e.msg }

// handleConfigPut is PUT /api/config: the body is the FULL polyclav.toml
// text (text/plain). It is validated (config.Load + config.Validate)
// against a temp file in the config's directory and atomically renamed
// over the real file only when valid, so the file on disk can never be
// one the daemon would refuse to boot from. Hot reload is explicitly out
// of scope (docs/WEB_UI.md): the response says a restart is required and
// the dashboard shows the banner.
func (s *Server) handleConfigPut(w http.ResponseWriter, r *http.Request) {
	if s.deps.ConfigPath == "" {
		writeErr(w, http.StatusNotFound, "config file not available")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "read body: "+err.Error())
		return
	}
	if err := s.saveValidatedConfig(body, true); err != nil {
		var ve *configValidationError
		if errors.As(err, &ve) {
			writeErr(w, http.StatusUnprocessableEntity, ve.msg)
			return
		}
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"saved": true, "restart_required": true})
}

// saveValidatedConfig writes data to a temp file next to ConfigPath,
// validates it, and atomically renames it into place (0644). The temp
// file is removed on every failure path — no litter either way.
// runValidate additionally runs the config.Validate dependency check
// (the PUT /api/config path); the velocity save path skips it because a
// curve edit never touches [[patches]] and must not be blocked by a
// soundfont that was already missing at boot.
func (s *Server) saveValidatedConfig(data []byte, runValidate bool) error {
	path := s.deps.ConfigPath
	tmp, err := os.CreateTemp(filepath.Dir(path), ".polyclav-*.toml")
	if err != nil {
		return fmt.Errorf("create temp config: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath) // no-op after a successful rename
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp config: %w", err)
	}
	cfg, err := config.Load(tmpPath)
	if err != nil {
		// Load's message quotes the file it parsed; swap the temp name for
		// the real path so the error reads like the startup error would.
		return &configValidationError{msg: strings.ReplaceAll(err.Error(), tmpPath, path)}
	}
	if runValidate {
		if err := config.Validate(cfg); err != nil {
			return &configValidationError{msg: err.Error()}
		}
	}
	if err := os.Chmod(tmpPath, 0o644); err != nil {
		return fmt.Errorf("chmod temp config: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("replace config: %w", err)
	}
	return nil
}

// ---- GET/PUT /api/velocity ------------------------------------------------

// velocityPutBody is the PUT /api/velocity request. Exactly one curve
// shape may be present — points, or curve/gamma — mirroring the config
// file's same-scope mutual-exclusion rule (both is a 400, never a silent
// pick). save=true additionally persists the curve to polyclav.toml's
// managed [midi.velocity] block.
type velocityPutBody struct {
	Curve  *string  `json:"curve"`
	Gamma  *float64 `json:"gamma"`
	OutMin *int     `json:"out_min"`
	OutMax *int     `json:"out_max"`
	Points [][]int  `json:"points"`
	Save   bool     `json:"save"`
}

// velocitySpec is the normalized form of a velocityPutBody: the shape
// both the curve constructor and the TOML block renderer consume, so the
// live curve and the saved block can never disagree.
type velocitySpec struct {
	curve  string  // preset name or "custom"; "" when points are set
	gamma  float64 // meaningful only when curve == "custom"
	outMin int
	outMax int
	points [][]int // nil for gamma curves
}

// velocitySpecFromBody validates the request's field combinations and
// ranges; msg is non-empty (a 400) on rejection. Ranges the velocity
// package checks itself (point monotonicity etc.) are left to build —
// only what would silently corrupt on the int→uint8 conversion is
// checked here.
func velocitySpecFromBody(b *velocityPutBody) (velocitySpec, string) {
	var sp velocitySpec
	if len(b.Points) > 0 && (b.Curve != nil || b.Gamma != nil) {
		return sp, "points and curve/gamma are mutually exclusive — set one or the other"
	}
	if len(b.Points) == 0 && b.Curve == nil && b.Gamma == nil {
		return sp, "nothing to apply: provide curve/gamma or points"
	}
	if b.OutMin != nil {
		sp.outMin = *b.OutMin
	}
	if b.OutMax != nil {
		sp.outMax = *b.OutMax
	}
	if sp.outMin < 0 || sp.outMin > 127 {
		return sp, fmt.Sprintf("out_min must be in 0..127 (got %d)", sp.outMin)
	}
	if sp.outMax < 0 || sp.outMax > 127 {
		return sp, fmt.Sprintf("out_max must be in 0..127 (got %d)", sp.outMax)
	}
	if len(b.Points) > 0 {
		sp.points = b.Points
		return sp, ""
	}
	if b.Gamma != nil && (!finite(*b.Gamma) || *b.Gamma <= 0) {
		return sp, fmt.Sprintf("gamma must be > 0 (got %v)", *b.Gamma)
	}
	if b.Curve != nil {
		sp.curve = *b.Curve
	}
	if b.Gamma != nil {
		sp.gamma = *b.Gamma
	}
	// Gamma with no curve name is the "custom" shorthand — the same rule
	// config.Load applies to [midi.velocity].
	if sp.curve == "" && sp.gamma > 0 {
		sp.curve = "custom"
	}
	if sp.curve == "" {
		sp.curve = "linear"
	}
	return sp, ""
}

// build constructs the velocity.Curve for sp; errors map to 400.
func (sp velocitySpec) build() (velocity.Curve, error) {
	if sp.points != nil {
		pairs := make([][2]uint8, len(sp.points))
		for i, pt := range sp.points {
			if len(pt) != 2 {
				return velocity.Curve{}, fmt.Errorf("points[%d]: want an [x, y] pair, got %d values", i, len(pt))
			}
			if pt[0] < 0 || pt[0] > 127 || pt[1] < 0 || pt[1] > 127 {
				return velocity.Curve{}, fmt.Errorf("points[%d]: [%d, %d] out of range 0..127", i, pt[0], pt[1])
			}
			pairs[i] = [2]uint8{uint8(pt[0]), uint8(pt[1])}
		}
		return velocity.NewFromPoints(pairs, uint8(sp.outMin), uint8(sp.outMax))
	}
	return velocity.New(sp.curve, float32(sp.gamma), uint8(sp.outMin), uint8(sp.outMax))
}

// handleVelocityGet reports the ACTIVE curve (whatever ApplyVelocity is
// using right now) and where it came from: "session" while an unsaved
// PUT-applied curve is installed, "config" otherwise. Source is inferred
// by label comparison because patch changes re-resolve the curve from
// config in the daemon, outside this package: once the installed label no
// longer matches the last session PUT, the session edit has been
// superseded by a config-resolved curve.
func (s *Server) handleVelocityGet(w http.ResponseWriter, _ *http.Request) {
	label := s.deps.Controls.VelocityLabel()
	source := "config"
	s.velMu.Lock()
	if label != "" && label == s.sessionVelLabel && !s.sessionVelSaved {
		source = "session"
	}
	s.velMu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"curve": label, "source": source})
}

// handleVelocityPut builds the requested curve, optionally persists it
// to polyclav.toml (save=true — the explicit-save contract from
// docs/VELOCITY_CURVES.md: no silent config mutation), and installs it
// live at the MIDI funnel via the controls layer. Save-then-apply order:
// a request that fails to save must not leave a half-applied state.
func (s *Server) handleVelocityPut(w http.ResponseWriter, r *http.Request) {
	var body velocityPutBody
	if err := decodeJSON(w, r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "bad JSON: "+err.Error())
		return
	}
	sp, msg := velocitySpecFromBody(&body)
	if msg != "" {
		writeErr(w, http.StatusBadRequest, msg)
		return
	}
	curve, err := sp.build()
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}

	if body.Save {
		if s.deps.ConfigPath == "" {
			writeErr(w, http.StatusNotFound, "config file not available; cannot save")
			return
		}
		if err := s.saveVelocityBlock(sp); err != nil {
			var ve *configValidationError
			switch {
			case errors.Is(err, errUnmanagedVelocity) || errors.Is(err, errCorruptMarkers):
				writeErr(w, http.StatusConflict, err.Error())
			case errors.As(err, &ve):
				writeErr(w, http.StatusConflict, "saving would produce an invalid config — edit polyclav.toml by hand: "+ve.msg)
			default:
				writeErr(w, http.StatusInternalServerError, err.Error())
			}
			return
		}
	}

	label := curve.Describe()
	s.deps.Controls.SetVelocityRemap(curve.Apply, label)
	s.velMu.Lock()
	s.sessionVelLabel, s.sessionVelSaved = label, body.Save
	s.velMu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"curve": label, "saved": body.Save})
}

// ---- managed [midi.velocity] block ----------------------------------------

// Marker lines fencing the web-UI-owned [midi.velocity] block in
// polyclav.toml. Everything between them is regenerated wholesale on
// every save — the BEGIN line says so, so a hand edit inside the fence
// is a documented loss, while a hand-written section OUTSIDE the fence
// is never touched (errUnmanagedVelocity).
const (
	velocityBeginMarker = "# BEGIN polyclav-managed [midi.velocity] (web UI — edits between markers are overwritten)"
	velocityEndMarker   = "# END polyclav-managed [midi.velocity]"
)

var (
	errUnmanagedVelocity = errors.New("polyclav.toml already has a hand-written [midi.velocity] section — edit the config file by hand instead of saving from the web UI")
	errCorruptMarkers    = errors.New("the managed [midi.velocity] markers in polyclav.toml are corrupted (one of BEGIN/END is missing) — repair the config file by hand")
)

// unmanagedVelocityRe matches a [midi.velocity] table header line with
// TOML's optional interior whitespace. Eccentric spellings that dodge it
// (e.g. an inline velocity table under [midi]) still fail the merged
// file's config.Load, which maps to 409 — never a silent rewrite.
var unmanagedVelocityRe = regexp.MustCompile(`(?m)^\s*\[\s*midi\s*\.\s*velocity\s*\]`)

// renderVelocityBlock renders sp as the marker-fenced [midi.velocity]
// block, without a trailing newline. Only meaningful keys are written:
// gamma only for "custom", out_min/out_max only when explicitly clamped
// (0 means "the velocity package's 1/127 defaults").
func renderVelocityBlock(sp velocitySpec) string {
	var b strings.Builder
	b.WriteString(velocityBeginMarker + "\n")
	b.WriteString("[midi.velocity]\n")
	if sp.points != nil {
		pts := make([]string, len(sp.points))
		for i, p := range sp.points {
			pts[i] = fmt.Sprintf("[%d, %d]", p[0], p[1])
		}
		fmt.Fprintf(&b, "points = [%s]\n", strings.Join(pts, ", "))
	} else {
		fmt.Fprintf(&b, "curve = %q\n", sp.curve)
		if sp.curve == "custom" {
			fmt.Fprintf(&b, "gamma = %g\n", sp.gamma)
		}
	}
	if sp.outMin > 0 {
		fmt.Fprintf(&b, "out_min = %d\n", sp.outMin)
	}
	if sp.outMax > 0 {
		fmt.Fprintf(&b, "out_max = %d\n", sp.outMax)
	}
	b.WriteString(velocityEndMarker)
	return b.String()
}

// upsertManagedVelocity replaces the existing managed block in orig with
// block, or appends block when no markers exist yet. A [midi.velocity]
// header outside the fenced region refuses with errUnmanagedVelocity —
// hand-written config is never silently rewritten.
func upsertManagedVelocity(orig, block string) (string, error) {
	bi := strings.Index(orig, velocityBeginMarker)
	ei := strings.Index(orig, velocityEndMarker)
	switch {
	case bi >= 0 && ei > bi:
		outside := orig[:bi] + orig[ei+len(velocityEndMarker):]
		if unmanagedVelocityRe.MatchString(outside) {
			return "", errUnmanagedVelocity
		}
		return orig[:bi] + block + orig[ei+len(velocityEndMarker):], nil
	case bi < 0 && ei < 0:
		if unmanagedVelocityRe.MatchString(orig) {
			return "", errUnmanagedVelocity
		}
		trimmed := strings.TrimRight(orig, "\n")
		if trimmed == "" {
			return block + "\n", nil
		}
		return trimmed + "\n\n" + block + "\n", nil
	default:
		return "", errCorruptMarkers
	}
}

// saveVelocityBlock persists sp into ConfigPath's managed block, going
// through the same temp-validate-rename path as PUT /api/config (Load
// only — see saveValidatedConfig on why Validate is skipped here).
func (s *Server) saveVelocityBlock(sp velocitySpec) error {
	orig, err := os.ReadFile(s.deps.ConfigPath)
	if err != nil {
		return fmt.Errorf("read config: %w", err)
	}
	merged, err := upsertManagedVelocity(string(orig), renderVelocityBlock(sp))
	if err != nil {
		return err
	}
	return s.saveValidatedConfig([]byte(merged), false)
}
