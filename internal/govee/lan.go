/*
 * Copyright 2026 Scott Walter, MMFP Solutions LLC
 *
 * This program is free software; you can redistribute it and/or modify it
 * under the terms of the GNU General Public License as published by the Free
 * Software Foundation; either version 3 of the License, or (at your option)
 * any later version.  See LICENSE for more details.
 */

package govee

// Govee LAN Control — the fast path. See design-documents/lan-transport/.
//
// Protocol (verified live 2026-07-12): a scan goes to UDP multicast
// 239.255.255.250:4001; devices answer with UNICAST to :4002 (so no multicast
// group membership is needed — plain stdlib net is enough). Control and
// devStatus go unicast to device:4003, and devStatus answers land on :4002
// keyed only by source IP (the reply carries no device id).
//
// LAN speaks ONLY turn / brightness / colorwc / devStatus. No scenes, no
// segments, no music — those stay on the cloud client. LAN also reports LESS
// state than cloud, never more.
//
// Writes are fire-and-forget UDP with no ack: a send that "succeeds" proves
// nothing. That is why the engine verifies with a read-back (VerifyReachable
// on Client), which doubles as the stale-route health check.

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/mmfpsolutions/mmfp-govee/internal/logger"
)

const (
	lanMulticastIP = "239.255.255.250"
	lanScanPort    = 4001
	lanListenPort  = 4002
	lanControlPort = 4003

	// A LAN round-trip measured ~26ms; 2s is a generous ceiling before we
	// call the route stale and fall back to cloud.
	lanReplyTimeout = 2 * time.Second

	// How long a scan collects replies before the route set is considered
	// settled.
	lanScanWindow = 3 * time.Second
)

// lanStartupRetries are bounded re-scans after startup, then never again.
// This is not polling — it is finishing startup: after a power cut the host
// boots long before the lamps rejoin Wi-Fi, and without these every device
// would sit silently on cloud until a human clicked Refresh. Four multicast
// packets total.
var lanStartupRetries = []time.Duration{time.Minute, 5 * time.Minute, 15 * time.Minute}

// LANRoute is a LAN-reachable device — discovered by scan, or configured
// statically where discovery cannot run (see StaticRoute).
type LANRoute struct {
	IP       string    `json:"ip"`
	SKU      string    `json:"sku"`
	LastSeen time.Time `json:"lastSeen"`
	Static   bool      `json:"static,omitempty"` // from config, not discovery
}

// StaticRoute is a configured device→IP mapping for environments where
// multicast discovery cannot run (Docker Desktop for Mac). Discovery and
// transport fail independently: the scan may find nothing while unicast to a
// known IP works perfectly.
type StaticRoute struct {
	Device string
	SKU    string
	IP     string
}

// lanEnvelope is the wire frame for every LAN message.
type lanEnvelope struct {
	Msg struct {
		Cmd  string          `json:"cmd"`
		Data json.RawMessage `json:"data"`
	} `json:"msg"`
}

type lanScanData struct {
	IP     string `json:"ip"`
	Device string `json:"device"`
	SKU    string `json:"sku"`
}

// lanStatusData is the devStatus reply — exactly four fields. Note what is
// absent: scenes, toggles, segments. LAN buys speed, never visibility.
type lanStatusData struct {
	OnOff      int `json:"onOff"`
	Brightness int `json:"brightness"`
	Color      struct {
		R int `json:"r"`
		G int `json:"g"`
		B int `json:"b"`
	} `json:"color"`
	ColorTemInKelvin int `json:"colorTemInKelvin"`
}

// lanService owns the :4002 socket, the discovered routes, and the UDP ops.
type lanService struct {
	log  *logger.Logger
	conn *net.UDPConn

	// controlPort is lanControlPort in production; tests point it at a fake
	// device on an ephemeral port.
	controlPort int

	// statics are re-seeded on startup and manual re-scan ONLY — never on the
	// self-heal scan, so a dead static device drops to cloud and stays there
	// instead of re-arming and costing a read-back timeout on every effect.
	statics []StaticRoute

	mu       sync.RWMutex
	routes   map[string]LANRoute // device ID → route
	lastScan time.Time

	// devStatus replies carry no device id, so waiters are keyed by source IP.
	waitMu  sync.Mutex
	waiters map[string][]chan lanStatusData

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// newLANService binds :4002 and starts the receive loop. A bind failure is
// NOT fatal — LAN is a bonus; the caller logs and runs cloud-only.
func newLANService(log *logger.Logger) (*lanService, error) {
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{Port: lanListenPort})
	if err != nil {
		return nil, fmt.Errorf("bind udp :%d for LAN control: %w", lanListenPort, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &lanService{
		log:         log,
		conn:        conn,
		controlPort: lanControlPort,
		routes:      make(map[string]LANRoute),
		waiters:     make(map[string][]chan lanStatusData),
		ctx:         ctx,
		cancel:      cancel,
	}
	s.wg.Add(1)
	go s.receiveLoop()
	return s, nil
}

// SetStaticRoutes installs configured routes (applied on the next seed).
func (s *lanService) SetStaticRoutes(routes []StaticRoute) {
	s.statics = routes
}

// seedStatics installs configured routes. Discovery wins: a scan reply for
// the same device overwrites the static entry with the observed IP.
func (s *lanService) seedStatics() {
	if len(s.statics) == 0 {
		return
	}
	s.mu.Lock()
	for _, r := range s.statics {
		if r.Device == "" || r.IP == "" {
			continue
		}
		if _, discovered := s.routes[r.Device]; discovered {
			continue // a live discovery beats a config guess
		}
		s.routes[r.Device] = LANRoute{IP: r.IP, SKU: r.SKU, LastSeen: time.Now(), Static: true}
	}
	n := len(s.routes)
	s.mu.Unlock()
	s.log.Info("Seeded %d static LAN route(s); %d route(s) total", len(s.statics), n)
}

// Start seeds static routes, then runs the startup scan plus the bounded
// retry schedule.
func (s *lanService) Start() {
	s.seedStatics()
	s.Scan()
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		for _, d := range lanStartupRetries {
			select {
			case <-s.ctx.Done():
				return
			case <-time.After(d):
				s.Scan()
			}
		}
		s.log.Debug("LAN startup scan schedule complete; discovery is manual + self-heal from here")
	}()
}

// Stop closes the socket and waits for the loops to exit.
func (s *lanService) Stop() {
	s.cancel()
	s.conn.Close() // unblocks receiveLoop
	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		s.log.Warn("LAN service stop timed out")
	}
}

// Scan sends one multicast discovery packet. Replies arrive asynchronously on
// the receive loop, so this returns immediately; callers that need a settled
// route set should wait lanScanWindow.
func (s *lanService) Scan() {
	msg := []byte(`{"msg":{"cmd":"scan","data":{"account_topic":"reserve"}}}`)
	addr := &net.UDPAddr{IP: net.ParseIP(lanMulticastIP), Port: lanScanPort}
	if _, err := s.conn.WriteToUDP(msg, addr); err != nil {
		s.log.Debug("LAN scan send failed: %v", err)
		return
	}
	s.mu.Lock()
	s.lastScan = time.Now()
	s.mu.Unlock()
	s.log.Debug("LAN scan sent to %s:%d", lanMulticastIP, lanScanPort)
}

// ScanAndWait sends a scan and waits for the reply window to close, so the
// caller sees a settled route set (used by manual Refresh / Re-scan).
func (s *lanService) ScanAndWait(ctx context.Context) {
	s.Scan()
	select {
	case <-ctx.Done():
	case <-time.After(lanScanWindow):
	}
	// Manual re-scan re-arms static routes too — this is the operator's
	// explicit "try again", including for a device that had gone stale.
	s.seedStatics()
}

// receiveLoop demuxes everything arriving on :4002 — scan replies (which
// update routes) and devStatus replies (which go to waiters, keyed by IP).
func (s *lanService) receiveLoop() {
	defer s.wg.Done()
	buf := make([]byte, 4096)
	for {
		n, addr, err := s.conn.ReadFromUDP(buf)
		if err != nil {
			select {
			case <-s.ctx.Done():
				return // closed on Stop
			default:
			}
			s.log.Debug("LAN read error: %v", err)
			continue
		}
		var env lanEnvelope
		if err := json.Unmarshal(buf[:n], &env); err != nil {
			continue // not ours
		}
		switch env.Msg.Cmd {
		case "scan":
			s.handleScanReply(env.Msg.Data)
		case "devStatus":
			s.handleStatusReply(addr.IP.String(), env.Msg.Data)
		}
	}
}

func (s *lanService) handleScanReply(data json.RawMessage) {
	var d lanScanData
	if err := json.Unmarshal(data, &d); err != nil || d.Device == "" || d.IP == "" {
		return
	}
	s.mu.Lock()
	_, existed := s.routes[d.Device]
	s.routes[d.Device] = LANRoute{IP: d.IP, SKU: d.SKU, LastSeen: time.Now()}
	s.mu.Unlock()
	if !existed {
		s.log.Info("LAN device discovered: %s (%s) at %s", d.Device, d.SKU, d.IP)
	}
}

func (s *lanService) handleStatusReply(ip string, data json.RawMessage) {
	var d lanStatusData
	if err := json.Unmarshal(data, &d); err != nil {
		return
	}
	s.waitMu.Lock()
	chans := s.waiters[ip]
	delete(s.waiters, ip)
	s.waitMu.Unlock()
	for _, ch := range chans {
		select {
		case ch <- d:
		default:
		}
	}
}

// Route returns the LAN route for a device, if it has one.
func (s *lanService) Route(deviceID string) (LANRoute, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.routes[deviceID]
	return r, ok
}

// Routes returns a snapshot of all discovered routes.
func (s *lanService) Routes() map[string]LANRoute {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]LANRoute, len(s.routes))
	for k, v := range s.routes {
		out[k] = v
	}
	return out
}

// LastScan reports when a scan was last sent.
func (s *lanService) LastScan() time.Time {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lastScan
}

// dropRoute forgets a device's LAN route (it stopped answering) and kicks off
// a re-scan so it can come back on its own. This is the self-healing path —
// no polling timer needed, because the read-back we already do IS the health
// check.
func (s *lanService) dropRoute(deviceID string) {
	s.mu.Lock()
	r, existed := s.routes[deviceID]
	delete(s.routes, deviceID)
	s.mu.Unlock()
	if existed {
		s.log.Warn("LAN route for %s (%s) went stale — falling back to cloud and re-scanning", deviceID, r.IP)
		s.Scan()
	}
}

// send fires one fire-and-forget command at a device. A nil error means the
// packet left the host — NOT that the device got it or acted on it.
func (s *lanService) send(ip string, payload interface{}) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal LAN command: %w", err)
	}
	addr := &net.UDPAddr{IP: net.ParseIP(ip), Port: s.controlPort}
	if addr.IP == nil {
		return fmt.Errorf("invalid LAN device IP %q", ip)
	}
	if _, err := s.conn.WriteToUDP(data, addr); err != nil {
		return fmt.Errorf("send LAN command to %s: %w", ip, err)
	}
	return nil
}

func lanCmd(cmd string, data interface{}) map[string]interface{} {
	return map[string]interface{}{"msg": map[string]interface{}{"cmd": cmd, "data": data}}
}

// Turn powers a device on/off.
func (s *lanService) Turn(ip string, on bool) error {
	v := 0
	if on {
		v = 1
	}
	return s.send(ip, lanCmd("turn", map[string]int{"value": v}))
}

// Brightness sets 1-100.
func (s *lanService) Brightness(ip string, percent int) error {
	return s.send(ip, lanCmd("brightness", map[string]int{"value": percent}))
}

// ColorRGB sets a 24-bit RGB color (colorTemInKelvin 0 disables temp mode).
func (s *lanService) ColorRGB(ip string, rgb int) error {
	return s.send(ip, lanCmd("colorwc", map[string]interface{}{
		"color": map[string]int{
			"r": (rgb >> 16) & 0xFF,
			"g": (rgb >> 8) & 0xFF,
			"b": rgb & 0xFF,
		},
		"colorTemInKelvin": 0,
	}))
}

// ColorTemp sets color temperature in Kelvin.
func (s *lanService) ColorTemp(ip string, kelvin int) error {
	return s.send(ip, lanCmd("colorwc", map[string]interface{}{
		"colorTemInKelvin": kelvin,
	}))
}

// Status queries devStatus and waits for the reply. A timeout means the route
// is stale — the caller is expected to dropRoute and fall back to cloud.
func (s *lanService) Status(ctx context.Context, ip string) (*lanStatusData, error) {
	ch := make(chan lanStatusData, 1)
	s.waitMu.Lock()
	s.waiters[ip] = append(s.waiters[ip], ch)
	s.waitMu.Unlock()

	cleanup := func() {
		s.waitMu.Lock()
		remaining := s.waiters[ip][:0]
		for _, c := range s.waiters[ip] {
			if c != ch {
				remaining = append(remaining, c)
			}
		}
		if len(remaining) == 0 {
			delete(s.waiters, ip)
		} else {
			s.waiters[ip] = remaining
		}
		s.waitMu.Unlock()
	}

	if err := s.send(ip, lanCmd("devStatus", map[string]interface{}{})); err != nil {
		cleanup()
		return nil, err
	}

	select {
	case d := <-ch:
		return &d, nil
	case <-ctx.Done():
		cleanup()
		return nil, ctx.Err()
	case <-time.After(lanReplyTimeout):
		cleanup()
		return nil, fmt.Errorf("no devStatus reply from %s within %s", ip, lanReplyTimeout)
	}
}
