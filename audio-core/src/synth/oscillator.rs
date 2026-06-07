//! Per-voice oscillator.
//!
//! Phase 1 ships a single PolyBLEP saw — `fundsp::PolySaw` driven
//! sample-by-sample with a frequency input. This file is a thin facade
//! over that primitive so Phase 2 (multi-osc + waveform selection) can
//! swap in a `Waveform` enum without changing the call sites.

use fundsp::audionode::AudioNode;
use fundsp::oscillator::PolySaw;

/// Single anti-aliased oscillator. The output is in -1..+1.
pub struct Oscillator {
    saw: PolySaw<f32>,
}

impl Oscillator {
    /// Construct a fresh oscillator at the given sample rate (Hz). The
    /// phase is randomised by `PolySaw::new()` — see Phase 2 if voices
    /// should phase-lock instead.
    pub fn new(sample_rate: f32) -> Self {
        let mut saw = PolySaw::<f32>::new();
        saw.set_sample_rate(sample_rate as f64);
        Self { saw }
    }

    /// Render one sample at the given frequency in Hz.
    pub fn tick(&mut self, freq_hz: f32) -> f32 {
        let out = self.saw.tick(&[freq_hz].into());
        out[0]
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    /// PolyBLEP saw output should span -1..+1 over a few cycles.
    #[test]
    fn saw_spans_unipolar_range() {
        let mut osc = Oscillator::new(48_000.0);
        let mut min = f32::INFINITY;
        let mut max = f32::NEG_INFINITY;
        // 4 cycles of 100 Hz = ~1920 samples @ 48 kHz.
        for _ in 0..2400 {
            let v = osc.tick(100.0);
            min = min.min(v);
            max = max.max(v);
        }
        // PolyBLEP introduces a small amount of overshoot at edges, so we
        // assert "close to ±1" rather than exact equality.
        assert!(min < -0.8, "saw min should be near -1, got {min}");
        assert!(max > 0.8, "saw max should be near +1, got {max}");
    }
}
