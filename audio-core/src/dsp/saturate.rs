//! Shared diode-pair soft-saturation nonlinearity — the core math behind
//! [`crate::dsp::DrivePedal`] and reused as the "analog warmth" character
//! inside the delay pedal's feedback loop ([`crate::dsp::AnalogDelay`]).
//!
//! Models an antiparallel diode pair (the classic Tube-Screamer-style
//! clipping topology) as a memoryless nonlinear one-port. The exact circuit
//! equation `(driven_input - v) / R = 2 * Is * sinh(v / Vt)` for the
//! clipped output voltage `v` is solved via the closed-form
//! deep-conduction approximation `v = Vt * asinh(driven / (2*Is*R))`
//! (dropping the resistive feedback term `-v/R`, negligible once the
//! diodes conduct) rather than an iterative Newton-Raphson solve (tried
//! first in `DrivePedal`'s history; diverges badly from a poor initial
//! guess on this steep exponential) or the academic reference's
//! closed-form Lambert-W solve. See `drive_pedal.rs`'s module docs for
//! the full derivation history.
//!
//! `R`/`Vt`/`Is` are representative silicon small-signal-diode values
//! (1N4148-class), not SPICE-matched to a specific real pedal.
//!
//! This function returns the raw clipped voltage `v` — NOT scaled by any
//! output-makeup gain. Callers calibrate their own makeup gain (and their
//! own `pre_gain`, i.e. how hard the stage is driven) against
//! `dsp::loudness::measure_lufs`, since a dedicated drive pedal and a
//! feedback-loop warmth stage want very different intensities from the
//! same nonlinearity.

const THERMAL_VOLTAGE: f32 = 0.02585;
const SATURATION_CURRENT: f32 = 2.52e-9;
const INPUT_RESISTANCE: f32 = 4_700.0;
/// Sanity bound on the pre-gained signal before it enters `asinh`. Real
/// audio never approaches this at any sane `pre_gain` a caller would
/// choose — this only ever clamps a pathological upstream value, and
/// guarantees `driven / (2*Is*R)` stays comfortably inside f32 range so
/// this function can never emit non-finite output.
const DRIVEN_CLAMP: f32 = 1.0e6;

/// Diode-pair soft clipper. `pre_gain` sets how hard the stage is
/// driven — 1.0 is unity (barely into conduction), larger values push
/// deeper into saturation. Returns the raw clipped voltage; the caller
/// applies its own output-makeup gain.
///
/// Non-finite `x` or `pre_gain` returns `0.0` rather than propagating —
/// `f32::clamp` only sanitizes out-of-range values, not NaN (unlike
/// `f32::max`/`min`, it compares with `<`/`>`, both false for NaN, so
/// NaN passes straight through), and `x * pre_gain` is itself NaN
/// whenever one operand is infinite and the other is exactly zero.
#[inline]
pub fn diode_clip(x: f32, pre_gain: f32) -> f32 {
    if !x.is_finite() || !pre_gain.is_finite() {
        return 0.0;
    }
    let driven = (x * pre_gain).clamp(-DRIVEN_CLAMP, DRIVEN_CLAMP);
    THERMAL_VOLTAGE * (driven / (2.0 * SATURATION_CURRENT * INPUT_RESISTANCE)).asinh()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn zero_input_is_zero_output() {
        assert_eq!(diode_clip(0.0, 400.0), 0.0);
    }

    #[test]
    fn odd_symmetric() {
        // A symmetric diode pair must clip + and - equally: f(-x) == -f(x).
        for x in [0.01, 0.1, 0.3, 1.0, 5.0] {
            let pos = diode_clip(x, 40.0);
            let neg = diode_clip(-x, 40.0);
            assert!(
                (pos + neg).abs() < 1e-6,
                "not odd-symmetric at x={x}: f(x)={pos}, f(-x)={neg}"
            );
        }
    }

    #[test]
    fn monotonic_in_input() {
        let mut prev = diode_clip(-1.0, 40.0);
        let mut x = -0.99;
        while x <= 1.0 {
            let v = diode_clip(x, 40.0);
            assert!(v >= prev, "not monotonic at x={x}: v={v} < prev={prev}");
            prev = v;
            x += 0.01;
        }
    }

    #[test]
    fn finite_for_extreme_pre_gain_and_input() {
        for pre_gain in [0.0, 1.0, 1.0e6, f32::MAX] {
            for x in [
                0.0,
                1.0,
                -1.0,
                f32::MAX,
                f32::MIN,
                f32::INFINITY,
                f32::NEG_INFINITY,
            ] {
                let v = diode_clip(x, pre_gain);
                assert!(
                    v.is_finite(),
                    "non-finite for x={x}, pre_gain={pre_gain}: got {v}"
                );
            }
        }
    }

    #[test]
    fn higher_pre_gain_saturates_harder() {
        // Same input, more pre_gain -> at least as much clipped output
        // (deeper into the compressive region of asinh).
        let low = diode_clip(0.3, 5.0).abs();
        let high = diode_clip(0.3, 400.0).abs();
        assert!(
            high >= low,
            "expected more pre_gain to saturate at least as hard: low={low}, high={high}"
        );
    }
}
