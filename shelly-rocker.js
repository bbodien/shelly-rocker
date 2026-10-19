// Shelly Pro Dimmer 2PM - corston centre-retractive rocker behaviour
// Top button: single press = turn off, hold = dim down (floors at DIM_MIN_BRIGHTNESS)
//             double press = turn on at the light's minimum + NIGHT_OFFSET (night light)
// The minimum is each light's own range_map[0], read from the device (at least DIM_MIN_BRIGHTNESS).
// Bottom button: single press = turn on, hold = dim up (turns on at last brightness if off)
//               double press = turn on at 100% brightness
// Requires output type set to Detached on both channels.

const DIM_STEP = 1;              // % per tick
const DIM_INTERVAL = 75;         // ms between steps
const DIM_MIN_BRIGHTNESS = 1;    // % to start at when dimming up from off
const DOUBLE_PRESS_WINDOW = 400; // ms window to detect a second btn_down
const NIGHT_OFFSET = 10;         // % above the light's minimum for the top double press (night light)

// component -> [light id, direction: -1=off/down, +1=on/up]
let INPUT_CFG = {
  "input:0": [0,  1],  // SW1 bottom -> light 0 on  / dim up
  "input:1": [0, -1],  // SW2 top    -> light 0 off / dim down
  "input:2": [1,  1],  // SW3 bottom -> light 1 on  / dim up
  "input:3": [1, -1],  // SW4 top    -> light 1 off / dim down
};

let timers = {};     // dim timers per key
let dimActive = {};  // guards against btn_up racing the initial GetStatus
let brightness = {}; // locally tracked brightness per light id
let dpTimers = {};   // double press window timers per key
let dpFired = {};    // suppresses single_push that follows a detected double press
let minB = {};       // per-light minimum brightness (range_map[0]) read from the device

// Lowest brightness this fitting can usefully show: the light's own range_map
// minimum, but never below DIM_MIN_BRIGHTNESS.
function floorOf(lid) {
  let m = minB[lid] || 0;
  return m > DIM_MIN_BRIGHTNESS ? m : DIM_MIN_BRIGHTNESS;
}

function loadMin(lid) {
  Shelly.call("Light.GetConfig", { id: lid }, function (res) {
    if (res && res.range_map) minB[lid] = res.range_map[0];
  });
}

for (let k in INPUT_CFG) loadMin(INPUT_CFG[k][0]);

Shelly.addEventHandler(function (ev) {
  let cfg = INPUT_CFG[ev.component];
  if (!cfg || !ev.info) return;

  let lid = cfg[0];
  let dir = cfg[1];
  let e = ev.info.event;
  let key = ev.component;

  // Double press target: bottom = 100%, top = the light's minimum + NIGHT_OFFSET
  let dpLevel = dir > 0 ? 100 : Math.min(100, floorOf(lid) + NIGHT_OFFSET);

  if (e === "btn_down") {
    if (dpTimers[key]) {
      // Second press within window — double press
      Timer.clear(dpTimers[key]); dpTimers[key] = null;
      dpFired[key] = true;
      brightness[lid] = dpLevel;
      Shelly.call("Light.Set", { id: lid, on: true, brightness: dpLevel });
    } else {
      dpFired[key] = false; // new gesture: drop any stale double press flag
      loadMin(lid);         // pick up range_map changes made in the Shelly UI
      dpTimers[key] = Timer.set(DOUBLE_PRESS_WINDOW, false, function () {
        dpTimers[key] = null;
      });
    }

  } else if (e === "double_push") {
    // The firmware reports a double press instead of single_push, so this is
    // where dpFired must be cleared. Also covers presses slower than
    // DOUBLE_PRESS_WINDOW that the firmware still counts as a double.
    if (!dpFired[key]) {
      brightness[lid] = dpLevel;
      Shelly.call("Light.Set", { id: lid, on: true, brightness: dpLevel });
    }
    dpFired[key] = false;

  } else if (e === "single_push") {
    if (timers[key]) { Timer.clear(timers[key]); timers[key] = null; }
    if (dpFired[key]) { dpFired[key] = false; return; }
    Shelly.call("Light.Set", { id: lid, on: dir > 0 });

  } else if (e === "long_push") {
    if (dpTimers[key]) { Timer.clear(dpTimers[key]); dpTimers[key] = null; }
    dimActive[key] = true;
    Shelly.call("Light.GetStatus", { id: lid }, function (res) {
      if (!dimActive[key]) return;
      if (!res) return;
      if (!res.output) {
        if (dir < 0) return;
        Shelly.call("Light.Set", { id: lid, on: true });
      }
      brightness[lid] = res.brightness;
      timers[key] = Timer.set(DIM_INTERVAL, true, function () {
        let b = brightness[lid] + dir * DIM_STEP;
        let lo = floorOf(lid);
        b = b < lo ? lo : b > 100 ? 100 : b;
        brightness[lid] = b;
        Shelly.call("Light.Set", { id: lid, brightness: b });
      });
    });

  } else if (e === "btn_up") {
    dimActive[key] = false;
    if (timers[key]) { Timer.clear(timers[key]); timers[key] = null; }
  }
});
