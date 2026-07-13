//! DSP chain: drive pedal + chorus + tremolo + analog delay (the
//! pedalboard) feeding into compressor + reverb + mastering + limiter
//! (the mix bus). All processors operate on interleaved stereo F32
//! buffers in place and pre-allocate their state.

pub mod analog_delay;
pub mod chorus;
pub mod compressor;
pub mod drive_pedal;
pub mod limiter;
pub mod loudness;
pub mod mastering;
pub mod reverb;
pub mod saturate;
pub mod tremolo;

pub use analog_delay::AnalogDelay;
pub use chorus::Chorus;
pub use compressor::Compressor;
pub use drive_pedal::DrivePedal;
pub use limiter::Limiter;
pub use mastering::MasteringCompressor;
pub use reverb::Reverb;
pub use tremolo::Tremolo;
