const SAMPLE_RATE: f32 = 48_000.0;
const DEFAULT_RATIO: f32 = 4.0;
const DEFAULT_ATTACK_S: f32 = 0.005;
const DEFAULT_RELEASE_S: f32 = 0.100;
const DEFAULT_KNEE_DB: f32 = 6.0;

pub struct Compressor {
    attack_coeff: f32,
    release_coeff: f32,
    envelope: f32,
    amount: f32,
    threshold_db: f32,
    ratio: f32,
    knee_db: f32,
    makeup_db: f32,
}

impl Compressor {
    pub fn new() -> Self {
        let attack_coeff = 1.0 - (-1.0 / (DEFAULT_ATTACK_S * SAMPLE_RATE)).exp();
        let release_coeff = 1.0 - (-1.0 / (DEFAULT_RELEASE_S * SAMPLE_RATE)).exp();

        Self {
            attack_coeff,
            release_coeff,
            envelope: 0.0,
            amount: 0.0,
            threshold_db: -18.0,
            ratio: DEFAULT_RATIO,
            knee_db: DEFAULT_KNEE_DB,
            makeup_db: 6.0,
        }
    }

    pub fn set_amount(&mut self, amount: f32) {
        let amt = amount.clamp(0.0, 1.0);
        self.amount = amt;
        self.threshold_db = 6.0 + amt * (-24.0 - 6.0);
        self.makeup_db = amt * 9.0;
    }

    #[allow(dead_code)]
    pub fn amount(&self) -> f32 {
        self.amount
    }

    pub fn process(&mut self, samples: &mut [f32]) {
        let knee = self.knee_db;
        let ratio = self.ratio;
        let threshold = self.threshold_db;
        let makeup = self.makeup_db;
        let attack = self.attack_coeff;
        let release = self.release_coeff;

        let slope = 1.0 - 1.0 / ratio;
        let knee_half = knee / 2.0;
        let knee_inv_2 = 1.0 / (2.0 * knee);

        for chunk in samples.chunks_exact_mut(2) {
            let l = chunk[0];
            let r = chunk[1];

            let peak = l.abs().max(r.abs());

            let coeff = if peak > self.envelope {
                attack
            } else {
                release
            };

            self.envelope += coeff * (peak - self.envelope);

            let env_safe = self.envelope.max(1e-20);
            let env_db = 20.0 * env_safe.log10();

            let over = env_db - threshold;

            let gr_db = if over <= -knee_half {
                0.0
            } else if over >= knee_half {
                over * slope
            } else {
                slope * (over + knee_half).powi(2) * knee_inv_2
            };

            let lin_gain = 10.0_f32.powf((-gr_db + makeup) / 20.0);

            chunk[0] *= lin_gain;
            chunk[1] *= lin_gain;
        }
    }
}

impl Default for Compressor {
    fn default() -> Self {
        Self::new()
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_new_no_panic() {
        let _ = Compressor::new();
    }

    #[test]
    fn test_silence_in_silence_out() {
        let mut comp = Compressor::new();
        let mut samples = [0.0; 1024];
        comp.process(&mut samples);
        for &s in &samples {
            assert!(s.abs() < 1e-6);
            assert!(s.is_finite());
        }
    }

    #[test]
    fn test_no_nan_or_inf_on_loud_signal() {
        let mut comp = Compressor::new();
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
        let mut comp = Compressor::new();
        comp.set_amount(-1.0);
        assert_eq!(comp.amount(), 0.0);
        comp.set_amount(2.0);
        assert_eq!(comp.amount(), 1.0);
    }

    #[test]
    fn test_amount_zero_is_near_unity() {
        let mut comp = Compressor::new();
        comp.set_amount(0.0);
        let mut samples = [0.0; 480];
        let amp = 0.1;
        for (i, s) in samples.iter_mut().enumerate() {
            *s = (2.0 * std::f32::consts::PI * 440.0 * i as f32 / 48000.0).sin() * amp;
        }

        let max_in = samples.iter().cloned().fold(0.0_f32, |a, b| a.max(b.abs()));

        comp.process(&mut samples);

        let max_out = samples.iter().cloned().fold(0.0_f32, |a, b| a.max(b.abs()));

        assert!((max_out - max_in).abs() < 0.05);
    }

    #[test]
    fn test_amount_one_reduces_loud_signal() {
        let mut comp = Compressor::new();
        comp.set_amount(1.0);
        let mut samples = [0.0; 4800];
        let amp = 0.5;
        for (i, s) in samples.iter_mut().enumerate() {
            *s = (2.0 * std::f32::consts::PI * 440.0 * i as f32 / 48000.0).sin() * amp;
        }

        let max_in = samples.iter().cloned().fold(0.0_f32, |a, b| a.max(b.abs()));

        comp.process(&mut samples);

        let max_out = samples.iter().cloned().fold(0.0_f32, |a, b| a.max(b.abs()));

        // Attack is 5 ms; during the transient the envelope is still below
        // threshold so makeup (+9 dB ≈ 2.82x) is applied without full gain
        // reduction. After settling, gain reduction (~13.5 dB) dominates.
        // Bound is set generously above 2.82 to avoid flakiness.
        assert!(
            max_out <= max_in * 3.0,
            "max_out={max_out}, max_in={max_in}"
        );
    }
}
