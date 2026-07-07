//! CoreAudio output backend (macOS) — the cpal analog of the PipeWire
//! `run_audio`/`process_audio` path in `lib.rs`.
//!
//! Contract identical to PipeWire: interleaved stereo `f32`, 48_000 Hz, one
//! DSP block per callback. cpal drives its own realtime thread, so this file
//! does NOT spawn a callback thread — it only owns the "park" thread (the
//! existing `polyclav-audio` thread from `polyclav_audio_start`) that keeps the
//! `Stream` alive until stop().
//!
//! macOS v1 backends: Native + Oxi + Sfizz. LV2/CLAP are `cfg(target_os =
//! "linux")` — not for `Send` reasons (livi::Instance and clack's audio
//! processor are both `Send`, and cpal's `Send + 'static` callback bound is
//! identical on every backend) but because livi wraps the lilv C library, which
//! has no macOS build, and CLAP hosting is out of the v1 macOS scope.
#![cfg(target_os = "macos")]

use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::mpsc::SyncSender;
use std::sync::Arc;
use std::time::Duration;

use cpal::traits::{DeviceTrait, HostTrait, StreamTrait};
use cpal::{BufferSize, OutputCallbackInfo, SampleFormat, StreamConfig, SupportedBufferSize};

// Portable seams + the shared latency knob, all defined in lib.rs.
//   build_user_data      : soundfont/native load + DSP snapshot + UserData ctor
//   swap_pending_backend : drain reload_queue, generation-checked
//   drain_midi           : drain midi_queue into the active synth
//   render_block         : native-param push + synth dispatch + full DSP chain
//                          (computes n_frames = samples.len()/2 internally)
//   LATENCY_FRAMES       : the shared, already-clamped ([16,8192], 0->128)
//                          latency request from polyclav_audio_set_latency_frames
use crate::{build_user_data, drain_midi, render_block, swap_pending_backend, LATENCY_FRAMES};

/// Hard target format. 48 kHz is a compile-time const across audio-core — there
/// is no runtime sample-rate conversion — so the CoreAudio device must expose a
/// 48 kHz stereo f32 config. cpal 0.18's `SampleRate` is a plain `u32` alias, so
/// we compare/pass bare integers.
const TARGET_SAMPLE_RATE: u32 = 48_000;
const CHANNELS: u16 = 2;

/// Clamp a requested frame count into the device's supported range. CoreAudio
/// (via cpal) returns an `UnsupportedConfig`-class error for an out-of-range
/// `BufferSize::Fixed(n)` rather than clamping for us, so we clamp *before*
/// requesting — this is exactly "dictate our own buffer size, at or above the
/// device minimum".
#[inline]
fn clamp_frames(requested: u32, dev_min: u32, dev_max: u32) -> u32 {
    requested.max(dev_min).min(dev_max)
}

/// Choose a 2-channel f32 config at exactly 48 kHz. Errors if the device
/// advertises no such config (e.g. a device whose f32 range excludes 48 kHz).
fn select_output_config(device: &cpal::Device) -> Result<cpal::SupportedStreamConfig, String> {
    let ranges = device
        .supported_output_configs()
        .map_err(|e| format!("supported_output_configs: {e}"))?;

    for range in ranges {
        // `matches!` avoids relying on SampleFormat: PartialEq.
        let is_f32 = matches!(range.sample_format(), SampleFormat::F32);
        if range.channels() == CHANNELS
            && is_f32
            && range.min_sample_rate() <= TARGET_SAMPLE_RATE
            && range.max_sample_rate() >= TARGET_SAMPLE_RATE
        {
            // try_with_sample_rate returns None if 48 kHz is out of range; never
            // with_sample_rate (that one panics out of range).
            if let Some(cfg) = range.try_with_sample_rate(TARGET_SAMPLE_RATE) {
                return Ok(cfg);
            }
        }
    }
    Err(format!(
        "no stereo f32 output config at {TARGET_SAMPLE_RATE} Hz (device default rate may be 44100; \
         audio-core has no runtime sample-rate conversion)"
    ))
}

/// CoreAudio analog of the PipeWire `run_audio`. Called on the existing
/// `polyclav-audio` thread by `polyclav_audio_start`. Builds + plays the stream,
/// signals ready, then parks on THIS thread until `quit_flag` flips — keeping
/// the `Stream` local so its `Drop` stops audio and releases the callback (which
/// owns `UserData`). Streams start paused, so we must `play()`.
pub(crate) fn run_audio(
    quit_flag: Arc<AtomicBool>,
    ready_tx: &SyncSender<Result<(), String>>,
) -> Result<(), String> {
    // 1. Default host + default OUTPUT device.
    let host = cpal::default_host();
    let device = host
        .default_output_device()
        .ok_or_else(|| "no default output device".to_string())?;

    // 2. Stereo f32 @ 48 kHz (forces 48 kHz even if the device default is 44.1).
    let supported = select_output_config(&device)?;

    // 3. Device buffer-frame range → clamp the caller's request into it.
    let (dev_min, dev_max) = match supported.buffer_size() {
        SupportedBufferSize::Range { min, max } => (*min, *max),
        SupportedBufferSize::Unknown => (MIN_FALLBACK, MAX_FALLBACK),
    };
    // LATENCY_FRAMES is already clamped to [16,8192] (0->128) by the shared
    // polyclav_audio_set_latency_frames setter; here we only fit it to the device.
    let requested = LATENCY_FRAMES.load(Ordering::Relaxed);
    let frames = clamp_frames(requested, dev_min, dev_max);
    if frames != requested {
        eprintln!(
            "audio-core: coreaudio latency request {requested} clamped to {frames} \
             (device range {dev_min}..={dev_max})"
        );
    }

    // 4. Build our own StreamConfig with a FIXED buffer size. (Using
    //    supported.config() would force BufferSize::Default, discarding the knob.)
    let config = StreamConfig {
        channels: supported.channels(),       // == CHANNELS (2)
        sample_rate: supported.sample_rate(), // == 48_000
        buffer_size: BufferSize::Fixed(frames),
    };

    // 5. UserData is moved into the RT callback → it must be Send + 'static. On
    //    macOS SynthBackend ∈ {Oxi, Sfizz, Native}, all Send. The closure does no
    //    heap alloc / lock in the RT path.
    let mut user_data = build_user_data();
    let mut logged: u32 = 0;
    let mut last_frames: usize = 0;

    let err_quit = Arc::clone(&quit_flag);
    let stream = device
        .build_output_stream::<f32, _, _>(
            config, // by value — StreamConfig is Copy in cpal 0.18.
            // Data callback: interleaved stereo f32, samples.len() == frames*2.
            // Same three steps as process_audio: swap pending backend, drain
            // MIDI, render one block.
            move |samples: &mut [f32], _info: &OutputCallbackInfo| {
                swap_pending_backend(&mut user_data);
                drain_midi(&mut user_data);
                render_block(&mut user_data, samples);

                let n = samples.len() / CHANNELS as usize;
                if logged < 10 || n != last_frames {
                    eprintln!("audio-core: coreaudio callback frames={n}");
                    logged = logged.saturating_add(1);
                    last_frames = n;
                }
            },
            // Error callback: treat a stream error as fatal — flip quit so the
            // park loop below exits and start() can be retried.
            move |err| {
                eprintln!("audio-core: coreaudio stream error: {err}");
                err_quit.store(true, Ordering::SeqCst);
            },
            None, // timeout: Option<Duration>
        )
        .map_err(|e| format!("build_output_stream: {e}"))?;

    // 6. Streams start paused — must play().
    stream.play().map_err(|e| format!("stream play: {e}"))?;
    eprintln!(
        "audio-core: coreaudio stream playing (buffer={frames} frames, ~{:.2} ms)",
        frames as f32 / TARGET_SAMPLE_RATE as f32 * 1000.0
    );

    // 7. Signal ready, then own the Stream on this thread until stop(). Mirrors
    //    PipeWire's `main_loop.run()` blocking until quit.
    let _ = ready_tx.send(Ok(()));
    while !quit_flag.load(Ordering::Relaxed) {
        std::thread::park_timeout(Duration::from_millis(100));
    }

    // Dropping the Stream stops CoreAudio and frees the callback + UserData.
    drop(stream);
    eprintln!("audio-core: coreaudio stream stopped");
    Ok(())
}

/// Fallback buffer bounds if the device reports `SupportedBufferSize::Unknown`
/// (not expected on CoreAudio, which always advertises a range).
const MIN_FALLBACK: u32 = 16;
const MAX_FALLBACK: u32 = 4096;
