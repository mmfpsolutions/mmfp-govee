/*
 * Copyright 2026 Scott Walter, MMFP Solutions LLC
 *
 * This program is free software; you can redistribute it and/or modify it
 * under the terms of the GNU General Public License as published by the Free
 * Software Foundation; either version 3 of the License, or (at your option)
 * any later version.  See LICENSE for more details.
 */

package govee

import (
	"context"
	"encoding/json"
	"net"
	"testing"
	"time"

	"github.com/mmfpsolutions/mmfp-govee/internal/logger"
)

// fakeDevice stands in for a Govee lamp on the LAN: it listens on an
// ephemeral "control port" and answers devStatus, exactly like the real
// protocol — replies go to the SENDER'S :4002 listener, not to the source
// port (that quirk is why lanService keys waiters by IP).
type fakeDevice struct {
	conn     *net.UDPConn
	replyTo  *net.UDPAddr
	t        *testing.T
	received chan lanEnvelope
	silent   bool // simulate a device that stopped answering
}

func newFakeDevice(t *testing.T, replyPort int) *fakeDevice {
	t.Helper()
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatalf("fake device listen: %v", err)
	}
	d := &fakeDevice{
		conn:     conn,
		replyTo:  &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: replyPort},
		t:        t,
		received: make(chan lanEnvelope, 16),
	}
	go d.serve()
	return d
}

func (d *fakeDevice) port() int { return d.conn.LocalAddr().(*net.UDPAddr).Port }

func (d *fakeDevice) serve() {
	buf := make([]byte, 2048)
	for {
		n, _, err := d.conn.ReadFromUDP(buf)
		if err != nil {
			return
		}
		var env lanEnvelope
		if json.Unmarshal(buf[:n], &env) != nil {
			continue
		}
		select {
		case d.received <- env:
		default:
		}
		if env.Msg.Cmd == "devStatus" && !d.silent {
			reply := []byte(`{"msg":{"cmd":"devStatus","data":{"onOff":1,"brightness":42,` +
				`"color":{"r":0,"g":255,"b":0},"colorTemInKelvin":0}}}`)
			d.conn.WriteToUDP(reply, d.replyTo)
		}
	}
}

func (d *fakeDevice) close() { d.conn.Close() }

// newTestLANService binds an ephemeral listener instead of the real :4002 so
// tests never collide with a running app or each other.
func newTestLANService(t *testing.T) *lanService {
	t.Helper()
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &lanService{
		log:         logger.New(logger.ModuleGovee),
		conn:        conn,
		controlPort: lanControlPort,
		routes:      make(map[string]LANRoute),
		waiters:     make(map[string][]chan lanStatusData),
		ctx:         ctx,
		cancel:      cancel,
	}
	s.wg.Add(1)
	go s.receiveLoop()
	t.Cleanup(s.Stop)
	return s
}

func (s *lanService) listenPort() int { return s.conn.LocalAddr().(*net.UDPAddr).Port }

func TestLAN_ScanReplyRegistersRoute(t *testing.T) {
	s := newTestLANService(t)

	// Simulate a device's unicast scan reply arriving on the listener.
	reply := []byte(`{"msg":{"cmd":"scan","data":{"ip":"192.168.7.75",` +
		`"device":"34:FD:CC:44:A9:3A:60:AC","sku":"H607C"}}}`)
	tx, err := net.DialUDP("udp4", nil, s.conn.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Close()
	tx.Write(reply)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if r, ok := s.Route("34:FD:CC:44:A9:3A:60:AC"); ok {
			if r.IP != "192.168.7.75" || r.SKU != "H607C" {
				t.Errorf("route = %+v, want IP 192.168.7.75 / H607C", r)
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("scan reply never registered a route")
}

func TestLAN_StatusRoundTrip(t *testing.T) {
	s := newTestLANService(t)
	dev := newFakeDevice(t, s.listenPort())
	defer dev.close()

	s.controlPort = dev.port()

	got, err := s.Status(context.Background(), "127.0.0.1")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if got.OnOff != 1 || got.Brightness != 42 || got.Color.G != 255 {
		t.Errorf("status = %+v, want onOff 1 / brightness 42 / green", got)
	}
}

func TestLAN_StatusTimeoutOnSilentDevice(t *testing.T) {
	s := newTestLANService(t)
	dev := newFakeDevice(t, s.listenPort())
	dev.silent = true // device stopped answering — the stale-route case
	defer dev.close()

	s.controlPort = dev.port()

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if _, err := s.Status(ctx, "127.0.0.1"); err == nil {
		t.Fatal("expected an error from a silent device")
	}
}

func TestLAN_DropRouteForgetsIt(t *testing.T) {
	s := newTestLANService(t)
	s.mu.Lock()
	s.routes["dev-1"] = LANRoute{IP: "192.168.1.5", SKU: "H607C"}
	s.mu.Unlock()

	s.dropRoute("dev-1")
	if _, ok := s.Route("dev-1"); ok {
		t.Error("route survived dropRoute")
	}
}

func TestLAN_ColorRGBSplitsChannels(t *testing.T) {
	s := newTestLANService(t)
	dev := newFakeDevice(t, s.listenPort())
	defer dev.close()

	s.controlPort = dev.port()
	if err := s.ColorRGB("127.0.0.1", 0xFFB000); err != nil {
		t.Fatal(err)
	}

	select {
	case env := <-dev.received:
		var data struct {
			Color struct{ R, G, B int } `json:"color"`
		}
		json.Unmarshal(env.Msg.Data, &data)
		if data.Color.R != 0xFF || data.Color.G != 0xB0 || data.Color.B != 0 {
			t.Errorf("color = %+v, want r=255 g=176 b=0", data.Color)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("device never received the command")
	}
}

func TestToInt(t *testing.T) {
	tests := []struct {
		in   interface{}
		want int
		ok   bool
	}{
		{42, 42, true},
		{float64(42), 42, true}, // JSON-decoded numbers
		{int64(42), 42, true},
		{"42", 0, false}, // struct/string values are cloud-only
		{map[string]int{}, 0, false},
	}
	for _, tt := range tests {
		got, ok := toInt(tt.in)
		if got != tt.want || ok != tt.ok {
			t.Errorf("toInt(%v) = (%d, %v), want (%d, %v)", tt.in, got, ok, tt.want, tt.ok)
		}
	}
}

// Static routes cover environments where discovery cannot run (Docker Desktop
// for Mac): multicast is dead there but unicast to a known IP works.
func TestLAN_StaticRoutesSeeded(t *testing.T) {
	s := newTestLANService(t)
	s.SetStaticRoutes([]StaticRoute{
		{Device: "34:FD:CC:44:A9:3A:60:AC", SKU: "H607C", IP: "192.168.7.75"},
		{Device: "", IP: "1.2.3.4"},          // ignored: no device id
		{Device: "no-ip", SKU: "H1", IP: ""}, // ignored: no IP
	})
	s.seedStatics()

	r, ok := s.Route("34:FD:CC:44:A9:3A:60:AC")
	if !ok {
		t.Fatal("static route was not seeded")
	}
	if r.IP != "192.168.7.75" || !r.Static {
		t.Errorf("route = %+v, want IP 192.168.7.75 and Static=true", r)
	}
	if len(s.Routes()) != 1 {
		t.Errorf("routes = %d, want 1 (malformed entries must be skipped)", len(s.Routes()))
	}
}

// A live discovery beats a config guess: the observed IP wins so a stale
// static entry cannot pin the app to a dead address.
func TestLAN_DiscoveryOverridesStatic(t *testing.T) {
	s := newTestLANService(t)
	s.mu.Lock()
	s.routes["dev-x"] = LANRoute{IP: "192.168.7.99", SKU: "H607C"} // discovered
	s.mu.Unlock()

	s.SetStaticRoutes([]StaticRoute{{Device: "dev-x", SKU: "H607C", IP: "10.0.0.1"}})
	s.seedStatics()

	r, _ := s.Route("dev-x")
	if r.IP != "192.168.7.99" || r.Static {
		t.Errorf("route = %+v, want the discovered 192.168.7.99 to win", r)
	}
}

// Self-heal must NOT re-arm a dead static route: otherwise every effect pays a
// read-back timeout forever. Only startup and manual re-scan re-seed.
func TestLAN_DropRouteDoesNotReseedStatics(t *testing.T) {
	s := newTestLANService(t)
	s.SetStaticRoutes([]StaticRoute{{Device: "dev-static", SKU: "H1", IP: "192.168.1.9"}})
	s.seedStatics()
	if _, ok := s.Route("dev-static"); !ok {
		t.Fatal("precondition: static route should be seeded")
	}

	s.dropRoute("dev-static") // stale → also triggers a self-heal Scan()
	if _, ok := s.Route("dev-static"); ok {
		t.Error("dead static route was re-armed by the self-heal scan — it must stay on cloud until a manual re-scan")
	}
}
