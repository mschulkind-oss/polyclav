const DEFAULT_RATIO: f32 = 2.0;
/// 2 ms attack: fast enough that the peak-preserving makeup can't overshoot a
/// transient into the limiter (a lagging envelope would apply the makeup
/// before it applied the matching gain reduction).
const DEFAULT_ATTACK_S: f32 = 0.002;
const DEFAULT_RELEASE_S: f32 = 0.100;
const DEFAULT_KNEE_DB: f32 = 6.0;
/// Adaptive peak-reference decay, dB/s: slow enough to hold a section's level,
/// fast enough to follow a patch change or a long quiet passage.
const PEAK_REF_DECAY_DB_PER_SEC: f32 = 12.0;

/// Soft-knee gain reduction in dB for an input `over` dB above threshold.
fn knee_gr_db(over: f32, slope: f32, knee_half: f32, knee_inv_2: f32) -> f32 {
    if over <= -knee_half {
        0.0
    } else if over >= knee_half {
        over * slope
    } else {
        slope * (over + knee_half).powi(2) * knee_inv_2
    }
}

pub struct MasteringCompressor {
    attack_coeff: f32,
    release_coeff: f32,
    envelope: f32,
    amount: f32,
    threshold_db: f32,
    ratio: f32,
    knee_db: f32,
    /// Adaptive peak reference (dBFS): rises instantly to a new peak, decays
    /// slowly. The makeup is the reduction applied *here*, so peaks are left
    /// where they were and the body below them is lifted — loudness without
    /// pushing anything new into the limiter.
    peak_ref_db: f32,
    peak_decay_db: f32,
}

impl MasteringCompressor {
    pub fn new(sample_rate: f32) -> Self {
        let attack_coeff = 1.0 - (-1.0 / (DEFAULT_ATTACK_S * sample_rate)).exp();
        let release_coeff = 1.0 - (-1.0 / (DEFAULT_RELEASE_S * sample_rate)).exp();
        Self {
            attack_coeff,
            release_coeff,
            envelope: 0.0,
            amount: 0.0,
            threshold_db: 0.0,
            ratio: DEFAULT_RATIO,
            knee_db: DEFAULT_KNEE_DB,
            peak_ref_db: -120.0,
            peak_decay_db: PEAK_REF_DECAY_DB_PER_SEC / sample_rate,
        }
    }

    pub fn set_amount(&mut self, amount: f32) {
        let amt = amount.clamp(0.0, 1.0);
        self.amount = amt;
        // More amount lowers the threshold AND raises the ratio: at 0 it is
        // transparent (0 dBFS threshold, 2:1), at 1 it is a hard leveler
        // (-30 dBFS threshold, 10:1).
        self.threshold_db = -30.0 * amt;
        self.ratio = 2.0 + amt * 8.0;
    }

    #[allow(dead_code)]
    pub fn amount(&self) -> f32 {
        self.amount
    }

    pub fn process(&mut self, samples: &mut [f32]) {
        let threshold_db = self.threshold_db;
        let knee_half = self.knee_db / 2.0;
        let slope = 1.0 - 1.0 / self.ratio;
        let knee_inv_2 = 1.0 / (2.0 * self.knee_db);
        let attack_coeff = self.attack_coeff;
        let release_coeff = self.release_coeff;

        for chunk in samples.chunks_exact_mut(2) {
            let l = chunk[0];
            let r = chunk[1];

            let peak = l.abs().max(r.abs());

            let coeff = if peak > self.envelope {
                attack_coeff
            } else {
                release_coeff
            };

            self.envelope += coeff * (peak - self.envelope);

            let env_safe = self.envelope.max(1e-20);
            let env_db = 20.0 * env_safe.log10();

            if env_db > self.peak_ref_db {
                self.peak_ref_db = env_db;
            } else {
                self.peak_ref_db = (self.peak_ref_db - self.peak_decay_db).max(-120.0);
            }

            let gr_db = knee_gr_db(env_db - threshold_db, slope, knee_half, knee_inv_2);
            let makeup_db = knee_gr_db(
                self.peak_ref_db - threshold_db,
                slope,
                knee_half,
                knee_inv_2,
            )
            .max(0.0);

            let lin_gain = 10.0_f32.powf((-gr_db + makeup_db) / 20.0);

            chunk[0] *= lin_gain;
            chunk[1] *= lin_gain;
        }
    }
}

impl Default for MasteringCompressor {
    fn default() -> Self {
        Self::new(48_000.0)
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_silence_in_silence_out() {
        let mut comp = MasteringCompressor::new(48_000.0);
        comp.set_amount(1.0);
        let mut samples = vec![0.0; 4096];
        comp.process(&mut samples);
        for &s in &samples {
            assert!(s.abs() < 1e-6);
            assert!(s.is_finite());
        }
    }

    #[test]
    fn test_no_nan_on_loud_input() {
        let mut comp = MasteringCompressor::new(48_000.0);
        comp.set_amount(1.0);
        let mut samples = vec![0.0; 4096];
        for (i, s) in samples.iter_mut().enumerate() {
            *s = if i % 2 == 0 { 1.0 } else { -1.0 };
        }
        comp.process(&mut samples);
        for &s in &samples {
            assert!(s.is_finite());
        }
    }

    #[test]
    fn test_set_amount_clamps() {
        let mut comp = MasteringCompressor::new(48_000.0);
        comp.set_amount(-1.0);
        assert_eq!(comp.amount(), 0.0);
        comp.set_amount(2.0);
        assert_eq!(comp.amount(), 1.0);
    }

    #[test]
    fn test_amount_zero_is_near_unity() {
        let mut comp = MasteringCompressor::new(48_000.0);
        comp.set_amount(0.0);
        let mut samples = vec![0.0; 480];
        let sample_rate = 48_000.0;
        let freq = 440.0;
        let amp = 0.1;
        for i in 0..240 {
            let t = i as f32 / sample_rate;
            let val = (2.0 * std::f32::consts::PI * freq * t).sin() * amp;
            samples[i * 2] = val;
            samples[i * 2 + 1] = val;
        }

        let max_in = samples.iter().cloned().fold(0.0_f32, |a, b| a.max(b.abs()));

        comp.process(&mut samples);

        let max_out = samples.iter().cloned().fold(0.0_f32, |a, b| a.max(b.abs()));

        assert!((max_out - max_in).abs() < 0.05);
    }

    /// Regression: more amount must never be *quieter*. The old makeup
    /// (`-threshold/2`) under-compensated the threshold drop, so 100% came out
    /// quieter than 75% while clipping harder — the "compression all the way
    /// up loses loudness" report.
    #[test]
    fn test_more_amount_is_never_quieter() {
        const N: usize = 48_000;
        let mut prev = f32::NEG_INFINITY;
        let mut prev_amt = 0.0;
        for step in 0..=10 {
            let amt = step as f32 / 10.0;
            let mut comp = MasteringCompressor::new(48_000.0);
            comp.set_amount(amt);

            // 1 kHz sine at -12 dBFS, long enough for the envelope to settle.
            let mut buf = vec![0.0f32; N * 2];
            for i in 0..N {
                let v = (2.0 * std::f32::consts::PI * 1000.0 * i as f32 / 48_000.0).sin() * 0.25;
                buf[i * 2] = v;
                buf[i * 2 + 1] = v;
            }
            comp.process(&mut buf);

            let tail = &buf[N..]; // second half: attack settled
            let rms = (tail.iter().map(|s| s * s).sum::<f32>() / tail.len() as f32).sqrt();
            assert!(
                rms >= prev - 1e-4,
                "amount {amt} (rms {rms:.5}) is quieter than amount {prev_amt} (rms {prev:.5})"
            );
            prev = rms;
            prev_amt = amt;
        }
    }
}
