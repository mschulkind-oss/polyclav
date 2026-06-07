//! Safe Rust wrapper for libsfizz (SFZ sample-set player).

use std::ffi::CString;
use std::os::raw::c_int;
use std::path::Path;

use crate::sfizz_sys::*;

pub struct Sfizz {
    ptr: *mut sfizz_synth_t,
    left: Vec<f32>,
    right: Vec<f32>,
}

// libsfizz handles its own internal locking; the C API is documented as
// thread-safe for the realtime methods we call. Wrapping the opaque pointer
// in Send lets us hand it across the spawn-thread boundary.
unsafe impl Send for Sfizz {}

impl Sfizz {
    pub fn load(path: &Path, sample_rate: f32, max_block: usize) -> Result<Self, String> {
        let cpath = CString::new(path.to_string_lossy().as_ref())
            .map_err(|e| format!("path → CString: {e}"))?;
        unsafe {
            let ptr = sfizz_create_synth();
            if ptr.is_null() {
                return Err("sfizz_create_synth returned null".into());
            }
            sfizz_set_sample_rate(ptr, sample_rate);
            sfizz_set_samples_per_block(ptr, max_block as c_int);
            if !sfizz_load_file(ptr, cpath.as_ptr()) {
                sfizz_free(ptr);
                return Err(format!("sfizz_load_file failed for {path:?}"));
            }
            Ok(Self {
                ptr,
                left: Vec::with_capacity(max_block),
                right: Vec::with_capacity(max_block),
            })
        }
    }

    pub fn note_on(&mut self, note: u8, velocity: u8) {
        unsafe { sfizz_send_note_on(self.ptr, 0, note as c_int, velocity as c_int) }
    }

    pub fn note_off(&mut self, note: u8) {
        unsafe { sfizz_send_note_off(self.ptr, 0, note as c_int, 0) }
    }

    pub fn cc(&mut self, cc: u8, value: u8) {
        unsafe { sfizz_send_cc(self.ptr, 0, cc as c_int, value as c_int) }
    }

    /// Pitch bend: 14-bit unsigned MIDI value (0..16383, 8192 = centre).
    /// Converted to signed (-8192..8191) for sfizz's API.
    pub fn pitch_bend(&mut self, bend: u16) {
        let signed = bend as i32 - 8192;
        unsafe { sfizz_send_pitch_wheel(self.ptr, 0, signed as c_int) }
    }

    /// Render into an interleaved stereo F32 slice (length must be 2 * n_frames).
    /// sfizz writes to two separate channel buffers, so we render to scratch
    /// L/R Vecs and interleave.
    pub fn render(&mut self, samples: &mut [f32]) {
        let n = samples.len() / 2;
        self.left.resize(n, 0.0);
        self.right.resize(n, 0.0);
        let mut ptrs: [*mut f32; 2] = [self.left.as_mut_ptr(), self.right.as_mut_ptr()];
        unsafe {
            sfizz_render_block(self.ptr, ptrs.as_mut_ptr(), 2, n as c_int);
        }
        for i in 0..n {
            samples[i * 2] = self.left[i];
            samples[i * 2 + 1] = self.right[i];
        }
    }
}

impl Drop for Sfizz {
    fn drop(&mut self) {
        unsafe { sfizz_free(self.ptr) }
    }
}
