# shelly-rocker

A Shelly script for centre-retractive rocker switches (e.g. Corston) wired to a Shelly Pro Dimmer 2PM.

## Behaviour

| Button | Action | Result |
|--------|--------|--------|
| Bottom | Single press | Turn on at last brightness |
| Bottom | Double press | Turn on at 100% |
| Bottom | Hold | Dim up |
| Top | Single press | Turn off |
| Top | Hold | Dim down |

Brightness is floored at 1% when dimming down. Holding while the light is off turns it on at the last brightness and dims up from there.

## Requirements

- Shelly Pro Dimmer 2PM (or compatible Gen2+ dimmer with scripting support)
- Output type set to **Detached** on all channels

## Configuration

At the top of `shelly-rocker.js`:

```js
const DIM_STEP = 1;              // % per tick
const DIM_INTERVAL = 75;         // ms between ticks
const DIM_MIN_BRIGHTNESS = 1;    // floor when dimming down
const DOUBLE_PRESS_WINDOW = 400; // ms to detect a double press
```

The `INPUT_CFG` map wires each input to a light channel and direction. Adjust if your switch wiring differs:

```js
let INPUT_CFG = {
  "input:0": [0,  1],  // SW1 bottom -> light 0 on  / dim up
  "input:1": [0, -1],  // SW2 top    -> light 0 off / dim down
  "input:2": [1,  1],  // SW3 bottom -> light 1 on  / dim up
  "input:3": [1, -1],  // SW4 top    -> light 1 off / dim down
};
```

## Deployment

### Go deployer (recommended)

Scans your local network for compatible Shelly dimmers and deploys via a TUI:

```sh
go build -o deploy .
./deploy
```

### Python deployer

Edit the `DEVICES` list in `deploy-shelly-script.py` with your device IPs, then:

```sh
python3 deploy-shelly-script.py
```

## License

MIT
