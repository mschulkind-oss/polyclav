// polyclav — WebMIDI capture shim for Novation Components
// ---------------------------------------------------------------------------
// Logs every SysEx (and other MIDI message) Components sends to / receives
// from a Launchkey, so we can verify our own encoder against the wire.
//
// HOW TO INSTALL
//   The shim MUST be in place before the page calls navigator.requestMIDIAccess.
//   Pick one of these:
//
//   A) Quick paste (one session):
//      1. Open https://components.novationmusic.com
//      2. Open DevTools (F12).
//      3. Paste this whole file into the Console and hit Enter.
//      4. Reload the page (Ctrl-R). The shim now wraps requestMIDIAccess.
//
//   B) Persistent (DevTools Sources → Overrides):
//      1. Open DevTools → Sources → Overrides → "Select folder for overrides".
//      2. Pick any local folder.
//      3. Right-click the Components root document, "Save for overrides".
//      4. Add a small inline <script> at the top of <head> that does:
//             const s = document.createElement('script');
//             s.src = '/components-capture.js';
//             document.head.appendChild(s);
//      5. Save this file as components-capture.js in the overrides folder.
//      6. Hard-reload.
//
// USAGE
//   window.__polyclavMidiTap.log         // array of { ts, isoTs, dir, port, hex, isSysex }
//   window.__polyclavSaveLog()           // downloads the log as JSON
//   window.__polyclavClearLog()          // resets the log
//   window.__polyclavMidiTap.stop()      // restore the original requestMIDIAccess
//
// CAVEATS
//   - Must run BEFORE the page calls requestMIDIAccess. If you paste it after
//     the page already has MIDI access, reload the page.
//   - onmidimessage on MIDIInput is a property setter, so the shim redefines
//     the property per instance. addEventListener('midimessage', ...) is also
//     intercepted via a wrapper.
//   - The shim does NOT alter the bytes — it only observes.
// ---------------------------------------------------------------------------

(() => {
    if (window.__polyclavMidiTap) {
        console.warn('[polyclav] capture shim already installed; skipping');
        return;
    }

    const log = [];
    const originalRMA = navigator.requestMIDIAccess
        ? navigator.requestMIDIAccess.bind(navigator)
        : null;

    if (!originalRMA) {
        console.error('[polyclav] navigator.requestMIDIAccess unavailable');
        return;
    }

    const toHex = (data) => {
        const buf = data instanceof Uint8Array ? data : new Uint8Array(data.buffer || data);
        let out = '';
        for (let i = 0; i < buf.length; i++) {
            out += (i ? ' ' : '') + buf[i].toString(16).padStart(2, '0');
        }
        return out;
    };

    const record = (dir, port, data) => {
        const ts = performance.now();
        const isoTs = new Date().toISOString();
        const hex = toHex(data);
        const isSysex = data.length > 0 && (data[0] === 0xf0);
        const entry = { ts, isoTs, dir, port, hex, isSysex };
        log.push(entry);
        const tag = isSysex ? 'SYSEX' : 'MIDI ';
        console.log(`[${tag} ${ts.toFixed(2)} ${dir} "${port}"] ${hex}`);
    };

    const wrapOutput = (output) => {
        if (output.__polyclavWrapped) return;
        output.__polyclavWrapped = true;
        const originalSend = output.send.bind(output);
        output.send = function (data, timestamp) {
            try { record('out', output.name, data); } catch (e) { console.warn('[polyclav] log out failed', e); }
            return originalSend(data, timestamp);
        };
    };

    const wrapInput = (input) => {
        if (input.__polyclavWrapped) return;
        input.__polyclavWrapped = true;

        // Wrap addEventListener('midimessage', ...).
        const originalAdd = input.addEventListener.bind(input);
        input.addEventListener = function (type, listener, opts) {
            if (type === 'midimessage' && typeof listener === 'function') {
                const wrapped = function (ev) {
                    try { record('in', input.name, ev.data); } catch (e) { console.warn('[polyclav] log in failed', e); }
                    return listener.call(this, ev);
                };
                // Track wrapper so removeEventListener still works.
                listener.__polyclavWrapper = wrapped;
                return originalAdd(type, wrapped, opts);
            }
            return originalAdd(type, listener, opts);
        };
        const originalRemove = input.removeEventListener.bind(input);
        input.removeEventListener = function (type, listener, opts) {
            if (type === 'midimessage' && listener && listener.__polyclavWrapper) {
                return originalRemove(type, listener.__polyclavWrapper, opts);
            }
            return originalRemove(type, listener, opts);
        };

        // Wrap the onmidimessage property — page may assign to it directly.
        let current = null;
        Object.defineProperty(input, 'onmidimessage', {
            configurable: true,
            enumerable: true,
            get() { return current; },
            set(handler) {
                current = handler;
                // Re-install our addEventListener-based interception so the
                // assigned handler also runs through record().
                if (input.__polyclavPropListener) {
                    originalRemove('midimessage', input.__polyclavPropListener);
                    input.__polyclavPropListener = null;
                }
                if (typeof handler === 'function') {
                    const wrapped = function (ev) {
                        try { record('in', input.name, ev.data); } catch (e) { console.warn('[polyclav] log in failed', e); }
                        return handler.call(this, ev);
                    };
                    input.__polyclavPropListener = wrapped;
                    originalAdd('midimessage', wrapped);
                }
            },
        });
    };

    const wrapAccess = (access) => {
        access.inputs.forEach((p) => wrapInput(p));
        access.outputs.forEach((p) => wrapOutput(p));
        access.addEventListener('statechange', (ev) => {
            const p = ev.port;
            if (!p) return;
            if (p.type === 'input') wrapInput(p);
            else if (p.type === 'output') wrapOutput(p);
        });
        return access;
    };

    navigator.requestMIDIAccess = function (opts) {
        return originalRMA(opts).then(wrapAccess);
    };

    window.__polyclavMidiTap = {
        log,
        stop() { navigator.requestMIDIAccess = originalRMA; },
    };
    window.__polyclavSaveLog = function () {
        const blob = new Blob([JSON.stringify(log, null, 2)], { type: 'application/json' });
        const url = URL.createObjectURL(blob);
        const a = document.createElement('a');
        a.href = url;
        a.download = `polyclav-midi-capture-${new Date().toISOString().replace(/[:.]/g, '-')}.json`;
        document.body.appendChild(a);
        a.click();
        a.remove();
        URL.revokeObjectURL(url);
    };
    window.__polyclavClearLog = function () { log.length = 0; };

    console.log('[polyclav] WebMIDI capture shim installed. Reload the page to capture from session start.');
})();
