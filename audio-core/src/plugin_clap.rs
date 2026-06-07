//! CLAP plugin backend, hosted via the `clack-host` crate.
//!
//! Phase 1 scope: load a CLAP bundle (`.clap` shared library) by file path
//! plus a plugin id (e.g. `com.asb2m10.dexed`), activate it with our
//! sample rate / quantum, start its audio processor, then route MIDI to
//! its main input port and copy its first two audio outputs to the
//! interleaved stereo buffer.
//!
//! ## Threading note
//!
//! `clack_host::plugin::PluginInstance` is `!Send` — it's pinned to the
//! main thread that instantiates it. The `StartedPluginAudioProcessor`
//! it produces, however, IS `Send` and owns an `Arc` into the same
//! underlying instance. Our pattern: the background worker thread loads
//! the entry, instantiates the plugin, activates it, and ships the
//! started processor across to the audio thread. The `PluginInstance`
//! is dropped at the end of the worker thread; its `Drop` impl
//! intentionally leaks the inner `Arc` if any other owner (the audio
//! processor) still exists — so a single processor-ref outlives the
//! worker thread cleanly. The trade-off is a small permanent leak per
//! plugin load; Phase 2 will introduce a proper main-thread keeper for
//! clean teardown.

use std::ffi::CString;
use std::path::Path;
use std::sync::OnceLock;

use clack_host::events::event_types::{MidiEvent as ClapMidiEvent, NoteOffEvent, NoteOnEvent};
use clack_host::events::io::{EventBuffer, InputEvents, OutputEvents};
use clack_host::prelude::{
    AudioPortBuffer, AudioPortBufferType, AudioPorts, HostInfo, OutputAudioBuffers,
    PluginAudioConfiguration, PluginEntry, PluginInstance, StartedPluginAudioProcessor,
};

use crate::MidiEvent;

/// Cached `HostInfo`. CLAP plugins expect a stable host identity; rebuild
/// it once and reuse for every instantiation.
fn host_info() -> &'static HostInfo {
    static INFO: OnceLock<HostInfo> = OnceLock::new();
    INFO.get_or_init(|| {
        HostInfo::new(
            "polyclav",
            "polyclav",
            "https://github.com/",
            env!("CARGO_PKG_VERSION"),
        )
        .expect("static host info strings are valid C strings")
    })
}

/// A loaded, activated CLAP plugin instance ready to render audio.
pub struct ClapInstance {
    processor: StartedPluginAudioProcessor<()>,
    /// Output audio port wrapper, sized to 2 channels / 1 port. We keep a
    /// pre-allocated `AudioPorts` so the per-callback `with_output_buffers`
    /// call doesn't allocate.
    output_ports: AudioPorts,
    /// Per-callback scratch buffers for the two output channels. Resized
    /// once per callback to match the frame count.
    out_l: Vec<f32>,
    out_r: Vec<f32>,
    /// Empty input audio ports — we have no audio input.
    input_ports: AudioPorts,
    /// Reusable event input buffer; cleared and refilled each callback.
    input_events: EventBuffer,
    /// Sink for events the plugin produces (note offs after sustain
    /// release, MPE expressions, etc). We drop them on the floor in
    /// Phase 1.
    output_events: EventBuffer,
}

// SAFETY: `StartedPluginAudioProcessor` is already `Send`. `AudioPorts`
// and `EventBuffer` are public clack types that are `Send`. `Vec<f32>` is
// `Send`. The struct as a whole is therefore `Send` by composition; this
// is just an explicit assertion that nothing in here pins us to the
// instantiation thread.
unsafe impl Send for ClapInstance {}

impl ClapInstance {
    /// Load a CLAP bundle and instantiate the plugin with the given id.
    /// Runs on a background worker thread; never on the audio thread.
    pub fn load(
        bundle_path: &Path,
        plugin_id: &str,
        sample_rate: f64,
        max_block: usize,
    ) -> Result<Self, String> {
        // 1. Load the .clap dynamic library and its entry descriptor.
        // SAFETY: `PluginEntry::load` is unsafe because dlopen can execute
        // arbitrary code; we accept that risk as the host of plugins.
        let entry = unsafe { PluginEntry::load(bundle_path) }
            .map_err(|e| format!("CLAP load {}: {e:?}", bundle_path.display()))?;

        // 2. Pull the plugin factory and verify the requested id exists.
        let factory = entry
            .get_plugin_factory()
            .ok_or_else(|| "CLAP entry has no plugin factory".to_string())?;

        let plugin_id_c =
            CString::new(plugin_id).map_err(|e| format!("CLAP plugin id has interior NUL: {e}"))?;

        // Validate the id is present — gives a clearer error than the
        // `PluginInstance::new` failure which doesn't say *why* the id
        // was not found.
        let known = factory
            .plugin_descriptors()
            .filter_map(|d| d.id())
            .any(|id| id.to_bytes() == plugin_id_c.as_bytes());
        if !known {
            let available: Vec<String> = factory
                .plugin_descriptors()
                .filter_map(|d| d.id())
                .map(|id| String::from_utf8_lossy(id.to_bytes()).into_owned())
                .collect();
            return Err(format!(
                "CLAP plugin id {plugin_id:?} not found in {}; available ids: {available:?}",
                bundle_path.display()
            ));
        }

        // 3. Instantiate. The unit-type `()` works as our `HostHandlers`
        // because clack provides a default no-op impl for it.
        let mut instance =
            PluginInstance::<()>::new(|_| (), |_| (), &entry, plugin_id_c.as_c_str(), host_info())
                .map_err(|e| format!("CLAP instantiate {plugin_id}: {e:?}"))?;

        // 4. Activate with our audio configuration.
        let audio_cfg = PluginAudioConfiguration {
            sample_rate,
            min_frames_count: 1,
            max_frames_count: max_block as u32,
        };
        let stopped = instance
            .activate(|_, _| (), audio_cfg)
            .map_err(|e| format!("CLAP activate {plugin_id}: {e:?}"))?;

        // 5. Start processing — must succeed before we ship the
        // processor to the audio thread.
        let processor = stopped
            .start_processing()
            .map_err(|e| format!("CLAP start_processing {plugin_id}: {e:?}"))?;

        // 6. Drop the PluginInstance shell. Per its Drop impl, since the
        // started processor still owns an Arc to the same inner, the
        // shell intentionally leaks one ref count rather than risk an
        // unsynchronised free. PluginEntry similarly Arc-clones into the
        // instance; letting it drop here is fine.
        drop(instance);
        drop(entry);

        Ok(Self {
            processor,
            output_ports: AudioPorts::with_capacity(2, 1),
            out_l: Vec::with_capacity(max_block),
            out_r: Vec::with_capacity(max_block),
            input_ports: AudioPorts::with_capacity(0, 0),
            input_events: EventBuffer::with_capacity(64),
            output_events: EventBuffer::with_capacity(64),
        })
    }

    /// Push a MIDI event onto the next-block input buffer.
    pub fn push_midi(&mut self, event: &MidiEvent) {
        match *event {
            MidiEvent::NoteOn {
                channel,
                note,
                velocity,
            } => {
                // Send both a raw MIDI event and a typed CLAP NoteOn —
                // some plugins (Dexed) react to typed note events
                // exclusively, others only to raw MIDI. Sending both is
                // belt-and-suspenders.
                self.input_events.push(&ClapMidiEvent::new(
                    0,
                    0,
                    [0x90 | (channel & 0x0F), note & 0x7F, velocity & 0x7F],
                ));
                let pckn = clack_host::events::Pckn::new(
                    0u16,           // port
                    channel as u16, // channel
                    note as u16,    // key
                    0u32,           // note id (0 = any)
                );
                self.input_events
                    .push(&NoteOnEvent::new(0, pckn, f64::from(velocity) / 127.0));
            }
            MidiEvent::NoteOff { channel, note } => {
                self.input_events.push(&ClapMidiEvent::new(
                    0,
                    0,
                    [0x80 | (channel & 0x0F), note & 0x7F, 0],
                ));
                let pckn = clack_host::events::Pckn::new(0u16, channel as u16, note as u16, 0u32);
                self.input_events.push(&NoteOffEvent::new(0, pckn, 0.0));
            }
            MidiEvent::ControlChange {
                channel,
                controller,
                value,
            } => {
                self.input_events.push(&ClapMidiEvent::new(
                    0,
                    0,
                    [0xB0 | (channel & 0x0F), controller & 0x7F, value & 0x7F],
                ));
            }
            MidiEvent::PitchBend { channel, bend } => {
                self.input_events.push(&ClapMidiEvent::new(
                    0,
                    0,
                    [
                        0xE0 | (channel & 0x0F),
                        (bend & 0x7F) as u8,
                        ((bend >> 7) & 0x7F) as u8,
                    ],
                ));
            }
        }
    }

    /// Render a single audio callback into the interleaved stereo `samples`
    /// buffer. Called on the audio thread.
    pub fn render(&mut self, samples: &mut [f32]) {
        let n_frames = samples.len() / 2;
        self.out_l.clear();
        self.out_l.resize(n_frames, 0.0);
        self.out_r.clear();
        self.out_r.resize(n_frames, 0.0);

        // Build a single AudioPortBuffer with two channels per port.
        let mut output_audio: OutputAudioBuffers<'_> =
            self.output_ports.with_output_buffers([AudioPortBuffer {
                latency: 0,
                channels: AudioPortBufferType::f32_output_only(
                    [self.out_l.as_mut_slice(), self.out_r.as_mut_slice()].into_iter(),
                ),
            }]);

        let input_audio = self
            .input_ports
            .with_input_buffers::<_, _, [_; 0], [_; 0]>([]);

        let in_events = InputEvents::from_buffer(&self.input_events);
        self.output_events.clear();
        let mut out_events = OutputEvents::from_buffer(&mut self.output_events);

        let result = self.processor.process(
            &input_audio,
            &mut output_audio,
            &in_events,
            &mut out_events,
            None,
            None,
        );

        // Clear MIDI input for the next block regardless of outcome.
        self.input_events.clear();

        if let Err(e) = result {
            eprintln!("audio-core: clap process failed: {e:?}");
            for s in samples.iter_mut() {
                *s = 0.0;
            }
            return;
        }

        // Interleave the scratch channels into the output buffer.
        for i in 0..n_frames {
            samples[i * 2] = self.out_l[i];
            samples[i * 2 + 1] = self.out_r[i];
        }
    }
}
