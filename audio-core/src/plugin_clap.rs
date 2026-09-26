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

use clap_sys::ext::params::{clap_param_info, clap_plugin_params, CLAP_EXT_PARAMS};
use clap_sys::ext::state::{clap_plugin_state, CLAP_EXT_STATE};
use clap_sys::stream::{clap_istream, clap_ostream};
use std::ffi::{CStr, CString};
use std::os::raw::{c_char, c_void};
use std::path::Path;
use std::sync::{mpsc, OnceLock};

use clack_host::events::event_types::{
    MidiEvent as ClapMidiEvent, NoteOffEvent, NoteOnEvent, ParamValueEvent,
};
use clack_host::events::io::{EventBuffer, InputEvents, OutputEvents};
use clack_host::events::{Match, Pckn};
use clack_host::prelude::{
    AudioPortBuffer, AudioPortBufferType, AudioPorts, HostInfo, OutputAudioBuffers,
    PluginAudioConfiguration, PluginEntry, PluginInstance, StartedPluginAudioProcessor,
};
use clack_host::utils::{ClapId, Cookie};

use crate::{
    clap_feedback_queue, clap_param_queue, record_clap_input_event_drop,
    record_plugin_render_error, MidiEvent, PolyclavClapFeedbackEvent, PolyclavClapParamInfo,
};
use clap_sys::ext::note_ports::{
    clap_note_port_info, clap_plugin_note_ports, CLAP_EXT_NOTE_PORTS, CLAP_NOTE_DIALECT_CLAP,
    CLAP_NOTE_DIALECT_MIDI,
};

#[derive(Clone, Copy, Debug, Eq, PartialEq)]
enum ClapNoteDialect {
    Clap,
    Midi,
}

#[derive(Clone, Copy, Debug, Eq, PartialEq)]
struct ClapNoteInput {
    dialect: ClapNoteDialect,
    port_index: u16,
    port_id: u32,
}

#[derive(Clone, Debug, PartialEq)]
#[cfg_attr(test, derive(Eq))]
enum EncodedClapEvent {
    RawMidi {
        port_index: u16,
        bytes: [u8; 3],
    },
    TypedNoteOn {
        port_index: u16,
        channel: u8,
        note: u8,
        velocity: u8,
        note_id: i32,
    },
    TypedNoteOff {
        port_index: u16,
        channel: u8,
        note: u8,
        note_id: i32,
    },
}

fn typed_pckn(port_index: u16, channel: u8, note: u8) -> Pckn {
    Pckn::new(port_index, channel as u16, note as u16, Match::All)
}

fn encode_midi_event(event: &MidiEvent, note_input: ClapNoteInput) -> Vec<EncodedClapEvent> {
    let dialect = note_input.dialect;
    match *event {
        MidiEvent::NoteOn {
            channel,
            note,
            velocity: 0,
        } => encode_midi_event(&MidiEvent::NoteOff { channel, note }, note_input),
        MidiEvent::NoteOn {
            channel,
            note,
            velocity,
        } => match dialect {
            ClapNoteDialect::Clap => vec![EncodedClapEvent::TypedNoteOn {
                port_index: note_input.port_index,
                channel,
                note,
                velocity,
                note_id: typed_pckn(note_input.port_index, channel, note).raw_note_id(),
            }],
            ClapNoteDialect::Midi => vec![EncodedClapEvent::RawMidi {
                port_index: note_input.port_index,
                bytes: [0x90 | (channel & 0x0F), note & 0x7F, velocity & 0x7F],
            }],
        },
        MidiEvent::NoteOff { channel, note } => match dialect {
            ClapNoteDialect::Clap => vec![EncodedClapEvent::TypedNoteOff {
                port_index: note_input.port_index,
                channel,
                note,
                note_id: typed_pckn(note_input.port_index, channel, note).raw_note_id(),
            }],
            ClapNoteDialect::Midi => vec![EncodedClapEvent::RawMidi {
                port_index: note_input.port_index,
                bytes: [0x80 | (channel & 0x0F), note & 0x7F, 0],
            }],
        },
        MidiEvent::ControlChange {
            channel,
            controller,
            value,
        } => vec![EncodedClapEvent::RawMidi {
            port_index: note_input.port_index,
            bytes: [0xB0 | (channel & 0x0F), controller & 0x7F, value & 0x7F],
        }],
        MidiEvent::PitchBend { channel, bend } => vec![EncodedClapEvent::RawMidi {
            port_index: note_input.port_index,
            bytes: [
                0xE0 | (channel & 0x0F),
                (bend & 0x7F) as u8,
                ((bend >> 7) & 0x7F) as u8,
            ],
        }],
    }
}

fn choose_note_dialect(supported: u32) -> Option<ClapNoteDialect> {
    if supported & CLAP_NOTE_DIALECT_CLAP != 0 {
        Some(ClapNoteDialect::Clap)
    } else if supported & CLAP_NOTE_DIALECT_MIDI != 0 {
        Some(ClapNoteDialect::Midi)
    } else {
        None
    }
}

fn query_note_input(instance: &PluginInstance<()>) -> Result<ClapNoteInput, String> {
    let handle = instance.plugin_shared_handle();
    let raw = handle.as_raw();
    let Some(get_extension) = raw.get_extension else {
        return Err("CLAP plugin does not expose get_extension".to_string());
    };

    // SAFETY: CLAP extension pointers are owned by the plugin and remain valid
    // for the plugin instance lifetime. We only call note-ports query functions
    // during load, before audio processing starts.
    let ext = unsafe { get_extension(handle.as_raw_ptr(), CLAP_EXT_NOTE_PORTS.as_ptr()) };
    if ext.is_null() {
        return Err("CLAP plugin has no note-ports extension".to_string());
    }
    let note_ports = unsafe { &*(ext as *const clap_plugin_note_ports) };
    let count = note_ports
        .count
        .ok_or_else(|| "CLAP note-ports extension missing count".to_string())?;
    let get = note_ports
        .get
        .ok_or_else(|| "CLAP note-ports extension missing get".to_string())?;
    let n = unsafe { count(handle.as_raw_ptr(), true) };

    let mut saw_midi: Option<ClapNoteInput> = None;
    for index in 0..n {
        let mut info = clap_note_port_info {
            id: 0,
            supported_dialects: 0,
            preferred_dialect: 0,
            name: [0; clap_sys::string_sizes::CLAP_NAME_SIZE],
        };
        if unsafe { get(handle.as_raw_ptr(), index, true, &mut info) } {
            match choose_note_dialect(info.supported_dialects) {
                Some(ClapNoteDialect::Clap) => {
                    return Ok(ClapNoteInput {
                        dialect: ClapNoteDialect::Clap,
                        port_index: index as u16,
                        port_id: info.id,
                    });
                }
                Some(ClapNoteDialect::Midi) if saw_midi.is_none() => {
                    saw_midi = Some(ClapNoteInput {
                        dialect: ClapNoteDialect::Midi,
                        port_index: index as u16,
                        port_id: info.id,
                    });
                }
                Some(ClapNoteDialect::Midi) | None => {}
            }
        }
    }
    saw_midi.ok_or_else(|| {
        "CLAP plugin has no input note port supporting CLAP notes or raw MIDI".to_string()
    })
}

fn c_char_array_to_string(buf: &[c_char]) -> String {
    let nul = buf.iter().position(|&c| c == 0).unwrap_or(buf.len());
    let bytes: Vec<u8> = buf[..nul].iter().map(|&c| c as u8).collect();
    String::from_utf8_lossy(&bytes).into_owned()
}

fn plugin_extension<T>(instance: &PluginInstance<()>, ext_id: &CStr) -> Option<&'static T> {
    let handle = instance.plugin_shared_handle();
    let raw = handle.as_raw();
    let get_extension = raw.get_extension?;
    let ptr = unsafe { get_extension(handle.as_raw_ptr(), ext_id.as_ptr()) };
    if ptr.is_null() {
        None
    } else {
        Some(unsafe { &*(ptr as *const T) })
    }
}

fn query_params(instance: &PluginInstance<()>) -> Vec<PolyclavClapParamInfo> {
    let Some(params) = plugin_extension::<clap_plugin_params>(instance, CLAP_EXT_PARAMS) else {
        return Vec::new();
    };
    let (Some(count), Some(get_info)) = (params.count, params.get_info) else {
        return Vec::new();
    };
    let handle = instance.plugin_shared_handle();
    let n = unsafe { count(handle.as_raw_ptr()) };
    let mut out = Vec::with_capacity(n as usize);
    for idx in 0..n {
        let mut info = clap_param_info {
            id: 0,
            flags: 0,
            cookie: std::ptr::null_mut(),
            name: [0; clap_sys::string_sizes::CLAP_NAME_SIZE],
            module: [0; clap_sys::string_sizes::CLAP_PATH_SIZE],
            min_value: 0.0,
            max_value: 0.0,
            default_value: 0.0,
        };
        if unsafe { get_info(handle.as_raw_ptr(), idx, &mut info) } {
            let mut current = info.default_value;
            if let Some(get_value) = params.get_value {
                let _ = unsafe { get_value(handle.as_raw_ptr(), info.id, &mut current) };
            }
            out.push(PolyclavClapParamInfo {
                clap_id: info.id,
                flags: info.flags,
                min_value: info.min_value,
                max_value: info.max_value,
                default_value: info.default_value,
                current_value: current,
                name: c_char_array_to_string(&info.name),
                module: c_char_array_to_string(&info.module),
            });
        }
    }
    out
}

struct ReadStream<'a> {
    data: &'a [u8],
    offset: usize,
}

unsafe extern "C" fn read_stream(
    stream: *const clap_istream,
    buffer: *mut c_void,
    size: u64,
) -> i64 {
    if stream.is_null() || buffer.is_null() {
        return -1;
    }
    let ctx = unsafe { &mut *((*stream).ctx as *mut ReadStream<'_>) };
    let remaining = ctx.data.len().saturating_sub(ctx.offset);
    let n = remaining.min(size as usize);
    unsafe {
        std::ptr::copy_nonoverlapping(ctx.data.as_ptr().add(ctx.offset), buffer.cast::<u8>(), n);
    }
    ctx.offset += n;
    n as i64
}

#[allow(dead_code)]
unsafe extern "C" fn write_stream(
    stream: *const clap_ostream,
    buffer: *const c_void,
    size: u64,
) -> i64 {
    if stream.is_null() || buffer.is_null() {
        return -1;
    }
    let out = unsafe { &mut *((*stream).ctx as *mut Vec<u8>) };
    let slice = unsafe { std::slice::from_raw_parts(buffer.cast::<u8>(), size as usize) };
    out.extend_from_slice(slice);
    size as i64
}

fn load_state(instance: &PluginInstance<()>, blob: &[u8]) -> Result<(), String> {
    if blob.is_empty() {
        return Ok(());
    }
    let Some(state) = plugin_extension::<clap_plugin_state>(instance, CLAP_EXT_STATE) else {
        return Err("CLAP plugin has no state extension".to_string());
    };
    let load = state
        .load
        .ok_or_else(|| "CLAP state extension missing load".to_string())?;
    let mut ctx = ReadStream {
        data: blob,
        offset: 0,
    };
    let stream = clap_istream {
        ctx: (&mut ctx as *mut ReadStream<'_>).cast::<c_void>(),
        read: Some(read_stream),
    };
    if unsafe { load(instance.plugin_shared_handle().as_raw_ptr(), &stream) } {
        Ok(())
    } else {
        Err("CLAP state load returned false".to_string())
    }
}

#[allow(dead_code)]
fn save_state(instance: &PluginInstance<()>) -> Result<Vec<u8>, String> {
    let Some(state) = plugin_extension::<clap_plugin_state>(instance, CLAP_EXT_STATE) else {
        return Err("CLAP plugin has no state extension".to_string());
    };
    let save = state
        .save
        .ok_or_else(|| "CLAP state extension missing save".to_string())?;
    let mut out = Vec::<u8>::new();
    let stream = clap_ostream {
        ctx: (&mut out as *mut Vec<u8>).cast::<c_void>(),
        write: Some(write_stream),
    };
    if unsafe { save(instance.plugin_shared_handle().as_raw_ptr(), &stream) } {
        Ok(out)
    } else {
        Err("CLAP state save returned false".to_string())
    }
}

pub fn discover_params(
    bundle_path: &Path,
    plugin_id: &str,
) -> Result<Vec<PolyclavClapParamInfo>, String> {
    let entry = unsafe { PluginEntry::load(bundle_path) }
        .map_err(|e| format!("CLAP load {}: {e:?}", bundle_path.display()))?;
    let plugin_id_c =
        CString::new(plugin_id).map_err(|e| format!("CLAP plugin id has interior NUL: {e}"))?;
    let instance =
        PluginInstance::<()>::new(|_| (), |_| (), &entry, plugin_id_c.as_c_str(), host_info())
            .map_err(|e| format!("CLAP instantiate {plugin_id}: {e:?}"))?;
    Ok(query_params(&instance))
}

fn encode_param_value_event(clap_id: u32, value: f64) -> Option<ParamValueEvent> {
    let id = ClapId::from_raw(clap_id)?;
    Some(ParamValueEvent::new(
        0,
        id,
        Pckn::match_all(),
        value,
        Cookie::empty(),
    ))
}

/// Cached `HostInfo`. CLAP plugins expect a stable host identity; rebuild
/// it once and reuse for every instantiation.
type ClapStateResponse = mpsc::SyncSender<Result<Vec<u8>, String>>;
type ClapStateRequestSender = mpsc::Sender<ClapStateResponse>;
type ClapStateRequestReceiver = mpsc::Receiver<ClapStateResponse>;
type LoadedClapOwner = (ClapInstance, PluginInstance<()>, ClapStateRequestReceiver);

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
    state_request_tx: ClapStateRequestSender,
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
    /// Reusable output event buffer for plugin-to-host parameter feedback.
    output_events: EventBuffer,
    /// Backend generation for filtering host-global parameter writes.
    generation: u64,
    /// Discovered parameter ids for filtering host-global parameter writes.
    param_ids: Vec<u32>,
    /// Selected CLAP note input dialect and port.
    note_input: ClapNoteInput,
    input_event_count: u32,
    input_event_limit: u32,
}

// SAFETY: `StartedPluginAudioProcessor` is already `Send`. `PluginInstance`
// is intentionally not stored here; it remains on the CLAP owner thread and is
// reached only by `state_request_tx`. `AudioPorts`, `EventBuffer`, and `Vec` are
// `Send`, so moving this audio processor wrapper to the audio thread preserves
// the plugin-instance thread affinity documented at the top of this file.
unsafe impl Send for ClapInstance {}

impl ClapInstance {
    pub(crate) fn state_request_sender(&self) -> ClapStateRequestSender {
        self.state_request_tx.clone()
    }

    /// Load a CLAP bundle and instantiate the plugin with the given id.
    /// Runs on a background worker thread; never on the audio thread.
    pub fn load(
        bundle_path: &Path,
        plugin_id: &str,
        sample_rate: f64,
        max_block: usize,
        state_blob: Option<&[u8]>,
        generation: u64,
    ) -> Result<Self, String> {
        let bundle_path = bundle_path.to_path_buf();
        let plugin_id = plugin_id.to_string();
        let state_blob = state_blob.map(|b| b.to_vec());
        let (loaded_tx, loaded_rx) = mpsc::sync_channel(1);

        std::thread::Builder::new()
            .name("polyclav-clap-owner".into())
            .spawn(move || {
                let result = (|| -> Result<LoadedClapOwner, String> {
                    // 1. Load the .clap dynamic library and its entry descriptor.
                    // SAFETY: `PluginEntry::load` is unsafe because dlopen can execute
                    // arbitrary code; we accept that risk as the host of plugins.
                    let entry = unsafe { PluginEntry::load(&bundle_path) }
                        .map_err(|e| format!("CLAP load {}: {e:?}", bundle_path.display()))?;

                    // 2. Pull the plugin factory and verify the requested id exists.
                    let factory = entry
                        .get_plugin_factory()
                        .ok_or_else(|| "CLAP entry has no plugin factory".to_string())?;

                    let plugin_id_c = CString::new(plugin_id.as_str())
                        .map_err(|e| format!("CLAP plugin id has interior NUL: {e}"))?;

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

                    // 3. Instantiate on this owner thread. The PluginInstance never
                    // leaves this thread; the Send audio processor is handed to the
                    // audio backend, and state save requests are serviced here.
                    let mut instance = PluginInstance::<()>::new(
                        |_| (),
                        |_| (),
                        &entry,
                        plugin_id_c.as_c_str(),
                        host_info(),
                    )
                    .map_err(|e| format!("CLAP instantiate {plugin_id}: {e:?}"))?;

                    // 4. Choose the input note event dialect and restore optional state before activation.
                    let note_input = query_note_input(&instance)?;
                    if let Some(blob) = state_blob.as_deref() {
                        load_state(&instance, blob)?;
                    }
                    let params = query_params(&instance);

                    // 5. Activate with our audio configuration.
                    let audio_cfg = PluginAudioConfiguration {
                        sample_rate,
                        min_frames_count: 1,
                        max_frames_count: max_block as u32,
                    };
                    let stopped = instance
                        .activate(|_, _| (), audio_cfg)
                        .map_err(|e| format!("CLAP activate {plugin_id}: {e:?}"))?;

                    // 6. Start processing — must succeed before we ship the
                    // processor to the audio thread.
                    let processor = stopped
                        .start_processing()
                        .map_err(|e| format!("CLAP start_processing {plugin_id}: {e:?}"))?;

                    drop(entry);

                    let (state_request_tx, state_request_rx) = mpsc::channel();
                    let clap = Self {
                        processor,
                        state_request_tx,
                        output_ports: AudioPorts::with_capacity(2, 1),
                        out_l: Vec::with_capacity(max_block),
                        out_r: Vec::with_capacity(max_block),
                        input_ports: AudioPorts::with_capacity(0, 0),
                        input_events: EventBuffer::with_capacity(64),
                        output_events: EventBuffer::with_capacity(64),
                        generation,
                        param_ids: params.into_iter().map(|p| p.clap_id).collect(),
                        note_input,
                        input_event_count: 0,
                        input_event_limit: 64,
                    };
                    Ok((clap, instance, state_request_rx))
                })();

                match result {
                    Ok((clap, instance, state_request_rx)) => {
                        let _ = loaded_tx.send(Ok(clap));
                        // Keep PluginInstance pinned to this owner thread and service
                        // non-realtime state saves here until the audio backend drops
                        // its sender.
                        for tx in state_request_rx {
                            let _ = tx.send(save_state(&instance));
                        }
                    }
                    Err(e) => {
                        let _ = loaded_tx.send(Err(e));
                    }
                }
            })
            .map_err(|e| format!("spawn CLAP owner thread: {e}"))?;

        loaded_rx
            .recv()
            .map_err(|e| format!("CLAP owner thread exited before load completed: {e}"))?
    }

    /// Push a MIDI event onto the next-block input buffer.
    pub fn push_midi(&mut self, event: &MidiEvent) {
        for encoded in encode_midi_event(event, self.note_input) {
            if self.input_event_count >= self.input_event_limit {
                record_clap_input_event_drop();
                continue;
            }
            match encoded {
                EncodedClapEvent::RawMidi { port_index, bytes } => {
                    self.input_events
                        .push(&ClapMidiEvent::new(0, port_index, bytes));
                }
                EncodedClapEvent::TypedNoteOn {
                    port_index,
                    channel,
                    note,
                    velocity,
                    ..
                } => {
                    self.input_events.push(&NoteOnEvent::new(
                        0,
                        typed_pckn(port_index, channel, note),
                        f64::from(velocity) / 127.0,
                    ));
                }
                EncodedClapEvent::TypedNoteOff {
                    port_index,
                    channel,
                    note,
                    ..
                } => {
                    self.input_events.push(&NoteOffEvent::new(
                        0,
                        typed_pckn(port_index, channel, note),
                        0.0,
                    ));
                }
            }
            self.input_event_count += 1;
        }
    }

    fn push_pending_param_events(&mut self) {
        while let Some(write) = clap_param_queue().pop() {
            if write.generation != self.generation || !self.param_ids.contains(&write.clap_id) {
                continue;
            }
            if self.input_event_count >= self.input_event_limit {
                record_clap_input_event_drop();
                continue;
            }
            if let Some(ev) = encode_param_value_event(write.clap_id, write.value) {
                self.input_events.push(&ev);
                self.input_event_count += 1;
            }
        }
    }

    fn collect_feedback_events(&self) {
        for ev in self.output_events.iter() {
            if let Some(param) = ev.as_event::<ParamValueEvent>() {
                if let Some(id) = param.param_id() {
                    let _ = clap_feedback_queue().push(PolyclavClapFeedbackEvent {
                        clap_id: id.get(),
                        value: param.value(),
                        kind: 0,
                    });
                }
            }
        }
    }

    /// Render a single audio callback into the interleaved stereo `samples`
    /// buffer. Called on the audio thread.
    pub fn render(&mut self, samples: &mut [f32]) {
        let n_frames = samples.len() / 2;
        self.push_pending_param_events();
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

        self.collect_feedback_events();

        // Clear MIDI/param input for the next block regardless of outcome.
        self.input_events.clear();
        self.input_event_count = 0;

        if result.is_err() {
            record_plugin_render_error();
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

#[cfg(test)]
mod tests {
    use super::*;

    fn note_input(dialect: ClapNoteDialect) -> ClapNoteInput {
        ClapNoteInput {
            dialect,
            port_index: 0,
            port_id: 0,
        }
    }

    #[test]
    fn clap_dialect_note_on_emits_one_typed_event_with_selected_port() {
        let events = encode_midi_event(
            &MidiEvent::NoteOn {
                channel: 2,
                note: 64,
                velocity: 100,
            },
            ClapNoteInput {
                dialect: ClapNoteDialect::Clap,
                port_index: 3,
                port_id: 99,
            },
        );
        assert_eq!(
            events,
            vec![EncodedClapEvent::TypedNoteOn {
                port_index: 3,
                channel: 2,
                note: 64,
                velocity: 100,
                note_id: -1
            }]
        );
    }

    #[test]
    fn midi_dialect_note_on_emits_one_raw_midi_event_with_selected_port_index() {
        let events = encode_midi_event(
            &MidiEvent::NoteOn {
                channel: 2,
                note: 64,
                velocity: 100,
            },
            ClapNoteInput {
                dialect: ClapNoteDialect::Midi,
                port_index: 4,
                port_id: 42,
            },
        );
        assert_eq!(
            events,
            vec![EncodedClapEvent::RawMidi {
                port_index: 4,
                bytes: [0x92, 64, 100]
            }]
        );
    }

    #[test]
    fn note_off_uses_selected_dialect() {
        assert_eq!(
            encode_midi_event(
                &MidiEvent::NoteOff {
                    channel: 1,
                    note: 60
                },
                note_input(ClapNoteDialect::Clap)
            ),
            vec![EncodedClapEvent::TypedNoteOff {
                port_index: 0,
                channel: 1,
                note: 60,
                note_id: -1
            }]
        );
        assert_eq!(
            encode_midi_event(
                &MidiEvent::NoteOff {
                    channel: 1,
                    note: 60
                },
                note_input(ClapNoteDialect::Midi)
            ),
            vec![EncodedClapEvent::RawMidi {
                port_index: 0,
                bytes: [0x81, 60, 0]
            }]
        );
    }

    #[test]
    fn velocity_zero_note_on_is_encoded_as_note_off() {
        assert_eq!(
            encode_midi_event(
                &MidiEvent::NoteOn {
                    channel: 1,
                    note: 60,
                    velocity: 0
                },
                note_input(ClapNoteDialect::Clap)
            ),
            vec![EncodedClapEvent::TypedNoteOff {
                port_index: 0,
                channel: 1,
                note: 60,
                note_id: -1
            }]
        );
        assert_eq!(
            encode_midi_event(
                &MidiEvent::NoteOn {
                    channel: 1,
                    note: 60,
                    velocity: 0
                },
                note_input(ClapNoteDialect::Midi)
            ),
            vec![EncodedClapEvent::RawMidi {
                port_index: 0,
                bytes: [0x81, 60, 0]
            }]
        );
    }

    #[test]
    fn param_event_encoding_produces_param_value() {
        let ev = encode_param_value_event(42, 0.75).expect("valid id");
        assert_eq!(ev.param_id().unwrap().get(), 42);
        assert_eq!(ev.value(), 0.75);
    }

    #[test]
    fn invalid_param_id_is_rejected() {
        assert!(encode_param_value_event(u32::MAX, 0.5).is_none());
    }

    #[test]
    fn note_dialect_prefers_clap_then_midi() {
        assert_eq!(
            choose_note_dialect(CLAP_NOTE_DIALECT_CLAP | CLAP_NOTE_DIALECT_MIDI),
            Some(ClapNoteDialect::Clap)
        );
        assert_eq!(
            choose_note_dialect(CLAP_NOTE_DIALECT_MIDI),
            Some(ClapNoteDialect::Midi)
        );
        assert_eq!(choose_note_dialect(0), None);
    }
}
