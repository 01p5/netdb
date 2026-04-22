// dhcp-probe sends a DHCPDISCOVER + DHCPREQUEST with a specified MAC
// address and prints the assigned IP. Pure-stdlib — no raw sockets, no BPF.
// Meant to be run inside the Kea container's network namespace via:
//
//     docker run --rm --network container:kea-dhcp4 \
//         -v $PWD/dhcp-probe:/probe alpine /probe -mac aa:bb:cc:dd:ee:ff
//
// Design: binds UDP/68 inside the netns, sends to 255.255.255.255:67 with
// the broadcast flag set, reads OFFER/ACK from its own socket. Kea running
// in the same netns listens on 0.0.0.0:67 and sees broadcasts delivered by
// the kernel, then replies to broadcast:68 which we pick up.
package main

import (
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"log"
	"math/rand/v2"
	"net"
	"os"
	"syscall"
	"time"
)

const (
	bootRequest = 1
	bootReply   = 2

	htypeEthernet = 1

	optMsgType  = 53
	optReqIP    = 50
	optServerID = 54
	optEnd      = 255

	msgDiscover = 1
	msgOffer    = 2
	msgRequest  = 3
	msgAck      = 5
	msgNak      = 6
)

var magicCookie = []byte{99, 130, 83, 99}

type packet struct {
	Op, HType, HLen, Hops byte
	XID                   uint32
	Secs, Flags           uint16
	CIAddr, YIAddr        net.IP // 4 bytes each
	SIAddr, GIAddr        net.IP
	CHAddr                net.HardwareAddr
	Options               map[byte][]byte
}

func (p *packet) MarshalBinary() []byte {
	buf := make([]byte, 240)
	buf[0] = p.Op
	buf[1] = p.HType
	buf[2] = p.HLen
	buf[3] = p.Hops
	binary.BigEndian.PutUint32(buf[4:8], p.XID)
	binary.BigEndian.PutUint16(buf[8:10], p.Secs)
	binary.BigEndian.PutUint16(buf[10:12], p.Flags)
	copy(buf[12:16], ensure4(p.CIAddr))
	copy(buf[16:20], ensure4(p.YIAddr))
	copy(buf[20:24], ensure4(p.SIAddr))
	copy(buf[24:28], ensure4(p.GIAddr))
	copy(buf[28:28+len(p.CHAddr)], p.CHAddr)
	// 64 byte sname + 128 byte file are already zeroed
	copy(buf[236:240], magicCookie)

	for code, val := range p.Options {
		buf = append(buf, code, byte(len(val)))
		buf = append(buf, val...)
	}
	buf = append(buf, optEnd)
	// DHCP pads to at least 300 bytes
	for len(buf) < 300 {
		buf = append(buf, 0)
	}
	return buf
}

func parse(raw []byte) (*packet, error) {
	if len(raw) < 240 {
		return nil, fmt.Errorf("short packet: %d", len(raw))
	}
	p := &packet{
		Op:      raw[0],
		HType:   raw[1],
		HLen:    raw[2],
		Hops:    raw[3],
		XID:     binary.BigEndian.Uint32(raw[4:8]),
		Secs:    binary.BigEndian.Uint16(raw[8:10]),
		Flags:   binary.BigEndian.Uint16(raw[10:12]),
		CIAddr:  net.IP(raw[12:16]),
		YIAddr:  net.IP(raw[16:20]),
		SIAddr:  net.IP(raw[20:24]),
		GIAddr:  net.IP(raw[24:28]),
		Options: map[byte][]byte{},
	}
	if int(p.HLen) > 0 && int(p.HLen) <= 16 {
		p.CHAddr = net.HardwareAddr(raw[28 : 28+int(p.HLen)])
	}
	// Validate magic cookie
	if raw[236] != 99 || raw[237] != 130 || raw[238] != 83 || raw[239] != 99 {
		return nil, errors.New("bad magic cookie")
	}
	// Walk options
	i := 240
	for i < len(raw) {
		code := raw[i]
		i++
		if code == 0 {
			continue
		}
		if code == optEnd {
			break
		}
		if i >= len(raw) {
			break
		}
		n := int(raw[i])
		i++
		if i+n > len(raw) {
			break
		}
		p.Options[code] = raw[i : i+n]
		i += n
	}
	return p, nil
}

func ensure4(ip net.IP) []byte {
	if ip == nil {
		return []byte{0, 0, 0, 0}
	}
	if v4 := ip.To4(); v4 != nil {
		return v4
	}
	return []byte{0, 0, 0, 0}
}

func opt(code byte, v ...byte) (byte, []byte) { return code, v }

// writeBroadcast sends buf to 255.255.255.255:67 using SO_BROADCAST.
// Using a separate unconnected socket so we can receive from whichever
// source Kea replies from.
func writeBroadcast(conn *net.UDPConn, buf []byte) error {
	addr := &net.UDPAddr{IP: net.IPv4bcast, Port: 67}
	_, err := conn.WriteToUDP(buf, addr)
	return err
}

func main() {
	macStr := flag.String("mac", "", "client MAC address (aa:bb:cc:dd:ee:ff)")
	giaddrStr := flag.String("giaddr", "",
		"relay-agent IP (giaddr). When set, probe acts as a relay: "+
			"binds UDP/<giaddr>:67, unicasts to -server, receives replies at giaddr:67. "+
			"This IP must already be configured locally.")
	serverStr := flag.String("server", "",
		"DHCP server IP to unicast to. Required when -giaddr is set.")
	timeout := flag.Duration("timeout", 5*time.Second, "receive timeout")
	flag.Parse()

	mac, err := net.ParseMAC(*macStr)
	if err != nil {
		log.Fatalf("parse mac: %v", err)
	}
	if len(mac) != 6 {
		log.Fatalf("need a 6-byte MAC")
	}

	var giaddr net.IP
	if *giaddrStr != "" {
		giaddr = net.ParseIP(*giaddrStr).To4()
		if giaddr == nil {
			log.Fatalf("bad -giaddr")
		}
		if *serverStr == "" {
			log.Fatalf("-server is required when -giaddr is set")
		}
	}

	// In relay mode, bind to giaddr:67 (so Kea's unicast reply lands here).
	// In client mode, bind to 0.0.0.0:68 with SO_BROADCAST.
	var lconn *net.UDPConn
	var sendTo *net.UDPAddr
	if giaddr != nil {
		lconn, err = net.ListenUDP("udp4", &net.UDPAddr{IP: giaddr, Port: 67})
		if err != nil {
			log.Fatalf("listen %s:67: %v", giaddr, err)
		}
		sendTo = &net.UDPAddr{IP: net.ParseIP(*serverStr).To4(), Port: 67}
	} else {
		lconn, err = net.ListenUDP("udp4", &net.UDPAddr{Port: 68})
		if err != nil {
			log.Fatalf("listen :68: %v", err)
		}
		sendTo = &net.UDPAddr{IP: net.IPv4bcast, Port: 67}
		// Enable SO_BROADCAST.
		raw, _ := lconn.SyscallConn()
		var sockErr error
		_ = raw.Control(func(fd uintptr) {
			sockErr = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_BROADCAST, 1)
		})
		if sockErr != nil {
			log.Fatalf("SO_BROADCAST: %v", sockErr)
		}
	}
	defer lconn.Close()

	xid := rand.Uint32()
	fmt.Fprintf(os.Stderr, "[probe] DISCOVER xid=%08x mac=%s giaddr=%v → %s\n",
		xid, mac, giaddr, sendTo)

	// broadcast flag only in client mode; relay mode uses unicast replies.
	var flags uint16
	if giaddr == nil {
		flags = 0x8000
	}

	discover := &packet{
		Op: bootRequest, HType: htypeEthernet, HLen: 6,
		XID:     xid,
		Flags:   flags,
		GIAddr:  giaddr,
		CHAddr:  mac,
		Options: map[byte][]byte{optMsgType: {msgDiscover}},
	}
	if _, err := lconn.WriteToUDP(discover.MarshalBinary(), sendTo); err != nil {
		log.Fatalf("send DISCOVER: %v", err)
	}

	offer, err := readMatching(lconn, xid, msgOffer, *timeout)
	if err != nil {
		log.Fatalf("OFFER: %v", err)
	}
	fmt.Fprintf(os.Stderr, "[probe] OFFER yiaddr=%s siaddr=%s\n", offer.YIAddr, offer.SIAddr)

	serverID := offer.Options[optServerID]
	if len(serverID) != 4 {
		log.Fatalf("no Server Identifier in OFFER")
	}
	reqIP := ensure4(offer.YIAddr)
	fmt.Fprintf(os.Stderr, "[probe] REQUEST ip=%s server=%v\n",
		net.IP(reqIP), net.IP(serverID))

	request := &packet{
		Op: bootRequest, HType: htypeEthernet, HLen: 6,
		XID:    xid,
		Flags:  flags,
		GIAddr: giaddr,
		CHAddr: mac,
		Options: map[byte][]byte{
			optMsgType:  {msgRequest},
			optReqIP:    reqIP,
			optServerID: serverID,
		},
	}
	if _, err := lconn.WriteToUDP(request.MarshalBinary(), sendTo); err != nil {
		log.Fatalf("send REQUEST: %v", err)
	}

	ack, err := readMatching(lconn, xid, msgAck, *timeout)
	if err != nil {
		log.Fatalf("ACK: %v", err)
	}
	fmt.Printf("lease acquired: ip=%s server=%v\n", ack.YIAddr, net.IP(serverID))
	// Dump the useful options Kea sent in the ACK so we can see what the
	// client would actually apply: router, DNS, netmask, domain-name, lease.
	printIP := func(name string, code byte) {
		if v, ok := ack.Options[code]; ok && len(v)%4 == 0 {
			var ips []string
			for i := 0; i < len(v); i += 4 {
				ips = append(ips, net.IP(v[i:i+4]).String())
			}
			fmt.Printf("  option %d %s = %s\n", code, name, joinCSV(ips))
		}
	}
	printIP("subnet-mask", 1)
	printIP("routers", 3)
	printIP("domain-name-servers", 6)
	if v, ok := ack.Options[15]; ok {
		fmt.Printf("  option 15 domain-name = %s\n", string(v))
	}
	if v, ok := ack.Options[51]; ok && len(v) == 4 {
		fmt.Printf("  option 51 lease-time = %d sec\n", binary.BigEndian.Uint32(v))
	}
}

func joinCSV(ss []string) string {
	out := ""
	for i, s := range ss {
		if i > 0 {
			out += ", "
		}
		out += s
	}
	return out
}

func readMatching(conn *net.UDPConn, xid uint32, wantType byte, timeout time.Duration) (*packet, error) {
	deadline := time.Now().Add(timeout)
	if err := conn.SetReadDeadline(deadline); err != nil {
		return nil, err
	}
	buf := make([]byte, 1500)
	for {
		n, _, err := conn.ReadFromUDP(buf)
		if err != nil {
			return nil, err
		}
		p, err := parse(buf[:n])
		if err != nil {
			continue
		}
		if p.Op != bootReply || p.XID != xid {
			continue
		}
		if t, ok := p.Options[optMsgType]; !ok || len(t) == 0 || t[0] != wantType {
			if ok && len(t) == 1 && t[0] == msgNak {
				return nil, fmt.Errorf("server sent NAK")
			}
			continue
		}
		return p, nil
	}
}
