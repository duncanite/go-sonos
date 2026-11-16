# go-sonos: the design

Research into controlling Sonos speakers, done on 2026-11-15 from public sources, and the
module built from it. The module follows what was read; nothing here was run against a
speaker.

In short: Sonos speakers are controlled entirely on the LAN, over UPnP: SOAP calls to
each speaker on port 1400. It covers playback, volume, grouping, favourites, the queue,
alarms and inputs, and needs no account. Sonos does not document it and calls it
unsupported, and since 2025 its app has a switch that turns it off; it is on by default.

## The device

From [the community's documentation](https://sonos.svrooij.io/sonos-communication),
generated from the speakers' own service descriptions:

- Speakers answer an SSDP search for `urn:schemas-upnp-org:device:ZonePlayer:1`.
- Each serves SOAP over HTTP on port 1400, sixteen services: the transport, rendering
  control for one speaker and for a group, the zone group topology, the content
  directory, the queue, the alarm clock, device properties and others.
- Speakers play in groups. A group has a coordinator, and transport commands go to it;
  volume goes to each speaker or to the group.
- State changes are UPnP events: a client subscribes and the speaker calls it back over
  HTTP.
- The music itself comes from services the speakers fetch on their own; that part needs
  Sonos's cloud and an account, and is not this module's.

## Ways in

### UPnP on port 1400 (local)

- No credentials. Read: what is playing, the groups, volume, a portable speaker's
  battery. Do: play, pause, skip, volume, group and ungroup, favourites, the queue,
  alarms, the line-in and television inputs.
- Its standing with Sonos, from [the connection security settings page](https://support.sonos.com/en/article/adjust-connection-security-settings):
  a setting named UPnP "pertains to integrations that utilize the unsupported UPnP
  protocol". "The default setting for UPnP is ON", and turning it off "will also prevent
  the Sonos app for macOS and Windows from controlling your system".
- The same page has a setting named Authentication, off by default, which makes
  integrations on the cloud and LAN APIs authenticate.

### The WebSocket on port 1443 (local, newer)

Speakers also serve a WebSocket that mirrors Sonos's cloud control API. The open clients
of it ([sonos-websocket](https://github.com/jjlawren/sonos-websocket)) connect with an API
key that is a public sample value and with certificate checks off. Sonos has not released
it for general use.

### Sonos's cloud control API (official)

OAuth against Sonos's servers with a registered integration, households, groups and
players, events sent to an HTTPS address Sonos can reach
([control](https://docs.sonos.com/docs/control), [authorize](https://docs.sonos.com/docs/authorize)).
It needs an account and a public callback, and covers less than the local API.

### AirPlay 2, Matter

AirPlay 2 on the newer speakers carries audio to them and nothing else. No Matter support
from Sonos was found.

## The ways in, compared

| | UPnP on 1400 | WebSocket on 1443 | Cloud control API |
|---|---|---|---|
| Supported by Sonos | "unsupported", and its own desktop app depends on it | not released | yes |
| Local at run time | yes | yes | no |
| Needs | the UPnP setting left on | a sample key that is no contract | an account, a registered integration, a public callback |
| Covers | everything above | playback, volume, groups | playback, volume, groups, favourites |

## What this module is

**A client of the UPnP API**, of the three ways in: it is local, needs no account, and
covers everything the others do.

- `sonos`: `Discover` asks one speaker for the household and gives a `System`, its groups
  with their coordinators and rooms. On it: `Status`, `Play`, `Pause`, `Stop`, `Next`,
  `Previous` for a group; `Volume`, `SetVolume`, `Muted`, `SetMute` for a room;
  `GroupVolume` and `SetGroupVolume`; `Join` and `Leave`. The standard library alone.
- `sonos/sonostest`: a household for tests, one HTTP server per speaker, that refuses a
  request not shaped as the documentation gives it.
- `cmd/sonos`: the command, over `internal/cli`.

### Decisions

- **The household comes from one speaker's address.** Any speaker gives the whole
  topology, so one address is enough, and a search by multicast does not pass every
  network. The search is not built.
- **The services' types and control URLs are constants**, as the documentation gives
  them, and not read from each speaker's device description: one request per action, and
  the documentation gives the same paths for every speaker.
- **A room is a speaker a person sees.** The topology marks the second speaker of a
  pair, a surround, a sub and a bridge as invisible; they are in no list, and a group
  with no room in it is left out.
- **Transport goes to the group's coordinator, volume to the room.** The documentation
  has a speaker that does not coordinate answer PLAYING whatever its group does, so
  nothing about playback is asked of one. A group's volume is set after a snapshot,
  which is what the speakers scale each room from.
- **A `System` is the household at one moment.** It follows no change; after a `Join` or
  a `Leave` it is read again.
- **What a speaker refuses is told apart**: a UPnP fault with its code, a 403, and an
  answer that is not the documented SOAP. The 403 is what Home Assistant's page gives for
  the UPnP setting turned off: "403 Client Error: Forbidden", "This error means UPnP is
  not properly enabled on your Sonos system". The community's documentation of the
  services says nothing of it.

### Not built

Events, so nothing follows a change as it happens. Favourites, the queue, the inputs and
alarms. Discovery by SSDP. The SOAP caller is this module's own and does what these
actions need; go-wemo holds a fuller UPnP stack, with service descriptions and events,
for another device.

[sonoscli](https://github.com/steipete/sonoscli) is a Go command that does all of this,
active, with its code not importable; it is a reference. The Go libraries found are
unmaintained.

## Open questions

- Whether the system is on the S2 app, or has speakers that only run S1.
- Whether the UPnP setting is on, in the app under privacy and security.
- Whether the speakers and the machine that runs the command share a network, with
  multicast passing; without it, one speaker's address is enough.

## Not verified

- Nothing above was observed on a speaker, and the module was run against its own
  stand-in only.
- What a speaker answers a transport action when it does not coordinate its group.
- Whether every model serves the services at the documented paths.
- The event subscription's details were not read in a primary source.
- Which software versions carry the UPnP setting: the page names none.
- What the port 1443 interface covers beyond what its open clients use.

## Sources

- [Sonos communication](https://sonos.svrooij.io/sonos-communication) and
  [the services](https://sonos.svrooij.io/services/): discovery, SOAP on 1400, the
  service list.
- [Sonos, adjust connection security settings](https://support.sonos.com/en/article/adjust-connection-security-settings):
  the UPnP and Authentication settings and their defaults.
- [Home Assistant, Sonos](https://www.home-assistant.io/integrations/sonos/): the 403 a
  speaker answers with the UPnP setting off, and where the setting is in the app.
- Sonos developer documentation: [control](https://docs.sonos.com/docs/control),
  [authorize](https://docs.sonos.com/docs/authorize).
- [sonoscli](https://github.com/steipete/sonoscli): a Go command on the UPnP API.
- [SoCo](https://github.com/SoCo/SoCo): the long-standing Python library on it.
- [sonos-websocket](https://github.com/jjlawren/sonos-websocket): the port 1443
  interface in use.
