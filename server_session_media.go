package gortsplib

import (
	"fmt"
	"net"
	"sync/atomic"
	"time"

	"github.com/pion/rtcp"
	"github.com/pion/rtp"

	"github.com/pipe01/gortsplib/v3/pkg/base"
	"github.com/pipe01/gortsplib/v3/pkg/media"
)

type serverSessionMedia struct {
	ss                     *ServerSession
	media                  *media.Media
	tcpChannel             int
	udpRTPReadPort         int
	udpRTPWriteAddr        *net.UDPAddr
	udpRTCPReadPort        int
	udpRTCPWriteAddr       *net.UDPAddr
	tcpRTPFrame            *base.InterleavedFrame
	tcpRTCPFrame           *base.InterleavedFrame
	tcpBuffer              []byte
	formats                map[uint8]*serverSessionFormat // record only
	writePacketRTPInQueue  func([]byte)
	writePacketRTCPInQueue func([]byte)
	readRTP                func([]byte) error
	readRTCP               func([]byte) error
	onPacketRTCP           func(rtcp.Packet)
}

func newServerSessionMedia(ss *ServerSession, medi *media.Media) *serverSessionMedia {
	sm := &serverSessionMedia{
		ss:           ss,
		media:        medi,
		onPacketRTCP: func(rtcp.Packet) {},
	}

	if ss.state == ServerSessionStatePreRecord {
		sm.formats = make(map[uint8]*serverSessionFormat)
		for _, forma := range medi.Formats {
			sm.formats[forma.PayloadType()] = newServerSessionFormat(sm, forma)
		}
	}

	return sm
}

func (sm *serverSessionMedia) start() {
	// allocate udpRTCPReceiver before udpRTCPListener
	// otherwise udpRTCPReceiver.LastSSRC() can't be called.
	for _, sf := range sm.formats {
		sf.start()
	}

	switch *sm.ss.setuppedTransport {
	case TransportUDP, TransportUDPMulticast:
		sm.writePacketRTPInQueue = sm.writePacketRTPInQueueUDP
		sm.writePacketRTCPInQueue = sm.writePacketRTCPInQueueUDP

		if sm.ss.state == ServerSessionStatePlay {
			sm.readRTCP = sm.readRTCPUDPPlay
		} else {
			sm.readRTP = sm.readRTPUDPRecord
			sm.readRTCP = sm.readRTCPUDPRecord
		}

	case TransportTCP:
		sm.writePacketRTPInQueue = sm.writePacketRTPInQueueTCP
		sm.writePacketRTCPInQueue = sm.writePacketRTCPInQueueTCP

		if sm.ss.state == ServerSessionStatePlay {
			sm.readRTP = sm.readRTPTCPPlay
			sm.readRTCP = sm.readRTCPTCPPlay
		} else {
			sm.readRTP = sm.readRTPTCPRecord
			sm.readRTCP = sm.readRTCPTCPRecord
		}

		sm.tcpRTPFrame = &base.InterleavedFrame{Channel: sm.tcpChannel}
		sm.tcpRTCPFrame = &base.InterleavedFrame{Channel: sm.tcpChannel + 1}
		sm.tcpBuffer = make([]byte, maxPacketSize+4)
	}

	if *sm.ss.setuppedTransport == TransportUDP {
		if sm.ss.state == ServerSessionStatePlay {
			// firewall opening is performed with RTCP sender reports generated by ServerStream

			// readers can send RTCP packets only
			sm.ss.s.udpRTCPListener.addClient(sm.ss.author.ip(), sm.udpRTCPReadPort, sm)
		} else {
			// open the firewall by sending test packets to the counterpart.
			sm.ss.WritePacketRTP(sm.media, &rtp.Packet{Header: rtp.Header{Version: 2}})
			sm.ss.WritePacketRTCP(sm.media, &rtcp.ReceiverReport{})

			sm.ss.s.udpRTPListener.addClient(sm.ss.author.ip(), sm.udpRTPReadPort, sm)
			sm.ss.s.udpRTCPListener.addClient(sm.ss.author.ip(), sm.udpRTCPReadPort, sm)
		}
	}
}

func (sm *serverSessionMedia) stop() {
	if *sm.ss.setuppedTransport == TransportUDP {
		sm.ss.s.udpRTPListener.removeClient(sm)
		sm.ss.s.udpRTCPListener.removeClient(sm)
	}

	for _, sf := range sm.formats {
		sf.stop()
	}
}

func (sm *serverSessionMedia) writePacketRTPInQueueUDP(payload []byte) {
	atomic.AddUint64(sm.ss.bytesSent, uint64(len(payload)))
	sm.ss.s.udpRTPListener.write(payload, sm.udpRTPWriteAddr)
}

func (sm *serverSessionMedia) writePacketRTCPInQueueUDP(payload []byte) {
	atomic.AddUint64(sm.ss.bytesSent, uint64(len(payload)))
	sm.ss.s.udpRTCPListener.write(payload, sm.udpRTCPWriteAddr)
}

func (sm *serverSessionMedia) writePacketRTPInQueueTCP(payload []byte) {
	atomic.AddUint64(sm.ss.bytesSent, uint64(len(payload)))
	sm.tcpRTPFrame.Payload = payload
	sm.ss.tcpConn.nconn.SetWriteDeadline(time.Now().Add(sm.ss.s.WriteTimeout))
	sm.ss.tcpConn.conn.WriteInterleavedFrame(sm.tcpRTPFrame, sm.tcpBuffer)
}

func (sm *serverSessionMedia) writePacketRTCPInQueueTCP(payload []byte) {
	atomic.AddUint64(sm.ss.bytesSent, uint64(len(payload)))
	sm.tcpRTCPFrame.Payload = payload
	sm.ss.tcpConn.nconn.SetWriteDeadline(time.Now().Add(sm.ss.s.WriteTimeout))
	sm.ss.tcpConn.conn.WriteInterleavedFrame(sm.tcpRTCPFrame, sm.tcpBuffer)
}

func (sm *serverSessionMedia) writePacketRTP(payload []byte) {
	sm.ss.writer.queue(func() {
		sm.writePacketRTPInQueue(payload)
	})
}

func (sm *serverSessionMedia) writePacketRTCP(payload []byte) {
	sm.ss.writer.queue(func() {
		sm.writePacketRTCPInQueue(payload)
	})
}

func (sm *serverSessionMedia) readRTCPUDPPlay(payload []byte) error {
	plen := len(payload)

	atomic.AddUint64(sm.ss.bytesReceived, uint64(plen))

	if plen == (maxPacketSize + 1) {
		onWarning(sm.ss, fmt.Errorf("RTCP packet is too big to be read with UDP"))
		return nil
	}

	packets, err := rtcp.Unmarshal(payload)
	if err != nil {
		onWarning(sm.ss, err)
		return nil
	}

	now := time.Now()
	atomic.StoreInt64(sm.ss.udpLastPacketTime, now.Unix())

	for _, pkt := range packets {
		sm.onPacketRTCP(pkt)
	}

	return nil
}

func (sm *serverSessionMedia) readRTPUDPRecord(payload []byte) error {
	plen := len(payload)

	atomic.AddUint64(sm.ss.bytesReceived, uint64(plen))

	if plen == (maxPacketSize + 1) {
		onWarning(sm.ss, fmt.Errorf("RTP packet is too big to be read with UDP"))
		return nil
	}

	pkt := &rtp.Packet{}
	err := pkt.Unmarshal(payload)
	if err != nil {
		onWarning(sm.ss, err)
		return nil
	}

	forma, ok := sm.formats[pkt.PayloadType]
	if !ok {
		onWarning(sm.ss, fmt.Errorf("received RTP packet with unknown payload type (%d)", pkt.PayloadType))
		return nil
	}

	now := time.Now()
	atomic.StoreInt64(sm.ss.udpLastPacketTime, now.Unix())

	forma.readRTPUDP(pkt, now)
	return nil
}

func (sm *serverSessionMedia) readRTCPUDPRecord(payload []byte) error {
	plen := len(payload)

	atomic.AddUint64(sm.ss.bytesReceived, uint64(plen))

	if plen == (maxPacketSize + 1) {
		onWarning(sm.ss, fmt.Errorf("RTCP packet is too big to be read with UDP"))
		return nil
	}

	packets, err := rtcp.Unmarshal(payload)
	if err != nil {
		onWarning(sm.ss, err)
		return nil
	}

	now := time.Now()
	atomic.StoreInt64(sm.ss.udpLastPacketTime, now.Unix())

	for _, pkt := range packets {
		if sr, ok := pkt.(*rtcp.SenderReport); ok {
			format := serverFindFormatWithSSRC(sm.formats, sr.SSRC)
			if format != nil {
				format.udpRTCPReceiver.ProcessSenderReport(sr, now)
			}
		}
	}

	for _, pkt := range packets {
		sm.onPacketRTCP(pkt)
	}

	return nil
}

func (sm *serverSessionMedia) readRTPTCPPlay(payload []byte) error {
	return nil
}

func (sm *serverSessionMedia) readRTCPTCPPlay(payload []byte) error {
	if len(payload) > maxPacketSize {
		onWarning(sm.ss, fmt.Errorf("RTCP packet size (%d) is greater than maximum allowed (%d)",
			len(payload), maxPacketSize))
		return nil
	}

	packets, err := rtcp.Unmarshal(payload)
	if err != nil {
		onWarning(sm.ss, err)
		return nil
	}

	for _, pkt := range packets {
		sm.onPacketRTCP(pkt)
	}

	return nil
}

func (sm *serverSessionMedia) readRTPTCPRecord(payload []byte) error {
	pkt := &rtp.Packet{}
	err := pkt.Unmarshal(payload)
	if err != nil {
		return err
	}

	forma, ok := sm.formats[pkt.PayloadType]
	if !ok {
		onWarning(sm.ss, fmt.Errorf("received RTP packet with unknown payload type (%d)", pkt.PayloadType))
		return nil
	}

	forma.readRTPTCP(pkt)
	return nil
}

func (sm *serverSessionMedia) readRTCPTCPRecord(payload []byte) error {
	if len(payload) > maxPacketSize {
		onWarning(sm.ss, fmt.Errorf("RTCP packet size (%d) is greater than maximum allowed (%d)",
			len(payload), maxPacketSize))
		return nil
	}

	packets, err := rtcp.Unmarshal(payload)
	if err != nil {
		onWarning(sm.ss, err)
		return nil
	}

	for _, pkt := range packets {
		sm.onPacketRTCP(pkt)
	}

	return nil
}
