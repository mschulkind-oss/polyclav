//! LV2 plugin backend, hosted via the `livi` crate (Rust wrapper around lilv).
//!
//! Phase 1 scope: instantiate a plugin by URI, route MIDI to its first
//! atom-sequence input port, and copy its first two audio outputs to the
//! interleaved stereo buffer the audio thread already operates on. Mono
//! plugins (1 audio out) get duplicated to both stereo channels; plugins
//! with >2 outputs have the rest ignored. Phase 2 will introduce control-
//! port automation and preset/state handling.

use std::sync::{Arc, OnceLock};

use livi::event::LV2AtomSequence;
use livi::{EmptyPortConnections, Features, FeaturesBuilder, World};

use crate::MidiEvent;

/// Atom-sequence event buffer size in bytes. The same constant the livi
/// JACK example uses; comfortably larger than anything one audio callback
/// will produce.
const EVENT_BUFFER_BYTES: usize = 32_768;

/// Process-wide LV2 world. Building this scans every LV2 bundle on the
/// system (~ms-to-seconds; we cache it once). The build runs on a worker
/// thread at startup so the audio thread never blocks on plugin discovery.
static WORLD: OnceLock<Arc<LvWorld>> = OnceLock::new();

/// The cached world plus pre-built feature set sized for our audio quantum.
pub(crate) struct LvWorld {
    pub world: World,
    pub features: Arc<Features>,
}

impl LvWorld {
    fn build(min_block: usize, max_block: usize) -> Self {
        let world = World::new();
        let features = world.build_features(FeaturesBuilder {
            min_block_length: min_block,
            max_block_length: max_block,
        });
        Self { world, features }
    }
}

/// Idempotently initialise the world. Safe to call multiple times — the
/// first caller wins and later callers reuse the cached instance.
pub(crate) fn ensure_world(min_block: usize, max_block: usize) -> &'static Arc<LvWorld> {
    WORLD.get_or_init(|| Arc::new(LvWorld::build(min_block, max_block)))
}

/// A loaded LV2 plugin instance, ready to receive MIDI and render audio
/// into the audio thread's interleaved stereo output buffer.
pub struct LvInstance {
    instance: livi::Instance,
    midi_in: LV2AtomSequence,
    /// Number of audio output ports the plugin exposes. We render up to
    /// min(2, audio_outputs) channels. If the plugin is mono we duplicate.
    audio_outputs: usize,
    /// Whether the plugin accepts MIDI atom input on a port.
    has_atom_in: bool,
    /// Scratch audio output buffers, one Vec per output channel. Resized
    /// to match the current callback's frame count.
    out_buffers: Vec<Vec<f32>>,
    midi_urid: lv2_raw::LV2Urid,
}

impl LvInstance {
    /// Look up `uri` in the cached LV2 world and instantiate it.
    ///
    /// Called on a worker thread; the resulting `LvInstance` is then sent
    /// to the audio thread via the existing reload queue.
    pub fn load(uri: &str, sample_rate: f64, max_block: usize) -> Result<Self, String> {
        let world = ensure_world(1, max_block);
        let plugin = world
            .world
            .plugin_by_uri(uri)
            .ok_or_else(|| format!("LV2 plugin not found: {uri}"))?;

        // SAFETY: livi::Plugin::instantiate is documented as unsafe because
        // it loads native code; we pass a valid feature set and sample rate.
        let instance = unsafe {
            plugin
                .instantiate(world.features.clone(), sample_rate)
                .map_err(|e| format!("LV2 instantiate({uri}): {e:?}"))?
        };

        let counts = *plugin.port_counts();
        let audio_outputs = counts.audio_outputs;
        let has_atom_in = counts.atom_sequence_inputs > 0;

        let midi_in = LV2AtomSequence::new(&world.features, EVENT_BUFFER_BYTES);
        let out_buffers = (0..audio_outputs.max(1))
            .map(|_| Vec::with_capacity(max_block))
            .collect();

        Ok(Self {
            instance,
            midi_in,
            audio_outputs,
            has_atom_in,
            out_buffers,
            midi_urid: world.features.midi_urid(),
        })
    }

    /// Push a MIDI event onto the next-block atom sequence. Called from the
    /// audio thread for every event drained from the lock-free queue.
    pub fn push_midi(&mut self, event: &MidiEvent) {
        if !self.has_atom_in {
            return;
        }
        let bytes = midi_event_bytes(event);
        // Time-in-frames = 0: deliver every event at the start of the block.
        // Phase 1 doesn't sample-accurately schedule MIDI; this matches how
        // oxisynth/sfizz already drain the queue before rendering.
        let _ = self.midi_in.push_midi_event::<3>(0, self.midi_urid, &bytes);
    }

    /// Render a single audio callback. `samples` is interleaved stereo
    /// (L, R, L, R, ...). Length is `2 * n_frames`.
    pub fn render(&mut self, samples: &mut [f32]) {
        let n_frames = samples.len() / 2;
        for buf in &mut self.out_buffers {
            buf.clear();
            buf.resize(n_frames, 0.0);
        }

        // SAFETY: `instance.run` is documented as unsafe; we provide a
        // matching port-connection iterator and a frame count within the
        // feature builder's bounds.
        let result = unsafe {
            let ports = EmptyPortConnections::new()
                .with_atom_sequence_inputs(std::iter::once(&self.midi_in))
                .with_audio_outputs(self.out_buffers.iter_mut().map(|b| b.as_mut_slice()));
            self.instance.run(n_frames, ports)
        };

        // Always clear the input atom sequence after `run` — LV2 events
        // are per-block, not cumulative.
        self.midi_in.clear();

        if let Err(e) = result {
            // Don't kill the audio thread on a single bad block; just zero
            // output and log sparsely. Bad plugins are a real possibility.
            eprintln!("audio-core: lv2 plugin.run failed: {e:?}");
            for s in samples.iter_mut() {
                *s = 0.0;
            }
            return;
        }

        // Copy our scratch outputs to the interleaved stereo buffer.
        match self.audio_outputs {
            0 => {
                // Plugin produces no audio (rare but possible — e.g. event-
                // transforming plugins). Emit silence.
                for s in samples.iter_mut() {
                    *s = 0.0;
                }
            }
            1 => {
                // Mono: duplicate to both channels.
                let mono = &self.out_buffers[0];
                for (i, s) in mono.iter().enumerate() {
                    samples[i * 2] = *s;
                    samples[i * 2 + 1] = *s;
                }
            }
            _ => {
                let l = &self.out_buffers[0];
                let r = &self.out_buffers[1];
                for i in 0..n_frames {
                    samples[i * 2] = l[i];
                    samples[i * 2 + 1] = r[i];
                }
            }
        }
    }
}

/// Convert our internal `MidiEvent` to the 3-byte raw MIDI 1.0 message
/// expected by `LV2AtomSequence::push_midi_event`.
fn midi_event_bytes(event: &MidiEvent) -> [u8; 3] {
    match *event {
        MidiEvent::NoteOn {
            channel,
            note,
            velocity,
        } => [0x90 | (channel & 0x0F), note & 0x7F, velocity & 0x7F],
        MidiEvent::NoteOff { channel, note } => [0x80 | (channel & 0x0F), note & 0x7F, 0],
        MidiEvent::ControlChange {
            channel,
            controller,
            value,
        } => [0xB0 | (channel & 0x0F), controller & 0x7F, value & 0x7F],
        MidiEvent::PitchBend { channel, bend } => {
            // 14-bit unsigned (0..=16383). LSB then MSB, both 7-bit.
            [
                0xE0 | (channel & 0x0F),
                (bend & 0x7F) as u8,
                ((bend >> 7) & 0x7F) as u8,
            ]
        }
    }
}
