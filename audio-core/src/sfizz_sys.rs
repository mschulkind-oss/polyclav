//! Bare FFI to the small subset of libsfizz we use. Linked via pkg-config
//! in build.rs. See `sfizz.h` (sfztools/sfizz) for the full API.

use std::os::raw::{c_char, c_int};

#[allow(non_camel_case_types)]
pub enum sfizz_synth_t {}

unsafe extern "C" {
    pub fn sfizz_create_synth() -> *mut sfizz_synth_t;
    pub fn sfizz_free(synth: *mut sfizz_synth_t);
    pub fn sfizz_load_file(synth: *mut sfizz_synth_t, path: *const c_char) -> bool;
    pub fn sfizz_set_sample_rate(synth: *mut sfizz_synth_t, sample_rate: f32);
    pub fn sfizz_set_samples_per_block(synth: *mut sfizz_synth_t, samples_per_block: c_int);
    pub fn sfizz_send_note_on(
        synth: *mut sfizz_synth_t,
        delay: c_int,
        note: c_int,
        velocity: c_int,
    );
    pub fn sfizz_send_note_off(
        synth: *mut sfizz_synth_t,
        delay: c_int,
        note: c_int,
        velocity: c_int,
    );
    pub fn sfizz_send_cc(
        synth: *mut sfizz_synth_t,
        delay: c_int,
        cc_number: c_int,
        cc_value: c_int,
    );
    pub fn sfizz_send_pitch_wheel(synth: *mut sfizz_synth_t, delay: c_int, pitch: c_int);
    pub fn sfizz_render_block(
        synth: *mut sfizz_synth_t,
        channels: *mut *mut f32,
        num_channels: c_int,
        num_frames: c_int,
    );
}
