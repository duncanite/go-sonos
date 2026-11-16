# go-sonos: the design

Research into controlling Sonos speakers, done on 2026-11-15 from public sources, to
decide what a Go module for them would be. Nothing is built and nothing here was run
against a speaker. The decisions at the end are open.

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

## What this module would be

Not decided. The proposal: **a client of the UPnP API**. Discovery by SSDP, or from one
speaker's address, since any speaker gives the whole topology; the topology as groups
and their coordinators; SOAP calls for transport, volume, grouping, favourites and
inputs; events through a local HTTP listener with its subscriptions renewed.

The shape is the one go-wemo already has for another UPnP device: an SSDP search, device
and service descriptions, a SOAP caller, an event listener.

[sonoscli](https://github.com/steipete/sonoscli) is a Go command that does all of this,
active, with its code not importable; it is a reference. The Go libraries found are
unmaintained.

## Open questions

- Whether the system is on the S2 app, or has speakers that only run S1.
- Whether the UPnP setting is on, in the app under privacy and security.
- Whether the speakers and the machine that runs the command share a network, with
  multicast passing; without it, one speaker's address is enough.

## Not verified

- Nothing above was observed on a speaker.
- The event subscription's details were not read in a primary source.
- Which software versions carry the UPnP setting: the page names none.
- What the port 1443 interface covers beyond what its open clients use.

## Sources

- [Sonos communication](https://sonos.svrooij.io/sonos-communication) and
  [the services](https://sonos.svrooij.io/services/): discovery, SOAP on 1400, the
  service list.
- [Sonos, adjust connection security settings](https://support.sonos.com/en/article/adjust-connection-security-settings):
  the UPnP and Authentication settings and their defaults.
- Sonos developer documentation: [control](https://docs.sonos.com/docs/control),
  [authorize](https://docs.sonos.com/docs/authorize).
- [sonoscli](https://github.com/steipete/sonoscli): a Go command on the UPnP API.
- [SoCo](https://github.com/SoCo/SoCo): the long-standing Python library on it.
- [sonos-websocket](https://github.com/jjlawren/sonos-websocket): the port 1443
  interface in use.
