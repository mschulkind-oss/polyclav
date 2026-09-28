// Run from web/: node --test app/launchkey-debug/layout.browser.mjs
// Real Chromium geometry regression for offline replay; requires chromium and the local Next dev server.
import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { mkdtemp, readFile, rm } from "node:fs/promises";
import { createServer } from "node:net";
import { tmpdir } from "node:os";
import path from "node:path";
import test from "node:test";

const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));
async function freePort() {
  const server = createServer();
  await new Promise((resolve) => server.listen(0, "127.0.0.1", resolve));
  const port = server.address().port;
  await new Promise((resolve) => server.close(resolve));
  return port;
}
async function until(fn, timeout = 45000) {
  const end = Date.now() + timeout;
  while (Date.now() < end) {
    try {
      const result = await fn();
      if (result) return result;
    } catch {
      /* server or browser not ready yet */
    }
    await sleep(200);
  }
  throw new Error("Timed out waiting for browser or page");
}
class CDP {
  constructor(url) {
    this.socket = new WebSocket(url);
    this.pending = new Map();
    this.sequence = 0;
    this.ready = new Promise((resolve, reject) => {
      this.socket.addEventListener("open", resolve, { once: true });
      this.socket.addEventListener("error", reject, { once: true });
    });
    this.socket.addEventListener("message", ({ data }) => {
      const message = JSON.parse(data);
      const pending = this.pending.get(message.id);
      if (!pending) return;
      this.pending.delete(message.id);
      if (message.error) pending.reject(new Error(JSON.stringify(message.error)));
      else pending.resolve(message.result);
    });
  }
  async send(method, params = {}) {
    await this.ready;
    const id = ++this.sequence;
    return new Promise((resolve, reject) => {
      this.pending.set(id, { resolve, reject });
      this.socket.send(JSON.stringify({ id, method, params }));
    });
  }
  async eval(expression) {
    const result = await this.send("Runtime.evaluate", {
      expression,
      awaitPromise: true,
      returnByValue: true,
    });
    if (result.exceptionDetails) throw new Error(result.exceptionDetails.text);
    return result.result.value;
  }
  close() {
    this.socket.close();
  }
}

const inventory = {
  controls: [
    {
      events: [
        {
          timestamp: "2026-09-26T18:15:00Z",
          port_role: "daw",
          kind: "cc",
          channel: 7,
          raw_values: { controller: 29, value: 5 },
        },
        {
          timestamp: "2026-09-26T18:15:01Z",
          port_role: "daw",
          kind: "cc",
          channel: 16,
          raw_values: { controller: 85, value: 66 },
        },
        {
          timestamp: "2026-09-26T18:15:02Z",
          port_role: "daw",
          kind: "poly_aftertouch",
          channel: 1,
          raw_values: { note: 96, value: 127 },
        },
        {
          timestamp: "2026-09-26T18:15:03Z",
          port_role: "daw",
          kind: "cc",
          channel: 16,
          raw_values: { controller: 5, value: 127 },
        },
        {
          timestamp: "2026-09-26T18:15:04Z",
          port_role: "daw",
          kind: "cc",
          channel: 1,
          raw_values: { controller: 51, value: 127 },
        },
      ],
    },
  ],
};

const geometry = `(() => {
  const box = (el) => {
    const r = el.getBoundingClientRect();
    return { x: r.x, y: r.y, right: r.right, bottom: r.bottom, width: r.width, height: r.height,
      scrollWidth: el.scrollWidth, clientWidth: el.clientWidth, scrollHeight: el.scrollHeight, clientHeight: el.clientHeight };
  };
  const pick = (selector) => [...document.querySelectorAll(selector)].map(box);
  return { doc: box(document.documentElement), main: pick('main.lk-debug'), panel: pick('.lk-panel'),
    latest: pick('.lk-latest'), source: pick('.lk-source-strip, .lk-source-strip > *'), body: pick('.lk-body'), blocks: pick('.lk-body > section'),
    rows: pick('.lk-faders, .lk-fader-buttons, .lk-encoder-row, .lk-pad-row, .lk-grid, .lk-transport, .lk-daw-commands'),
    tiles: pick('.lk-control'), keybed: pick('.lk-keys'),
    tileValueOverflow: [...document.querySelectorAll('.lk-control small')].map((el) => getComputedStyle(el).overflowY),
    rawColors: ['.lk-raw-details summary', '.lk-event-list', '.lk-offline-tools .hint'].map((selector) => getComputedStyle(document.querySelector(selector)).color),
    collisions: [...document.querySelectorAll('.lk-faders, .lk-fader-buttons, .lk-encoder-row, .lk-pad-row, .lk-grid, .lk-transport, .lk-daw-commands, .lk-wheels, .lk-mini-row')].flatMap((row) => {
      const r = row.getBoundingClientRect();
      const tiles = [...row.querySelectorAll(':scope > .lk-control')];
      return tiles.flatMap((tile, i) => {
        const a = tile.getBoundingClientRect();
        const errors = [];
        if (a.left < r.left - 1 || a.right > r.right + 1 || a.top < r.top - 1 || a.bottom > r.bottom + 1) errors.push('tile outside row');
        for (const other of tiles.slice(i + 1)) {
          const b = other.getBoundingClientRect();
          if (a.left < b.right - 1 && b.left < a.right - 1 && a.top < b.bottom - 1 && b.top < a.bottom - 1) errors.push('overlapping tiles');
        }
        return errors;
      });
    }),
    clippedLabels: [...document.querySelectorAll('.lk-control span, .lk-control small')].filter((el) => el.scrollHeight > el.clientHeight + 1 || el.scrollWidth > el.clientWidth + 1).map((el) => el.parentElement.dataset.control),
    latestText: document.querySelector('.lk-latest').textContent,
    values: [...document.querySelectorAll('.lk-control')].map((el) => ({ id: el.dataset.control, text: el.textContent })) };
})()`;

for (const [width, height] of [
  [1440, 900],
  [1280, 800],
  [1024, 768],
  [768, 900],
  [390, 844],
]) {
  test(`offline surface geometry ${width}x${height}`, { timeout: 90000 }, async () => {
    const profile = await mkdtemp(path.join(tmpdir(), "lk-chrome-"));
    const port = await freePort();
    const server = spawn(
      process.execPath,
      ["node_modules/next/dist/bin/next", "dev", "--port", String(port), "--hostname", "127.0.0.1"],
      { stdio: "ignore", detached: true },
    );
    const browser = spawn(
      process.env.CHROMIUM ?? "chromium",
      [
        "--headless",
        "--no-sandbox",
        "--disable-dev-shm-usage",
        "--no-first-run",
        "--remote-debugging-port=0",
        `--user-data-dir=${profile}`,
        "about:blank",
      ],
      { stdio: "ignore" },
    );
    let cdp;
    try {
      const devtools = await until(
        async () =>
          (await readFile(path.join(profile, "DevToolsActivePort"), "utf8")).split("\n")[0],
      );
      const tab = await until(async () =>
        (await (await fetch(`http://127.0.0.1:${devtools}/json`)).json()).find(
          (item) => item.type === "page",
        ),
      );
      cdp = new CDP(tab.webSocketDebuggerUrl);
      await cdp.send("Emulation.setDeviceMetricsOverride", {
        width,
        height,
        deviceScaleFactor: 1,
        mobile: false,
      });
      await until(async () => (await fetch(`http://127.0.0.1:${port}/app/launchkey-debug/`)).ok);
      await cdp.send("Page.navigate", { url: `http://127.0.0.1:${port}/app/launchkey-debug/` });
      await until(
        async () => (await cdp.eval("document.querySelectorAll('.lk-control').length")) > 50,
      );
      await until(async () =>
        cdp.eval(
          "Object.keys(document.querySelector('button.active')).some(k => k.startsWith('__reactProps$'))",
        ),
      );
      await cdp.eval(
        "[...document.querySelectorAll('button')].find(b => b.textContent === 'offline report').click()",
      );
      await until(async () =>
        cdp.eval("document.querySelector('.lk-ports').textContent.includes('Offline; load')"),
      );
      const snapshots = [await cdp.eval(geometry)];
      await cdp.eval(`(() => {
        const input = document.querySelector('input[type=file]');
        const file = new File([${JSON.stringify(JSON.stringify(inventory))}], 'inventory.json', { type: 'application/json' });
        Object.defineProperty(file, 'text', { value: async () => ${JSON.stringify(JSON.stringify(inventory))} });
        Object.defineProperty(input, 'files', { configurable: true, value: [file] });
        input.dispatchEvent(new Event('change', { bubbles: true }));
      })()`);
      await until(async () =>
        cdp.eval(
          "!![...document.querySelectorAll('button')].find(b => b.textContent === 'Next event')",
        ),
      );
      for (let i = 0; i < inventory.controls[0].events.length; i++) {
        await cdp.eval(
          "[...document.querySelectorAll('button')].find(b => b.textContent === 'Next event').click()",
        );
        await until(async () =>
          cdp.eval(`document.querySelector('.lk-ports').textContent.includes('${i + 1} / 5')`),
        );
        snapshots.push(await cdp.eval(geometry));
      }
      const baseline = snapshots[0];
      for (const [index, snapshot] of snapshots.entries()) {
        const name = `${width}x${height} event ${index}`;
        assert.ok(snapshot.doc.scrollWidth <= snapshot.doc.clientWidth, `${name}: page overflow`);
        assert.deepEqual(snapshot.collisions, [], `${name}: tile collision or escaped row`);
        assert.deepEqual(snapshot.clippedLabels, [], `${name}: unreadable tile label or value`);
        assert.ok(
          snapshot.tileValueOverflow.every((value) => value === "hidden"),
          `${name}: native scroll arrows inside controls`,
        );
        assert.deepEqual(
          snapshot.rawColors,
          ["rgb(182, 234, 255)", "rgb(220, 232, 246)", "rgb(181, 197, 213)"],
          `${name}: raw input or replay text lacks contrast`,
        );
        assert.ok(
          snapshot.panel[0].right <= width + 1 && snapshot.panel[0].x >= -1,
          `${name}: panel outside viewport`,
        );
        for (const group of ["body", "blocks", "rows", "tiles"]) {
          for (const rect of snapshot[group]) {
            assert.ok(rect.width > 0 && rect.height > 0, `${name}: empty ${group}`);
            assert.ok(
              rect.x >= snapshot.panel[0].x - 1 && rect.right <= snapshot.panel[0].right + 1,
              `${name}: ${group} outside panel`,
            );
            assert.ok(rect.scrollWidth <= rect.clientWidth + 1, `${name}: sideways ${group}`);
          }
        }
        for (const tile of snapshot.tiles) {
          assert.ok(tile.scrollHeight <= tile.clientHeight + 1, `${name}: tile content clipped`);
        }
        // Every block and row remains at the same position even when labels and values change.
        for (const group of ["latest", "body", "blocks", "rows", "tiles"]) {
          assert.deepEqual(
            snapshot[group].map(({ x, y, width, height }) => ({ x, y, width, height })),
            baseline[group].map(({ x, y, width, height }) => ({ x, y, width, height })),
            `${name}: ${group} moved or resized`,
          );
        }
        assert.ok(
          snapshot.keybed[0].scrollWidth >= snapshot.keybed[0].clientWidth,
          `${name}: keybed measurement`,
        );
      }
      assert.match(snapshots[1].values.find((v) => v.id === "pad-layout").text, /Custom 1/);
      assert.match(snapshots[2].values.find((v) => v.id === "encoder-1").text, /step \+2/);
      assert.match(snapshots[3].values.find((v) => v.id === "pad-top-1").text, /pressure 127/);
      assert.match(snapshots[5].latestText, /encoder up/);
      await cdp.eval(`window.EventSource = class {
        static CLOSED = 2;
        constructor() { this.listeners = {}; window.debugEvents = this; }
        addEventListener(name, cb) { this.listeners[name] = cb; }
        emit(name, payload) { this.listeners[name]?.({ data: JSON.stringify(payload) }); }
        close() {}
      }`);
      await cdp.eval(
        "[...document.querySelectorAll('button')].find(b => b.textContent === 'daemon raw SSE').click()",
      );
      await until(async () => cdp.eval("!!window.debugEvents"));
      await cdp.eval(`window.debugEvents.emit('midi-raw', {
        time: '2026-09-26T18:15:10Z', source: 'launchkey-daw',
        port: '${"Launchkey MK4 61 DAW Input Port with extended device identity ".repeat(12)}',
        kind: 'cc', channel: 15, data1: 85, data2: 66, raw: 'bf 55 42'
      })`);
      await until(async () =>
        cdp.eval(
          "document.querySelector('.lk-latest').textContent.includes('extended device identity')",
        ),
      );
      const longPort = await cdp.eval(geometry);
      assert.ok(
        longPort.latest[0].scrollHeight > longPort.latest[0].clientHeight,
        "long port should scroll locally",
      );
      assert.ok(
        longPort.doc.scrollWidth <= longPort.doc.clientWidth,
        "long port must not overflow page",
      );
      assert.deepEqual(longPort.collisions, [], "long port: tile collision");
      for (const group of ["latest", "body", "blocks", "rows", "tiles"]) {
        assert.deepEqual(
          longPort[group].map(({ x, y, width, height }) => ({ x, y, width, height })),
          baseline[group].map(({ x, y, width, height }) => ({ x, y, width, height })),
          `long port: ${group} moved or resized`,
        );
      }
      if (width >= 1100)
        assert.ok(baseline.panel[0].width > 1100, "desktop should use the available width");
      console.log(
        `${width}x${height}: panel ${Math.round(baseline.panel[0].width)}px, keybed ${baseline.keybed[0].scrollWidth}/${baseline.keybed[0].clientWidth}px`,
      );
    } finally {
      cdp?.close();
      browser.kill();
      try {
        process.kill(-server.pid, "SIGTERM");
      } catch {
        server.kill();
      }
      await new Promise((resolve) => browser.once("exit", resolve));
      for (let attempt = 0; attempt < 10; attempt++) {
        try {
          await rm(profile, { recursive: true, force: true });
          break;
        } catch (error) {
          if (attempt === 9) console.warn(`Could not remove browser profile ${profile}: ${error}`);
          else await sleep(200);
        }
      }
    }
  });
}
