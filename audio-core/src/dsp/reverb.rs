const COMB_TUNINGS_L: [usize; 8] = [1116, 1188, 1277, 1356, 1422, 1491, 1557, 1617];
const ALLPASS_TUNINGS_L: [usize; 4] = [556, 441, 341, 225];
const STEREO_SPREAD: usize = 23;

const DEFAULT_ROOMSIZE: f32 = 0.5;
const DEFAULT_DAMP: f32 = 0.5;
const DEFAULT_WIDTH: f32 = 1.0;
const FIXED_GAIN: f32 = 0.015;
const SCALE_ROOM: f32 = 0.28;
const OFFSET_ROOM: f32 = 0.7;
const SCALE_DAMP: f32 = 0.4;

struct LbcfComb {
    buffer: Box<[f32]>,
    index: usize,
    filter_store: f32,
    feedback: f32,
    damp1: f32,
    damp2: f32,
}

impl LbcfComb {
    fn new(size: usize) -> Self {
        Self {
            buffer: vec![0.0_f32; size].into_boxed_slice(),
            index: 0,
            filter_store: 0.0,
            feedback: 0.0,
            damp1: 0.0,
            damp2: 0.0,
        }
    }

    fn set_damp(&mut self, damp: f32) {
        self.damp1 = damp;
        self.damp2 = 1.0 - damp;
    }

    fn set_feedback(&mut self, feedback: f32) {
        self.feedback = feedback;
    }

    fn process(&mut self, input: f32) -> f32 {
        let output = self.buffer[self.index];
        self.filter_store = output * self.damp2 + self.filter_store * self.damp1;
        self.buffer[self.index] = input + self.filter_store * self.feedback;
        self.index = (self.index + 1) % self.buffer.len();
        output
    }
}

struct Allpass {
    buffer: Box<[f32]>,
    index: usize,
    feedback: f32,
}

impl Allpass {
    fn new(size: usize) -> Self {
        Self {
            buffer: vec![0.0_f32; size].into_boxed_slice(),
            index: 0,
            feedback: 0.5,
        }
    }

    fn process(&mut self, input: f32) -> f32 {
        let bufout = self.buffer[self.index];
        let output = -input + bufout;
        self.buffer[self.index] = input + bufout * self.feedback;
        self.index = (self.index + 1) % self.buffer.len();
        output
    }
}

pub struct Reverb {
    combs_l: [LbcfComb; 8],
    combs_r: [LbcfComb; 8],
    allpass_l: [Allpass; 4],
    allpass_r: [Allpass; 4],
    roomsize: f32,
    damp: f32,
    width: f32,
    mix: f32,
    wet1: f32,
    wet2: f32,
    dry: f32,
}

impl Reverb {
    pub fn new() -> Self {
        let combs_l = std::array::from_fn(|i| LbcfComb::new(COMB_TUNINGS_L[i]));
        let combs_r = std::array::from_fn(|i| LbcfComb::new(COMB_TUNINGS_L[i] + STEREO_SPREAD));
        let allpass_l = std::array::from_fn(|i| Allpass::new(ALLPASS_TUNINGS_L[i]));
        let allpass_r = std::array::from_fn(|i| Allpass::new(ALLPASS_TUNINGS_L[i] + STEREO_SPREAD));

        let mut reverb = Self {
            combs_l,
            combs_r,
            allpass_l,
            allpass_r,
            roomsize: DEFAULT_ROOMSIZE,
            damp: DEFAULT_DAMP,
            width: DEFAULT_WIDTH,
            mix: 0.0,
            wet1: 0.0,
            wet2: 0.0,
            dry: 1.0,
        };

        reverb.set_roomsize(DEFAULT_ROOMSIZE);
        reverb.set_damp(DEFAULT_DAMP);
        reverb.set_mix(0.0);
        reverb
    }

    fn update(&mut self) {
        self.wet1 = self.mix * (self.width / 2.0 + 0.5);
        self.wet2 = self.mix * ((1.0 - self.width) / 2.0);
        self.dry = 1.0 - self.mix * 0.5;
    }

    pub fn set_mix(&mut self, mix: f32) {
        self.mix = mix.clamp(0.0, 1.0);
        self.update();
    }

    #[allow(dead_code)]
    pub fn mix(&self) -> f32 {
        self.mix
    }

    pub fn set_roomsize(&mut self, roomsize: f32) {
        self.roomsize = roomsize.clamp(0.0, 1.0);
        let feedback = self.roomsize * SCALE_ROOM + OFFSET_ROOM;
        for comb in &mut self.combs_l {
            comb.set_feedback(feedback);
        }
        for comb in &mut self.combs_r {
            comb.set_feedback(feedback);
        }
    }

    pub fn set_damp(&mut self, damp: f32) {
        self.damp = damp.clamp(0.0, 1.0);
        let damp_val = self.damp * SCALE_DAMP;
        for comb in &mut self.combs_l {
            comb.set_damp(damp_val);
        }
        for comb in &mut self.combs_r {
            comb.set_damp(damp_val);
        }
    }

    #[allow(dead_code)]
    pub fn set_width(&mut self, width: f32) {
        self.width = width.clamp(0.0, 1.0);
        self.update();
    }

    pub fn process(&mut self, samples: &mut [f32]) {
        for chunk in samples.chunks_exact_mut(2) {
            let in_l = chunk[0];
            let in_r = chunk[1];

            let input = (in_l + in_r) * FIXED_GAIN;

            let mut out_l = 0.0;
            let mut out_r = 0.0;

            for comb in &mut self.combs_l {
                out_l += comb.process(input);
            }
            for comb in &mut self.combs_r {
                out_r += comb.process(input);
            }

            for allpass in &mut self.allpass_l {
                out_l = allpass.process(out_l);
            }
            for allpass in &mut self.allpass_r {
                out_r = allpass.process(out_r);
            }

            chunk[0] = out_l * self.wet1 + out_r * self.wet2 + in_l * self.dry;
            chunk[1] = out_r * self.wet1 + out_l * self.wet2 + in_r * self.dry;
        }
    }
}

impl Default for Reverb {
    fn default() -> Self {
        Self::new()
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn generate_sine(freq: f32, sample_rate: f32, num_samples: usize) -> Vec<f32> {
        (0..num_samples)
            .map(|i| (i as f32 * 2.0 * std::f32::consts::PI * freq / sample_rate).sin())
            .collect()
    }

    #[test]
    fn test_new_no_panic() {
        let _ = Reverb::new();
    }

    #[test]
    fn test_silence_in_silence_out() {
        let mut reverb = Reverb::new();
        reverb.set_mix(1.0);
        let mut samples = vec![0.0; 4096];
        reverb.process(&mut samples);
        for &s in &samples {
            assert!(s.abs() < 1e-6, "Silence output should remain near zero");
        }
    }

    #[test]
    fn test_no_nan_or_inf_on_loud_signal() {
        let mut reverb = Reverb::new();
        reverb.set_mix(1.0);
        let mut samples = generate_sine(440.0, 48000.0, 8192);
        for s in samples.iter_mut() {
            *s *= 0.5;
        }
        reverb.process(&mut samples);
        for &s in &samples {
            assert!(s.is_finite(), "Output must be finite");
        }
    }

    #[test]
    fn test_mix_zero_is_dry() {
        let mut reverb = Reverb::new();
        reverb.set_mix(0.0);
        let input = generate_sine(440.0, 48000.0, 480);
        let mut output = input.clone();
        reverb.process(&mut output);
        for (i, (&inp, &out)) in input.iter().zip(output.iter()).enumerate() {
            assert!(
                (inp - out).abs() < 1e-5,
                "Sample {} differs: input={}, output={}",
                i,
                inp,
                out
            );
        }
    }

    #[test]
    fn test_mix_changes_signal() {
        let mut reverb = Reverb::new();
        reverb.set_mix(0.5);
        let input = generate_sine(440.0, 48000.0, 480);
        let mut output = input.clone();
        reverb.process(&mut output);
        let diff_sq: f32 = input
            .iter()
            .zip(output.iter())
            .map(|(a, b)| (a - b).powi(2))
            .sum();
        assert!(diff_sq > 0.0, "Reverb should modify the signal");
    }

    #[test]
    fn test_set_mix_clamps() {
        let mut reverb = Reverb::new();
        reverb.set_mix(-1.0);
        assert_eq!(reverb.mix(), 0.0);
        reverb.set_mix(2.0);
        assert_eq!(reverb.mix(), 1.0);
    }
}
