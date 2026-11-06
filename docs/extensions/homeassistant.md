# Home Assistant

[Documentation](../README.md) · [Source](../../extensions/homeassistant/)

Your own Home Assistant in Persona: read the home's state and control its devices through Home Assistant's REST API with a long-lived access token.

| Extension point | Contribution | Behavior |
| --- | --- | --- |
| `ui` | `home`, placement `nav_page`, icon `gauge` | A "家" entry on the main navigation. The overview lists alarms (leaks, smoke, jammed locks…) and every area with its device count, what is on, what is offline and its temperature / humidity. An area lists its devices, sensors or everything (offline ones behind a row). A search box and an area filter (UI version 3) find any entity. An entity opens with its state, its controls, the last 24 hours (a line for numbers, the last changes otherwise) and its attributes. |
| `connectors` | `homeassistant`, capabilities `status`, `tools` and `actions` | Read tools `homeassistant_overview`, `homeassistant_search`, `homeassistant_entity`, `homeassistant_history`; write actions `homeassistant_call` and `homeassistant_secure_call`. |

## Controls and safety

A tap in the page is the user's own and calls the service at once; unlocking and disarming ask first. The page offers, by domain: on / off (lights, switches, fans, humidifiers, helpers), brightness 20 / 50 / 100 % (dimmable lights), open / close / stop (covers, valves), on / off, ±1 step of the target temperature and the HVAC mode (climates), play / pause, volume and power (media players), start / pause / dock (vacuums, mowers), lock / unlock (locks), arm / disarm (alarm panels), run (scenes, scripts), press (buttons), an option (selects) and a value (numbers).

The assistant's writes are connector actions, so they only run after the hub's gate (`connector_gate`; by default the phone confirms each one). `homeassistant_secure_call` is the only way to locks and alarm panels and is in `confirm_always`: an `allow` rule in the gate cannot skip the confirmation. Every write, from the page or the assistant:

- names its entities (`entity_id`, one domain per call, at most 20); `data` may not carry `entity_id`, `device_id`, `area_id`, `floor_id` or `label_id`, so a call cannot be widened to an area or the whole home;
- must be in a writable domain: by default the common device domains (lights, switches, fans, covers, climate, media players, vacuums, humidifiers, water heaters, helpers, numbers, selects, buttons, scenes, scripts, remotes, sirens, valves, mowers, locks, alarm panels). Domains that act on the whole system or run commands (`homeassistant`, `hassio`, `shell_command`, `rest_command`, `notify`, …) are never writable unless listed in `writable_domains`;
- is checked against Home Assistant's own service list when it has been read.

`actions.describe` reads only cached state (no network) and writes the card as the entities' names, their current state, the service and its data. `actions.do` runs a request id once.

## Configuration

[Manifest and settings schema](../../extensions/homeassistant/persona-extension.json)

| Setting | Meaning |
| --- | --- |
| `url` | The address the hub's machine reaches Home Assistant at, `http(s)://host:port` (a path is kept for a reverse proxy under one). |
| secret `token` | A long-lived access token: Home Assistant → your profile → Security → Long-lived access tokens. It acts as that user. Areas need an administrator's token (see [Data](#data)). |
| `insecure_tls` | Skip certificate checks for an `https` address with a self-signed certificate. |
| `writable_domains` | Replace the default writable domains (an empty list makes everything read-only). |
| `hidden_areas` | Area names or ids the page and the assistant never show (another home in the same account, for example). Entities only in a hidden area are left out of searches and tools too. |

## Network and sandbox

The address is the user's, so the manifest cannot list it in `permissions.network` (which takes fixed host names). Installed from this market, the extension runs outside the sandbox and connects directly. Installed from a local package (`file:`, `dir:`, an upload) it starts in the sandbox, whose egress proxy then refuses the address: turn the sandbox off for it on the hub's machine with `personad extension sandbox homeassistant off`. A Home Assistant on the hub's own machine is `http://host.persona.internal:8123` only inside the sandbox; with it off, use `http://127.0.0.1:8123`.

## Data

States are read in one request (`/api/states`) and kept 5 seconds; areas come from one template render (`areas()`, `area_entities()`) kept 5 minutes, since the REST API has no area registry. Home Assistant keeps its template API for administrators: with a non-administrator's token it answers 401 there while states work, so the page and the tools group devices by type instead and say why (`hidden_areas` cannot apply then); services are kept 10 minutes; history is read per entity when its page or `homeassistant_history` asks. Nothing is stored on disk. Errors never contain the token; a refused token reports its shape (length, `eyJ` start, dots) and the refused request.

Home Assistant bans an address after a few failed logins and then answers 403 to everything from it, a valid token included (`ip_bans.yaml`, read at start). So after a refusal the extension does not try again on its own for 5 minutes; the page's Retry and a new token try at once. To lift a ban, remove the address from `ip_bans.yaml` and restart Home Assistant.

An area's temperature and humidity are the averages of its sensors that read like a room (10–40 °C, 0–100 %), so a fridge's or a water heater's sensor does not count.

## Limits

- No live updates: the page refreshes every 30 seconds (15 on an entity) and after each tap; there is no websocket subscription.
- No entity categories: the REST API does not say which switches are configuration toggles of a device, so they are listed with the devices; use search or `hidden_areas`.
- Cameras, colors and color temperature, and arbitrary service data from the page are not offered; the assistant can still set them through `homeassistant_call` data.
