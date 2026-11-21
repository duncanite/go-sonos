# go-sonos

A Go library and command for Sonos speakers, on the LAN, over the UPnP services every
speaker serves on port 1400: the rooms and their groups, what plays, play and pause,
volume, moving a room in and out of a group, and all of it told as it changes. No account
and no cloud. Written against the community's documentation of those services:
[docs/design.md](docs/design.md) says what was read.

The command asks any one speaker, which names the others: the one at the address in
`--host` or `SONOS_HOST`, or, with neither, the first that answers a search of this
network.

```bash
just sonos discover                 # every speaker on this network, and the room it is
just sonos watch                    # what the household does, as it happens
just sonos rooms                    # every room: who it plays with, what, how loud
just sonos status Kitchen           # what the room's group plays
just sonos pause Kitchen            # also play, stop, next, previous
just sonos volume Kitchen           # print the room's volume
just sonos volume Kitchen 25        # set it, 0 to 100
just sonos volume Kitchen 25 --group    # the whole group, each room keeping its share
just sonos mute Kitchen             # and unmute
just sonos join Kitchen "Living Room"   # play with the other room's group
just sonos leave Kitchen            # play alone again
```

`watch` has the speakers call this host back: it listens at this host's address on their
network and a free port, or at `--callback host:port`, and a firewall has to let them in.

A room's name is matched whatever its case. Sonos calls this protocol unsupported, and its
app has a UPnP setting, on by default, that turns it off: by
[Home Assistant's account](https://www.home-assistant.io/integrations/sonos/) a speaker then
answers 403, and the command says where the setting is.

[docs/design.md](docs/design.md) is the research the module starts from, what the speakers
offer and the ways into them, and the shape of the module with its decisions.
