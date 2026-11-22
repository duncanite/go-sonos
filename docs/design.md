# go-sonos: the design

Research into controlling Sonos speakers, done on 2026-11-15 from public sources, and the
module built from it. The module follows what was read; what was since run against
speakers is under [Observed on speakers](#observed-on-speakers).

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

- `sonos`: `Open` asks one speaker for the household and gives a `System`, its groups
  with their coordinators and rooms. On it: `Status`, `Play`, `Pause`, `Stop`, `Next`,
  `Previous` for a group; `Volume`, `SetVolume`, `Muted`, `SetMute` for a room;
  `GroupVolume` and `SetGroupVolume`; `Join` and `Leave`; `Household`, its identity;
  `Watch`, everything above told as it changes. `Recognize` and `Find` say what a speaker
  is on the network. Built on [go-upnp](https://github.com/forkcloser/go-upnp) for the
  actions and the subscriptions, and on the standard library.
- `sonos/sonostest`: a household for tests, one HTTP server per speaker, that refuses a
  request not shaped as the documentation gives it, and sends events.
- `cmd/sonos`: the command, over `internal/cli`. It is where the sockets are: the SSDP
  search, through go-upnp's `ssdp`, and the choice of where the events listener listens.

### What the library opens, and what it is lent

The library reads no file, writes no log, and opens no socket to find a speaker.

- **Requests** go through the caller's client, a `Doer`: an `*http.Client` is one.
- **Finding** goes through a `Browser` the caller lends, an interface of this package's
  own in standard types: SSDP's port is one to a host, and belongs to the program. `Find`
  is that browser's search for `SearchTarget` narrowed to what `Recognize` takes, and
  `Recognize` works on a value, for a caller that holds advertisements already.
- **Events** arrive at a listener the library does open, `Listen`, at the address the
  caller gives; each `Watch` is told the URL the speakers reach it at. Neither address is
  guessed: only the caller knows which of the host's addresses the speakers can reach.

### Decisions

- **The household comes from one speaker.** Any speaker gives the whole topology, so one
  is enough: one named by its address, since a search by multicast does not pass every
  network, or the first a search finds.
- **A speaker is who the household says it is.** Its `ID` is the id the zone group state
  gives it, `RINCON_...`, which is its UDN without `uuid:`: what an advertisement names
  it by is what the household names it by. An advertisement proves nothing, and a network
  may carry two households: `Household` asks a speaker which one it is of, with
  `GetHouseholdID` of the device properties service.
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
- **A `System` is the household at one moment.** Its groups follow no change; after a
  `Join` or a `Leave` it is read again, or watched.
- **`Watch` follows the household, not a speaker.** It subscribes to the topology on the
  speaker the `System` was opened on, to the transport of each group's coordinator and to
  the rendering control of each room, and tells the groups (`Regrouped`), what a group
  plays (`Played`) and how loud a room is (`Sounded`): first how each stands, then each
  change, whole. When the groups change it ends the subscriptions to speakers it follows
  no more and starts those it lacks. It ends with why and subscribes again to nothing by
  itself: a speaker that vanishes ends it with `ErrLost`, for all the household, and
  watching again starts from how things stand, and ends at once, with the client's error
  for a speaker it cannot reach, for as long as the household lists that speaker. It ends
  every subscription before it returns, one the speaker was still granting included, when
  the groups change twice at once or the `Watch` ends: go-upnp sees the asking through,
  then ends what was granted. So `Watch` returns once each speaker has answered, or has
  had its five seconds to, twice over when it had not yet said which subscription it
  granted.
- **An event's variables are never handed on as they came.** The topology's events carry
  more than the groups; SoCo redacts one of them, `ThirdPartyMediaServersX`, as holding
  account tokens. `Watch` reads the groups, the transport state and track, the volume and
  the mute, and gives those.
- **A transport or rendering event is a `LastChange` document**, as SoCo reads it: under
  an `InstanceID` element, one element per variable that changed, its value in `val`, or
  in the element's text when `val` is absent; a volume and a mute come once per channel,
  and `Master` is the speaker's. A change carries only what changed, so `Watch` keeps
  what each speaker said and tells the whole. A speaker's events say nothing of the
  position in a track; `Status` asks.
- **The command shows a speaker's text and never obeys it.** A room's name, a track's
  title and what a speaker says in refusing an action are whatever the speaker, or
  whoever answers for it, wrote. Every line the command prints goes through
  `internal/terminal`, which writes out, as Go writes them in a string, each character a
  terminal would not simply draw: a control or a direction mark, a line's end, a tab, a
  byte that is no text. A name with a line's end in it forges no line of the command's
  output. A test reads the command's source and fails on a line printed any other way,
  since no test of what is printed could name every line there will be.

### Not built

Favourites, the queue, the inputs and alarms. A `Watch` that survives a lost subscription
by itself. A speaker's own description is not read: nothing here needs it.

[sonoscli](https://github.com/steipete/sonoscli) is a Go command that does all of this,
active, with its code not importable; it is a reference. The Go libraries found are
unmaintained.

## Open questions

- Whether the system is on the S2 app, or has speakers that only run S1.
- Whether the UPnP setting is on, in the app under privacy and security.
- Whether the speakers and the machine that runs the command share a network, with
  multicast passing; without it, one speaker's address is enough.

## Observed on speakers

The command, run on 2026-11-21 against two speakers on Sonos software 98.0, each a room
playing alone, from a host on their network:

- Both answered the search for `urn:schemas-upnp-org:device:ZonePlayer:1`, and the id each
  advertised is the id the zone group state gives it: `discover` named each one's room.
- `GetZoneGroupState`, `GetTransportInfo`, `GetPositionInfo`, `GetVolume` and `GetMute`
  answered at the documented paths, and read as documented.
- `play` and `pause`, sent by the command, started and paused a room.
- Each took the subscriptions `watch` makes, to the topology on one and to the transport
  and the rendering control on both, and called back a listener on their own network. The
  first events carried the groups, each room's volume and mute, and each group's state and
  track. Playback started from the Sonos app, then a volume changed there, were each told and
  read.
- A speaker with nothing to play told a track whose title is a space, in its event, where
  `GetPositionInfo` gives none; `described` reads both as no title.

## Not verified

- Of the actions that change something, only `Play` and `Pause` were sent to a speaker by
  this module: not `Stop`, `Next`, `Previous`, `SetVolume`, `SetMute`, `SetGroupVolume`,
  `Join`, `Leave`. Nor was `GetHouseholdID` asked of one, nor a `Watch` run through a
  regrouping.
- What a speaker answers a transport action when it does not coordinate its group.
- Whether every model serves the services at the documented paths.
- The shape of a `LastChange` document was read in SoCo's reader of it, not in a
  speaker's own word.
- Whether a speaker asked from another network than its own takes a subscription.
- What a speaker's first event carries: the module tells what it has been told, and a
  first event without a volume would be told with a volume of 0.
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
- [SoCo](https://github.com/SoCo/SoCo): the long-standing Python library on it; its
  [reader of events](https://github.com/SoCo/SoCo/blob/0bcdf2d5a2c78dbb1acd1e433717e4416afe165f/soco/events_base.py) for
  the `LastChange` document and the variable it redacts.
- The services' pages for what each tells and where:
  [AVTransport](https://sonos.svrooij.io/services/av-transport),
  [RenderingControl](https://sonos.svrooij.io/services/rendering-control),
  [ZoneGroupTopology](https://sonos.svrooij.io/services/zone-group-topology),
  [DeviceProperties](https://sonos.svrooij.io/services/device-properties).
- [sonos-websocket](https://github.com/jjlawren/sonos-websocket): the port 1443
  interface in use.
