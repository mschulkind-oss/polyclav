//! The mastering (bus) compressor: the primary dynamics device of the master
//! tail, with the brick-wall limiter behind it as the fallback. Every
//! character parameter is live — the master gain feeds it, the threshold
//! control sets how far below the ceiling the squash begins, and the makeup
//! is tied to the threshold, so the compressed curve is fixed and predictable.

/// Defaults and clamps for the live parameters. The defaults are the values
/// this stage shipped with; every `set_*` clamps into the documented range.
pub const DEFAULT_RATIO: f32 = 8.0;
pub const MIN_RATIO: f32 = 1.0;
pub const MAX_RATIO: f32 = 20.0;
/// How far the threshold drops below the ceiling at amount 1.0, in dB.
pub const DEFAULT_RANGE_DB: f32 = 30.0;
pub const MAX_RANGE_DB: f32 = 48.0;
/// Headroom the fixed makeup leaves under the ceiling. The makeup is tied to
/// the threshold, so without this it anchors the compressed curve exactly on
/// the ceiling, leaving the master nothing to drive into the limiter. The
/// master can add `headroom_db * ratio` dB of drive before the peak reaches
/// the ceiling.
pub const DEFAULT_HEADROOM_DB: f32 = 2.0;
pub const MAX_HEADROOM_DB: f32 = 24.0;
pub const DEFAULT_ATTACK_MS: f32 = 2.0;
pub const MIN_ATTACK_MS: f32 = 0.1;
pub const MAX_ATTACK_MS: f32 = 200.0;
pub const DEFAULT_RELEASE_MS: f32 = 100.0;
pub const MIN_RELEASE_MS: f32 = 1.0;
pub const MAX_RELEASE_MS: f32 = 2000.0;
/// Ceiling of the soft-knee width. A narrow window (low amount) gets a narrow
/// knee, so the onset hardens as you push the threshold down.
const MAX_KNEE_DB: f32 = 6.0;
const DEFAULT_CEILING_DB: f32 = -0.3;

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

/// Time constant for a one-pole coefficient: `1 - e^(-1 / (t * rate))`.
fn coeff(time_s: f32, rate: f32) -> f32 {
    1.0 - (-1.0 / (time_s.max(1e-6) * rate)).exp()
}

pub struct MasteringCompressor {
    sample_rate: f32,
    attack_ms: f32,
    release_ms: f32,
    attack_coeff: f32,
    release_coeff: f32,
    envelope: f32,
    /// Control position in [0, 1] — the threshold depth (see `set_amount`).
    amount: f32,
    ratio: f32,
    /// How far the threshold reaches below the ceiling at amount 1.0.
    range_db: f32,
    /// Headroom left under the ceiling by the fixed makeup.
    headroom_db: f32,
    /// `range_db * amount`: the current threshold depth.
    window_db: f32,
    threshold_db: f32,
    knee_db: f32,
    /// The limiter's ceiling: the threshold's origin and the anchor the fixed
    /// makeup compensates the threshold drop against.
    ceiling_db: f32,
    /// Post-compressor makeup in dB, derived from the threshold depth: the
    /// reduction a signal *at the ceiling* would receive, minus `headroom_db`.
    /// Tied to the threshold, not the signal.
    makeup_db: f32,
    /// Worst-case (minimum) net linear gain applied during the most recent
    /// `process` call — 1.0 is unity, and it may exceed 1.0 when the makeup
    /// lifts a signal. Read by the web level meter.
    block_min_gain: f32,
    /// Largest input peak (linear) seen during the most recent `process` call.
    block_max_in_peak: f32,
    /// Largest output peak (linear, after gain) seen during the most recent
    /// `process` call.
    block_max_out_peak: f32,
}

impl MasteringCompressor {
    pub fn new(sample_rate: f32) -> Self {
        let mut comp = Self {
            sample_rate,
            attack_ms: DEFAULT_ATTACK_MS,
            release_ms: DEFAULT_RELEASE_MS,
            attack_coeff: coeff(DEFAULT_ATTACK_MS / 1000.0, sample_rate),
            release_coeff: coeff(DEFAULT_RELEASE_MS / 1000.0, sample_rate),
            envelope: 0.0,
            amount: 0.0,
            ratio: DEFAULT_RATIO,
            range_db: DEFAULT_RANGE_DB,
            headroom_db: DEFAULT_HEADROOM_DB,
            window_db: 0.0,
            threshold_db: DEFAULT_CEILING_DB,
            knee_db: 0.0,
            ceiling_db: DEFAULT_CEILING_DB,
            makeup_db: 0.0,
            block_min_gain: 1.0,
            block_max_in_peak: 0.0,
            block_max_out_peak: 0.0,
        };
        comp.recompute();
        comp
    }

    /// Set the control position in [0, 1] — the threshold depth. At 0 the
    /// threshold is the ceiling (no compression); at 1 it is `range_db` below
    /// it.
    pub fn set_amount(&mut self, amount: f32) {
        if !amount.is_finite() {
            return;
        }
        self.amount = amount.clamp(0.0, 1.0);
        self.recompute();
    }

    /// Compression ratio. Fixed by default, but live so the character can be
    /// dialed in.
    pub fn set_ratio(&mut self, ratio: f32) {
        if !ratio.is_finite() {
            return;
        }
        self.ratio = ratio.clamp(MIN_RATIO, MAX_RATIO);
        self.recompute();
    }

    /// How far the threshold reaches below the ceiling at amount 1.0, in dB.
    pub fn set_range_db(&mut self, db: f32) {
        if !db.is_finite() {
            return;
        }
        self.range_db = db.clamp(0.0, MAX_RANGE_DB);
        self.recompute();
    }

    /// Headroom the makeup leaves under the ceiling, in dB.
    pub fn set_headroom_db(&mut self, db: f32) {
        if !db.is_finite() {
            return;
        }
        self.headroom_db = db.clamp(0.0, MAX_HEADROOM_DB);
        self.recompute();
    }

    pub fn set_attack_ms(&mut self, ms: f32) {
        if !ms.is_finite() {
            return;
        }
        self.attack_ms = ms.clamp(MIN_ATTACK_MS, MAX_ATTACK_MS);
        self.attack_coeff = coeff(self.attack_ms / 1000.0, self.sample_rate);
    }

    pub fn set_release_ms(&mut self, ms: f32) {
        if !ms.is_finite() {
            return;
        }
        self.release_ms = ms.clamp(MIN_RELEASE_MS, MAX_RELEASE_MS);
        self.release_coeff = coeff(self.release_ms / 1000.0, self.sample_rate);
    }

    /// Track the limiter ceiling: the threshold's origin, so the compressor's
    /// window and its makeup anchor move with the limiter as the ceiling
    /// changes.
    pub fn set_ceiling_db(&mut self, db: f32) {
        if !db.is_finite() {
            return;
        }
        self.ceiling_db = db.clamp(-12.0, 0.0);
        self.recompute();
    }

    fn recompute(&mut self) {
        self.window_db = self.range_db * self.amount;
        self.threshold_db = self.ceiling_db - self.window_db;
        self.knee_db = self.window_db.min(MAX_KNEE_DB);
        // A signal at the ceiling is `window_db` over the threshold, so it is
        // reduced by `window_db * (1 - 1/R)`. Compensating that (less the
        // headroom) puts the compressed curve back at the ceiling.
        self.makeup_db = (self.window_db * (1.0 - 1.0 / self.ratio) - self.headroom_db).max(0.0);
    }

    #[allow(dead_code)]
    pub fn amount(&self) -> f32 {
        self.amount
    }

    /// Minimum net linear gain applied during the last `process` call. 1.0 =
    /// unity; below 1.0 the compressor is reducing, above 1.0 the makeup is
    /// lifting. Read once per callback by the web level meter.
    pub fn block_min_gain(&self) -> f32 {
        self.block_min_gain
    }

    /// Largest input peak (linear) seen during the last `process` call.
    pub fn block_max_in_peak(&self) -> f32 {
        self.block_max_in_peak
    }

    /// Largest output peak (linear, after gain) seen during the last `process`
    /// call.
    pub fn block_max_out_peak(&self) -> f32 {
        self.block_max_out_peak
    }

    /// Reset the per-block stats to idle (unity gain, no signal). The bypass
    /// path calls this so a block the compressor skipped reports idle rather
    /// than the previous block's values.
    pub fn reset_block(&mut self) {
        self.block_min_gain = 1.0;
        self.block_max_in_peak = 0.0;
        self.block_max_out_peak = 0.0;
    }

    pub fn process(&mut self, samples: &mut [f32]) {
        self.reset_block();
        let threshold_db = self.threshold_db;
        let knee_half = self.knee_db / 2.0;
        let slope = 1.0 - 1.0 / self.ratio;
        let knee_inv_2 = if self.knee_db > 0.0 {
            1.0 / (2.0 * self.knee_db)
        } else {
            0.0
        };
        let makeup_db = self.makeup_db;
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

            let gr_db = knee_gr_db(env_db - threshold_db, slope, knee_half, knee_inv_2);
            let lin_gain = 10.0_f32.powf((makeup_db - gr_db) / 20.0);

            chunk[0] *= lin_gain;
            chunk[1] *= lin_gain;

            if lin_gain < self.block_min_gain {
                self.block_min_gain = lin_gain;
            }
            if peak > self.block_max_in_peak {
                self.block_max_in_peak = peak;
            }
            let out_peak = chunk[0].abs().max(chunk[1].abs());
            if out_peak > self.block_max_out_peak {
                self.block_max_out_peak = out_peak;
            }
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

    const RATE: f32 = 48_000.0;

    fn sine_buf(freq: f32, amp: f32, n: usize) -> Vec<f32> {
        let mut buf = vec![0.0f32; n * 2];
        for i in 0..n {
            let v = (2.0 * std::f32::consts::PI * freq * i as f32 / RATE).sin() * amp;
            buf[i * 2] = v;
            buf[i * 2 + 1] = v;
        }
        buf
    }

    fn peak_db(buf: &[f32]) -> f32 {
        let peak = buf.iter().cloned().fold(0.0_f32, |a, b| a.max(b.abs()));
        20.0 * peak.max(1e-20).log10()
    }

    #[test]
    fn test_silence_in_silence_out() {
        let mut comp = MasteringCompressor::new(RATE);
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
        let mut comp = MasteringCompressor::new(RATE);
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
    fn test_setters_clamp_and_survive_nan() {
        let mut comp = MasteringCompressor::new(RATE);
        comp.set_amount(2.0);
        assert_eq!(comp.amount(), 1.0);
        comp.set_ratio(100.0);
        assert_eq!(comp.ratio, MAX_RATIO);
        comp.set_ratio(0.0);
        assert_eq!(comp.ratio, MIN_RATIO);
        comp.set_range_db(1000.0);
        assert_eq!(comp.range_db, MAX_RANGE_DB);
        comp.set_headroom_db(-5.0);
        assert_eq!(comp.headroom_db, 0.0);
        comp.set_attack_ms(0.0);
        assert_eq!(comp.attack_ms, MIN_ATTACK_MS);
        comp.set_release_ms(1e9);
        assert_eq!(comp.release_ms, MAX_RELEASE_MS);
        // Non-finite input must not poison the coefficients.
        comp.set_attack_ms(f32::NAN);
        assert!(comp.attack_coeff.is_finite());
        comp.set_ratio(f32::NAN);
        assert!(comp.ratio.is_finite());
    }

    #[test]
    fn test_amount_zero_is_near_unity() {
        let mut comp = MasteringCompressor::new(RATE);
        comp.set_amount(0.0);
        let mut samples = sine_buf(440.0, 0.1, 240);
        let max_in = samples.iter().cloned().fold(0.0_f32, |a, b| a.max(b.abs()));
        comp.process(&mut samples);
        let max_out = samples.iter().cloned().fold(0.0_f32, |a, b| a.max(b.abs()));
        assert!((max_out - max_in).abs() < 0.05);
    }

    /// Block min gain reports reduction and restarts each block: silence stays
    /// at unity, a loud burst drives it below unity, and the next block resets
    /// to unity before tracking again.
    #[test]
    fn test_block_min_gain_reports_reduction() {
        let mut comp = MasteringCompressor::new(RATE);
        comp.set_amount(1.0);

        let mut quiet = vec![0.0f32; 256];
        comp.process(&mut quiet);
        assert_eq!(comp.block_min_gain(), 1.0);

        let mut loud = vec![4.0f32; 256];
        comp.process(&mut loud);
        let min_gain = comp.block_min_gain();
        assert!(
            min_gain > 0.0 && min_gain < 1.0,
            "expected a reduction below unity, got {min_gain}"
        );

        // The next quiet block does not jump back to unity: the compressor is
        // still releasing, so the block minimum is the gain it entered with.
        comp.process(&mut quiet);
        assert!(comp.block_min_gain() < 1.0);

        // Once the release has settled, a quiet block reports unity again (the
        // reset at the top clears the previous minimum). The 100 ms release
        // needs a long tail, hence 250 ms blocks.
        let mut settle = vec![0.0f32; 24_000]; // 250 ms
        comp.process(&mut settle);
        comp.process(&mut settle);
        assert!(
            comp.block_min_gain() > 0.999,
            "expected full recovery, got {}",
            comp.block_min_gain()
        );
    }

    /// Max in/out peak track the block's extremes. With amount 0 the compressor
    /// is transparent, so both peaks equal the input peak.
    #[test]
    fn test_block_peaks_track_input_and_output() {
        let mut comp = MasteringCompressor::new(RATE);
        comp.set_amount(0.0);

        let mut buf = vec![0.5f32; 256];
        comp.process(&mut buf);
        assert!((comp.block_max_in_peak() - 0.5).abs() < 1e-6);
        assert!((comp.block_max_out_peak() - 0.5).abs() < 1e-6);

        let mut quiet = vec![0.0f32; 256];
        comp.process(&mut quiet);
        assert_eq!(comp.block_max_in_peak(), 0.0);
        assert_eq!(comp.block_max_out_peak(), 0.0);
    }

    /// reset_block restores the idle state without running a block — the bypass
    /// path uses it so a skipped compressor reports idle, not stale stats.
    #[test]
    fn test_reset_block_restores_idle_stats() {
        let mut comp = MasteringCompressor::new(RATE);
        comp.set_amount(1.0);
        let mut loud = vec![4.0f32; 256];
        comp.process(&mut loud);
        assert!(comp.block_min_gain() < 1.0);

        comp.reset_block();
        assert_eq!(comp.block_min_gain(), 1.0);
        assert_eq!(comp.block_max_in_peak(), 0.0);
        assert_eq!(comp.block_max_out_peak(), 0.0);
    }

    /// The threshold starts at the ceiling and comes down with the amount, so
    /// 0 is no compression and 1 is `range_db` below the ceiling.
    #[test]
    fn test_amount_is_threshold_depth_from_the_ceiling() {
        let mut comp = MasteringCompressor::new(RATE);
        comp.set_ratio(DEFAULT_RATIO);
        comp.set_range_db(DEFAULT_RANGE_DB);
        comp.set_amount(0.0);
        assert!((comp.threshold_db - comp.ceiling_db).abs() < 1e-4);
        comp.set_amount(0.5);
        assert!((comp.threshold_db - (comp.ceiling_db - DEFAULT_RANGE_DB * 0.5)).abs() < 1e-4);
        comp.set_amount(1.0);
        assert!((comp.threshold_db - (comp.ceiling_db - DEFAULT_RANGE_DB)).abs() < 1e-4);
    }

    /// The threshold's origin is the limiter ceiling, so lowering the ceiling
    /// moves the whole window down with it.
    #[test]
    fn test_threshold_tracks_the_ceiling() {
        let mut comp = MasteringCompressor::new(RATE);
        comp.set_amount(0.5);
        comp.set_ceiling_db(-6.0);
        assert!((comp.threshold_db - (-6.0 - DEFAULT_RANGE_DB * 0.5)).abs() < 1e-4);
        comp.set_ceiling_db(-0.3);
        assert!((comp.threshold_db - (-0.3 - DEFAULT_RANGE_DB * 0.5)).abs() < 1e-4);
    }

    /// The makeup is tied to the threshold, not the signal: a full-scale input
    /// is anchored `headroom_db` under the ceiling regardless of what else is
    /// playing, so the curve is fixed.
    #[test]
    fn test_makeup_anchors_a_full_scale_signal_at_the_headroom() {
        let mut comp = MasteringCompressor::new(RATE);
        comp.set_headroom_db(2.0);
        comp.set_amount(1.0);
        // Alternating full-scale: the peak follower settles exactly on 1.0, so
        // the test measures the makeup anchor, not envelope lag.
        let mut buf = vec![0.0f32; 8192];
        for (i, s) in buf.iter_mut().enumerate() {
            *s = if i % 2 == 0 { 1.0 } else { -1.0 };
        }
        comp.process(&mut buf);
        let out = peak_db(&buf[4096..]);
        let want = comp.ceiling_db - comp.headroom_db;
        assert!(
            (out - want).abs() < 0.2,
            "full-scale input landed at {out:.2} dBFS, want the {want:.2} dBFS anchor"
        );
    }

    /// More amount must never be *quieter*: the threshold-tied makeup grows
    /// with the threshold drop, so turning the control up levels and lifts
    /// rather than throwing level away.
    #[test]
    fn test_more_amount_is_never_quieter() {
        const N: usize = 48_000;
        let mut prev = f32::NEG_INFINITY;
        let mut prev_amt = 0.0;
        for step in 0..=10 {
            let amt = step as f32 / 10.0;
            let mut comp = MasteringCompressor::new(RATE);
            comp.set_amount(amt);
            let mut buf = sine_buf(1000.0, 0.25, N);
            comp.process(&mut buf);
            let rms = (buf[N..].iter().map(|s| s * s).sum::<f32>() / (N * 2) as f32).sqrt();
            let db = 20.0 * rms.max(1e-20).log10();
            assert!(
                db >= prev - 1e-3,
                "amount {amt} ({db:.3} dB) is quieter than amount {prev_amt} ({prev:.3} dB)"
            );
            prev = db;
            prev_amt = amt;
        }
    }
}
