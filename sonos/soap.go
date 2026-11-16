// Package sonos controls Sonos speakers on the LAN, over the UPnP services
// every speaker serves on port 1400: which speakers there are and how they
// are grouped, what a group is playing, play and pause, volume, and moving
// a speaker in and out of a group. No account and no cloud.
//
// Sonos does not document these services and calls the protocol
// unsupported. The contract here is the community's documentation,
// https://sonos.svrooij.io, generated from the speakers' own service
// descriptions; docs/design.md says what was read there.
package sonos

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
)

// Port is the port a speaker serves its services on.
const Port = "1400"

// maxAnswer bounds one answer read; the zone group state of a large
// household is some tens of kilobytes.
const maxAnswer = 4 << 20

// The services called, each by its type and the path of its control URL.
const (
	transportType = "urn:schemas-upnp-org:service:AVTransport:1"
	transportPath = "/MediaRenderer/AVTransport/Control"

	renderingType = "urn:schemas-upnp-org:service:RenderingControl:1"
	renderingPath = "/MediaRenderer/RenderingControl/Control"

	groupRenderingType = "urn:schemas-upnp-org:service:GroupRenderingControl:1"
	groupRenderingPath = "/MediaRenderer/GroupRenderingControl/Control"

	topologyType = "urn:schemas-upnp-org:service:ZoneGroupTopology:1"
	topologyPath = "/ZoneGroupTopology/Control"
)

// The errors, to test with errors.Is.
var (
	// ErrRefused: the speaker answered a UPnP fault; the error carries its
	// code.
	ErrRefused = errors.New("the speaker refused the action")
	// ErrForbidden: the speaker answered 403. Home Assistant's page gives
	// that as the answer of a system with the UPnP setting off in the Sonos
	// app; docs/design.md has the source.
	ErrForbidden = errors.New("the speaker forbids UPnP control")
	// ErrAnswer: the answer is not SOAP in the documented shape, or is
	// larger than an answer may be.
	ErrAnswer = errors.New("the speaker's answer is not in the expected shape")
	// ErrArgument: a value that cannot be sent; nothing was.
	ErrArgument = errors.New("not a valid argument")
)

// argument is one argument of an action; a speaker reads them in order.
type argument struct {
	name  string
	value string
}

// instance is the argument every transport and rendering action starts
// with: the documentation gives 0 as its only value.
func instance() argument { return argument{"InstanceID", "0"} }

// master is the channel a volume or a mute is read and set on: the
// speaker as a whole.
func master() argument { return argument{"Channel", "Master"} }

// call invokes action on the service of serviceType at path on host, and
// returns the answer's elements by name.
func call(
	ctx context.Context,
	client *http.Client,
	host, serviceType, path, action string,
	args ...argument,
) (map[string]string, error) {
	var body bytes.Buffer

	body.WriteString(`<?xml version="1.0" encoding="utf-8"?>` +
		`<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/" ` +
		`s:encodingStyle="http://schemas.xmlsoap.org/soap/encoding/"><s:Body>` +
		`<u:` + action + ` xmlns:u="` + serviceType + `">`)

	for _, arg := range args {
		body.WriteString("<" + arg.name + ">")

		if err := xml.EscapeText(&body, []byte(arg.value)); err != nil {
			return nil, fmt.Errorf("%w: %s of %s: %w", ErrArgument, arg.name, action, err)
		}

		body.WriteString("</" + arg.name + ">")
	}

	body.WriteString(`</u:` + action + `></s:Body></s:Envelope>`)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+host+path, &body)
	if err != nil {
		return nil, fmt.Errorf("%w: %s on %s: %w", ErrArgument, action, host, err)
	}

	req.Header.Set("Content-Type", `text/xml; charset="utf-8"`)
	req.Header.Set("SoapAction", `"`+serviceType+"#"+action+`"`)

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s on %s: %w", action, host, err)
	}

	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxAnswer+1))
	if err != nil {
		return nil, fmt.Errorf("%s on %s: reading the answer: %w", action, host, err)
	}

	if len(raw) > maxAnswer {
		return nil, fmt.Errorf("%w: %s on %s: over %d bytes", ErrAnswer, action, host, maxAnswer)
	}

	if resp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("%w: %s on %s", ErrForbidden, action, host)
	}

	answer, fault, err := elements(raw)

	switch {
	case err == nil && fault:
		return nil, fmt.Errorf("%w: %s on %s: UPnP error %s", ErrRefused, action, host, answer["errorCode"])
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("%w: %s on %s: HTTP %d", ErrRefused, action, host, resp.StatusCode)
	case err != nil:
		return nil, fmt.Errorf("%w: %s on %s: %w", ErrAnswer, action, host, err)
	}

	return answer, nil
}

// A SOAP answer as far as it is read: the one element of the body, the
// action's response or a fault, and the elements it holds.
type (
	soapAnswer struct {
		Body soapBody `xml:"Body"`
	}
	soapBody struct {
		Response soapResponse `xml:",any"`
	}
	soapResponse struct {
		XMLName xml.Name `xml:""`
		// Code is a fault's UPnP error code.
		Code   string      `xml:"detail>UPnPError>errorCode"`
		Fields []soapField `xml:",any"`
	}
	soapField struct {
		XMLName xml.Name `xml:""`
		Value   string   `xml:",chardata"`
	}
)

// elements reads a SOAP answer: the text of every element of the action's
// response, by name, and whether the response is a fault. A fault's UPnP
// code is the element errorCode.
func elements(raw []byte) (map[string]string, bool, error) {
	var answer soapAnswer
	if err := xml.Unmarshal(raw, &answer); err != nil {
		return nil, false, fmt.Errorf("not XML: %w", err)
	}

	response := answer.Body.Response
	if response.XMLName.Local == "" {
		return nil, false, fmt.Errorf("%w: no SOAP body", ErrAnswer)
	}

	found := map[string]string{"errorCode": response.Code}
	for _, field := range response.Fields {
		found[field.XMLName.Local] = field.Value
	}

	return found, response.XMLName.Local == "Fault", nil
}

// address is host with the speakers' port when it carries none.
func address(host string) (string, error) {
	if host == "" {
		return "", fmt.Errorf("%w: an empty host", ErrArgument)
	}

	if _, _, err := net.SplitHostPort(host); err == nil {
		return host, nil
	}

	return net.JoinHostPort(strings.Trim(host, "[]"), Port), nil
}
