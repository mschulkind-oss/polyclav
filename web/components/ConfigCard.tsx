"use client";

import { useState } from "react";
import { api, errorMessage } from "@/lib/api";

/**
 * polyclav.toml viewer/editor (docs/WEB_UI.md phase C). Loaded lazily on
 * first expand; Save PUTs the full TOML text. The daemon validates
 * against a temp file and atomically replaces the config — hot reload is
 * out of scope, hence the persistent restart banner. Validation failures
 * (422) render in the error box.
 */
export function ConfigCard() {
  const [text, setText] = useState("");
  const [loaded, setLoaded] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [banner, setBanner] = useState(false);

  const load = async () => {
    const t = await api.configGet();
    if (t !== null) {
      setText(t);
      setLoaded(true);
      setError(null);
    }
  };

  const save = async () => {
    const r = await api.configPut(text);
    if (!r) {
      setError("request failed");
      return;
    }
    if (r.ok) {
      setError(null);
      setBanner(true); // persistent until restart
    } else {
      setError(await errorMessage(r));
    }
  };

  return (
    <details
      onToggle={(e) => {
        if (e.currentTarget.open && !loaded) load();
      }}
    >
      <summary>polyclav.toml — edit &amp; save (validated before write)</summary>
      {banner ? <div className="warn">Saved. Restart polyclav to apply the new config.</div> : null}
      <textarea
        className="config-text"
        name="config"
        spellCheck={false}
        placeholder="loading polyclav.toml…"
        value={text}
        onChange={(e) => setText(e.currentTarget.value)}
      />
      <div className="btnrow">
        <button type="button" onClick={save}>
          Save
        </button>
        <button type="button" onClick={load}>
          Reload
        </button>
      </div>
      {error ? <div className="errbox">{error}</div> : null}
    </details>
  );
}
