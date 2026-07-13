//! Implements ITU-R BS.1770-4 K-weighted loudness measurement (the LUFS standard used by EBU R128
//! and most loudness-normalization tooling): a two-stage IIR pre-filter (a high-shelf "pre-filter"
//! stage approximating a simplified head model, followed by an "RLB" high-pass weighting stage)
//! applied per channel, then mean-square power averaged over the whole buffer, combined across
//! channels, and converted to LUFS via `-0.691 + 10*log10(sum of per-channel mean squares)`.
//!
//! This is INTEGRATED, UNGATED loudness — it skips BS.1770's relative-gating step (which excludes
//! quiet passages when measuring a long broadcast program), which is fine for short,
//! mostly-continuous calibration/test clips but is NOT a full broadcast-compliance implementation.
//!
//! The K-weighting biquad coefficients used here are the standard values from BS.1770-4's
//! informative annex for 48 kHz sampling — this module assumes 48 kHz input (matching the crate's
//! SAMPLE_RATE) and does not generalize to other rates.
//!
//! This exists to support invariant testing (e.g. "does a knob sweep change loudness smoothly,
//! does patch X sound about as loud as patch Y") — see docs/OPEN_SOUND_ENGINES.md and
//! docs/VISION.md for the broader initiative this is part of.

#[derive(Clone, Copy)]
struct Biquad {
    b0: f64,
    b1: f64,
    b2: f64,
    a1: f64,
    a2: f64,
    x1: f64,
    x2: f64,
    y1: f64,
    y2: f64,
}

impl Biquad {
    fn new(b0: f64, b1: f64, b2: f64, a1: f64, a2: f64) -> Self {
        Self {
            b0,
            b1,
            b2,
            a1,
            a2,
            x1: 0.0,
            x2: 0.0,
            y1: 0.0,
            y2: 0.0,
        }
    }

    #[inline]
    fn tick(&mut self, x: f64) -> f64 {
        let y = self.b0 * x + self.b1 * self.x1 + self.b2 * self.x2
            - self.a1 * self.y1
            - self.a2 * self.y2;
        self.x2 = self.x1;
        self.x1 = x;
        self.y2 = self.y1;
        self.y1 = y;
        y
    }
}

#[derive(Clone, Copy)]
struct KWeightingFilter {
    shelf: Biquad,
    highpass: Biquad,
}

impl KWeightingFilter {
    fn new() -> Self {
        Self {
            shelf: Biquad::new(
                1.53512485958697,
                -2.69169618940638,
                1.19839281085285,
                -1.69065929318241,
                0.73248077421585,
            ),
            highpass: Biquad::new(1.0, -2.0, 1.0, -1.99004745483398, 0.99007225036621),
        }
    }

    #[inline]
    fn process(&mut self, x: f64) -> f64 {
        self.highpass.tick(self.shelf.tick(x))
    }
}

pub fn measure_lufs(samples: &[f32]) -> f32 {
    let mut filter_l = KWeightingFilter::new();
    let mut filter_r = KWeightingFilter::new();

    let mut sum_l: f64 = 0.0;
    let mut sum_r: f64 = 0.0;
    let mut frame_count: u64 = 0;

    for chunk in samples.chunks_exact(2) {
        let l = chunk[0] as f64;
        let r = chunk[1] as f64;

        let kl = filter_l.process(l);
        let kr = filter_r.process(r);

        sum_l += kl * kl;
        sum_r += kr * kr;
        frame_count += 1;
    }

    if frame_count == 0 {
        return f32::NEG_INFINITY;
    }

    let z_l = sum_l / frame_count as f64;
    let z_r = sum_r / frame_count as f64;

    let total = z_l + z_r;

    if total <= 0.0 {
        return f32::NEG_INFINITY;
    }

    (-0.691 + 10.0 * total.log10()) as f32
}

pub fn measure_peak_dbfs(samples: &[f32]) -> f32 {
    let peak = samples.iter().fold(0.0f32, |acc, &s| acc.max(s.abs()));

    if peak <= 0.0 {
        return f32::NEG_INFINITY;
    }

    20.0 * peak.log10()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_silence_measures_negative_infinity() {
        let samples = vec![0.0f32; 4800];
        let lufs = measure_lufs(&samples);
        let peak = measure_peak_dbfs(&samples);
        assert!(lufs.is_infinite() && lufs.is_sign_negative());
        assert!(peak.is_infinite() && peak.is_sign_negative());
    }

    #[test]
    fn test_doubling_amplitude_raises_lufs_by_6db() {
        let sample_rate = 48000.0;
        let frames = 9600;
        let freq = 1000.0;

        let mut samples_low = vec![0.0f32; frames * 2];
        let mut samples_high = vec![0.0f32; frames * 2];

        for i in 0..frames {
            let t = i as f32 / sample_rate;
            let val = (2.0 * std::f32::consts::PI * freq * t).sin();
            samples_low[i * 2] = val * 0.25;
            samples_low[i * 2 + 1] = val * 0.25;
            samples_high[i * 2] = val * 0.5;
            samples_high[i * 2 + 1] = val * 0.5;
        }

        let lufs_low = measure_lufs(&samples_low);
        let lufs_high = measure_lufs(&samples_high);
        let diff = lufs_high - lufs_low;
        let expected = 20.0 * 2.0_f32.log10();

        assert!((diff - expected).abs() < 0.05);
    }

    #[test]
    fn test_higher_amplitude_is_louder() {
        let sample_rate = 48000.0;
        let frames = 9600;
        let freq = 1000.0;
        let amplitudes = [0.1, 0.3, 0.9];

        let mut lufs_values = Vec::new();

        for amp in amplitudes {
            let mut samples = vec![0.0f32; frames * 2];
            for i in 0..frames {
                let t = i as f32 / sample_rate;
                let val = (2.0 * std::f32::consts::PI * freq * t).sin();
                samples[i * 2] = val * amp;
                samples[i * 2 + 1] = val * amp;
            }
            lufs_values.push(measure_lufs(&samples));
        }

        assert!(lufs_values[0] < lufs_values[1]);
        assert!(lufs_values[1] < lufs_values[2]);
    }

    #[test]
    fn test_peak_dbfs_unity_sine_is_near_zero() {
        let sample_rate = 48000.0;
        let frames = 9600;
        let freq = 480.0;

        let mut samples = vec![0.0f32; frames * 2];
        for i in 0..frames {
            let t = i as f32 / sample_rate;
            let val = (2.0 * std::f32::consts::PI * freq * t).sin();
            samples[i * 2] = val;
            samples[i * 2 + 1] = val;
        }

        let peak_db = measure_peak_dbfs(&samples);
        assert!((peak_db - 0.0).abs() < 0.1);
    }

    #[test]
    fn test_peak_dbfs_half_amplitude_is_minus_6db() {
        let sample_rate = 48000.0;
        let frames = 9600;
        let freq = 480.0;

        let mut samples = vec![0.0f32; frames * 2];
        for i in 0..frames {
            let t = i as f32 / sample_rate;
            let val = (2.0 * std::f32::consts::PI * freq * t).sin();
            samples[i * 2] = val * 0.5;
            samples[i * 2 + 1] = val * 0.5;
        }

        let peak_db = measure_peak_dbfs(&samples);
        let expected = 20.0 * 0.5_f32.log10();
        assert!((peak_db - expected).abs() < 0.1);
    }

    #[test]
    fn test_lufs_and_peak_dont_panic_on_odd_length_input() {
        let buf = vec![0.1f32; 4801];
        let _ = measure_lufs(&buf);
        let _ = measure_peak_dbfs(&buf);
    }
}
