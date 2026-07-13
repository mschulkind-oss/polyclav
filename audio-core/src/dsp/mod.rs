//! DSP chain: compressor + reverb + mastering + limiter. All processors
//! operate on interleaved stereo F32 buffers in place and pre-allocate
//! their state.

pub mod compressor;
pub mod drive_pedal;
pub mod limiter;
pub mod mastering;
pub mod reverb;

pub use compressor::Compressor;
pub use drive_pedal::DrivePedal;
pub use limiter::Limiter;
pub use mastering::MasteringCompressor;
pub use reverb::Reverb;
