package sonos_test

// The contract of finding a speaker, from the community's documentation
// (https://sonos.svrooij.io/sonos-communication): a speaker answers an SSDP
// search for urn:schemas-upnp-org:device:ZonePlayer:1. SSDP names what
// answers by its USN, the device's UDN, "::" and what it advertises; a
// speaker's UDN is "uuid:" and the id the household gives it.

import (
	"context"
	"errors"
	"net/netip"
	"reflect"
	"testing"

	"github.com/duncanite/go-sonos/sonos"
)

// TestRecognize: an advertisement of a speaker's device type, at its
// version or a later one, is a speaker's, by the id in its UDN and at the
// places that have a URL; anything else is none.
func TestRecognize(t *testing.T) {
	t.Parallel()

	here := netip.MustParseAddr("192.0.2.5")
	place := sonos.Location{URL: "http://192.0.2.10:1400/xml/device_description.xml", Local: here}

	for _, row := range []struct {
		name string
		usn  string
		at   []sonos.Location
		want sonos.Found
		is   bool
	}{
		{
			"a speaker", "uuid:RINCON_000000000001" + "01400::" + sonos.SearchTarget,
			[]sonos.Location{place},
			sonos.Found{ID: "RINCON_00000000000101400", Locations: []sonos.Location{place}},
			true,
		},
		{
			"a later version", "uuid:RINCON_A::urn:schemas-upnp-org:device:ZonePlayer:2", nil,
			sonos.Found{ID: "RINCON_A", Locations: []sonos.Location{}},
			true,
		},
		{
			"a place with no URL is left out", "uuid:RINCON_A::" + sonos.SearchTarget,
			[]sonos.Location{{Local: here}, place},
			sonos.Found{ID: "RINCON_A", Locations: []sonos.Location{place}},
			true,
		},
		{"the speaker as a root device", "uuid:RINCON_A::upnp:rootdevice", nil, sonos.Found{}, false},
		{"the speaker by its UDN alone", "uuid:RINCON_A", nil, sonos.Found{}, false},
		{"one of its services", "uuid:RINCON_A::urn:schemas-upnp-org:service:AVTransport:1", nil, sonos.Found{}, false},
		{"its media renderer", "uuid:RINCON_A_MR::urn:schemas-upnp-org:device:MediaRenderer:1", nil, sonos.Found{}, false},
		{"another maker's device", "uuid:Socket-1_0-A::urn:Belkin:device:controllee:1", nil, sonos.Found{}, false},
		{"a version that is none", "uuid:RINCON_A::urn:schemas-upnp-org:device:ZonePlayer:0", nil, sonos.Found{}, false},
		{"a version that is no number", "uuid:RINCON_A::urn:schemas-upnp-org:device:ZonePlayer:x", nil, sonos.Found{}, false},
		{"a type that only starts as a speaker's", "uuid:RINCON_A::" + sonos.SearchTarget + "0:1", nil, sonos.Found{}, false},
		{"no UDN", "::" + sonos.SearchTarget, nil, sonos.Found{}, false},
		{"a UDN that is no uuid", "RINCON_A::" + sonos.SearchTarget, nil, sonos.Found{}, false},
		{"a uuid of nothing", "uuid:::" + sonos.SearchTarget, nil, sonos.Found{}, false},
		{"nothing", "", nil, sonos.Found{}, false},
	} {
		found, is := sonos.Recognize(sonos.Advertisement{USN: row.usn, Locations: row.at})
		if is != row.is || !reflect.DeepEqual(found, row.want) {
			t.Errorf("%s: Recognize = %+v, %t, want %+v, %t", row.name, found, is, row.want, row.is)
		}
	}
}

// errDeaf is what the browser of TestFind ends with.
var errDeaf = errors.New("the network hears no more")

// heard is a browser that tells what it was given, then ends.
type heard struct {
	target string
	tells  []sonos.Advertisement
	lost   []bool
}

func (h *heard) Browse(_ context.Context, target string, handle func(sonos.Advertisement, bool)) error {
	h.target = target

	for index, advertisement := range h.tells {
		handle(advertisement, h.lost[index])
	}

	return errDeaf
}

// TestFind: Find browses for the speakers' device type, tells of each
// advertisement that is a speaker's, found or lost, and of no other, and
// ends as the browser does; with no browser or no handler nothing is
// asked.
func TestFind(t *testing.T) {
	t.Parallel()

	place := sonos.Location{URL: "http://192.0.2.10:1400/xml/device_description.xml"}
	browser := &heard{
		tells: []sonos.Advertisement{
			{USN: "uuid:RINCON_A::" + sonos.SearchTarget, Locations: []sonos.Location{place}},
			{USN: "uuid:Socket-1_0-A::urn:Belkin:device:controllee:1"},
			{USN: "uuid:RINCON_A::" + sonos.SearchTarget, Locations: []sonos.Location{place}},
			{USN: "uuid:RINCON_A::" + sonos.SearchTarget},
		},
		lost: []bool{false, false, false, true},
	}

	type told struct {
		found sonos.Found
		lost  bool
	}

	var got []told

	err := sonos.Find(t.Context(), browser, func(found sonos.Found, lost bool) {
		got = append(got, told{found, lost})
	})

	want := []told{
		{sonos.Found{ID: "RINCON_A", Locations: []sonos.Location{place}}, false},
		{sonos.Found{ID: "RINCON_A", Locations: []sonos.Location{place}}, false},
		{sonos.Found{ID: "RINCON_A", Locations: []sonos.Location{}}, true},
	}

	if !errors.Is(err, errDeaf) || browser.target != sonos.SearchTarget || !reflect.DeepEqual(got, want) {
		t.Errorf("Find browsed for %q, told %+v and ended with %v", browser.target, got, err)
	}

	if err = sonos.Find(t.Context(), nil, func(sonos.Found, bool) {}); !errors.Is(err, sonos.ErrArgument) {
		t.Errorf("Find with no browser = %v, want ErrArgument", err)
	}

	unasked := &heard{}
	if err = sonos.Find(t.Context(), unasked, nil); !errors.Is(err, sonos.ErrArgument) || unasked.target != "" {
		t.Errorf("Find with no handler = %v, having browsed for %q", err, unasked.target)
	}
}
