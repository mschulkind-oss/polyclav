const DEFAULT_CEILING_DB: f32 = -0.3;
const RELEASE_S: f32 = 0.005;
const KNEE_DB: f32 = 3.0;

pub struct Limiter {
    ceiling_db: f32,
    ceiling_lin: f32,
    release_coeff: f32,
    gain: f32,
    /// Worst-case (minimum) gain applied during the most recent `process`
    /// call — the peak gain reduction for that block, read by the limiter
    /// activity indicator. 1.0 means "no reduction this block".
    block_min_gain: f32,
}

impl Limiter {
    pub fn new(sample_rate: f32) -> Self {
        let release_coeff = 1.0 - (-1.0 / (RELEASE_S * sample_rate)).exp();
        let ceiling_db = DEFAULT_CEILING_DB;
        let ceiling_lin = 10f32.powf(DEFAULT_CEILING_DB / 20.0);

        Self {
            ceiling_db,
            ceiling_lin,
            release_coeff,
            gain: 1.0,
            block_min_gain: 1.0,
        }
    }

    pub fn set_ceiling_db(&mut self, db: f32) {
        let d = db.clamp(-12.0, 0.0);
        self.ceiling_db = d;
        self.ceiling_lin = 10f32.powf(d / 20.0);
    }

    #[allow(dead_code)]
    pub fn ceiling_db(&self) -> f32 {
        self.ceiling_db
    }

    /// Minimum gain the limiter applied during the last `process` call.
    /// 1.0 = no reduction (signal stayed under the knee); < 1.0 = the
    /// limiter is compressing. The activity indicator reads this once per
    /// callback.
    pub fn block_min_gain(&self) -> f32 {
        self.block_min_gain
    }

    pub fn process(&mut self, samples: &mut [f32]) {
        let knee_lin = self.ceiling_lin * 10f32.powf(-KNEE_DB / 20.0);
        self.block_min_gain = 1.0;

        for chunk in samples.chunks_exact_mut(2) {
            let l = chunk[0];
            let r = chunk[1];

            let peak = l.abs().max(r.abs());

            let target_gain = if peak <= knee_lin {
                1.0
            } else {
                let over_lin = peak - knee_lin;
                let range = self.ceiling_lin - knee_lin;
                let shaped_excess = range * (over_lin / range).tanh();
                let desired_peak = knee_lin + shaped_excess;
                (desired_peak / peak.max(1e-20)).min(1.0)
            };

            if target_gain < self.gain {
                self.gain = target_gain;
            } else {
                self.gain += self.release_coeff * (target_gain - self.gain);
            }
            if self.gain < self.block_min_gain {
                self.block_min_gain = self.gain;
            }

            chunk[0] = (l * self.gain).clamp(-self.ceiling_lin, self.ceiling_lin);
            chunk[1] = (r * self.gain).clamp(-self.ceiling_lin, self.ceiling_lin);
        }
    }
}

impl Default for Limiter {
    fn default() -> Self {
        Self::new(48_000.0)
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_silence_in_silence_out() {
        let mut limiter = Limiter::new(48_000.0);
        let mut samples = vec![0.0; 4096];
        limiter.process(&mut samples);

        for &s in &samples {
            assert!(s.abs() < 1e-6);
            assert!(s.is_finite());
        }
    }

    #[test]
    fn test_hard_clipping_prevention() {
        let mut limiter = Limiter::new(48_000.0);
        limiter.set_ceiling_db(-6.0);
        let ceiling_lin = 10f32.powf(-6.0 / 20.0);

        let mut samples = vec![0.0; 4096];
        for (i, s) in samples.iter_mut().enumerate() {
            *s = 2.0 * (2.0 * std::f32::consts::PI * 440.0 * i as f32 / 48_000.0).sin();
        }

        limiter.process(&mut samples);

        for &s in &samples {
            assert!(s.abs() <= ceiling_lin + 1e-4);
        }
    }

    #[test]
    fn test_block_min_gain_reports_reduction() {
        let mut limiter = Limiter::new(48_000.0);

        // Silence stays at unity.
        let mut quiet = vec![0.0f32; 256];
        limiter.process(&mut quiet);
        assert_eq!(limiter.block_min_gain(), 1.0);

        // A burst well over the ceiling reduces gain this block; the value
        // is the peak reduction, strictly between 0 and 1.
        let mut loud = vec![4.0f32; 256];
        limiter.process(&mut loud);
        let min_gain = limiter.block_min_gain();
        assert!(
            min_gain > 0.0 && min_gain < 1.0,
            "expected a reduction below unity, got {min_gain}"
        );

        // The next quiet block does not jump back to unity: the limiter is
        // still releasing, so the block minimum is the gain it entered with.
        limiter.process(&mut quiet);
        assert!(limiter.block_min_gain() < 1.0);

        // Once the release has settled, a quiet block reports unity again
        // (the exponential release approaches it asymptotically).
        let mut settle = vec![0.0f32; 4_800]; // 100 ms — release fully settles
        limiter.process(&mut settle);
        limiter.process(&mut settle);
        assert!(
            limiter.block_min_gain() > 0.999,
            "expected full recovery, got {}",
            limiter.block_min_gain()
        );
    }

    #[test]
    fn test_no_nan_on_loud_input() {
        let mut limiter = Limiter::new(48_000.0);
        let mut samples = vec![0.0; 4096];
        for (i, s) in samples.iter_mut().enumerate() {
            *s = if i % 2 == 0 { 5.0 } else { -5.0 };
        }

        limiter.process(&mut samples);

        for &s in &samples {
            assert!(s.is_finite());
        }
    }

    #[test]
    fn test_set_ceiling_db_clamps() {
        let mut limiter = Limiter::new(48_000.0);
        limiter.set_ceiling_db(-20.0);
        assert_eq!(limiter.ceiling_db(), -12.0);

        limiter.set_ceiling_db(5.0);
        assert_eq!(limiter.ceiling_db(), 0.0);
    }
}
