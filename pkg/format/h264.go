package format

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/pion/rtp"

	"github.com/pipe01/gortsplib/v3/pkg/codecs/h264"
	"github.com/pipe01/gortsplib/v3/pkg/formatdecenc/rtph264"
)

// check whether a RTP/H264 packet contains a IDR, without decoding the packet.
func rtpH264ContainsIDR(pkt *rtp.Packet) bool {
	if len(pkt.Payload) == 0 {
		return false
	}

	typ := h264.NALUType(pkt.Payload[0] & 0x1F)

	switch typ {
	case h264.NALUTypeIDR:
		return true

	case 24: // STAP-A
		payload := pkt.Payload[1:]

		for len(payload) > 0 {
			if len(payload) < 2 {
				return false
			}

			size := uint16(payload[0])<<8 | uint16(payload[1])
			payload = payload[2:]

			if size == 0 || int(size) > len(payload) {
				return false
			}

			nalu := payload[:size]
			payload = payload[size:]

			typ = h264.NALUType(nalu[0] & 0x1F)
			if typ == h264.NALUTypeIDR {
				return true
			}
		}

		return false

	case 28: // FU-A
		if len(pkt.Payload) < 2 {
			return false
		}

		start := pkt.Payload[1] >> 7
		if start != 1 {
			return false
		}

		typ := h264.NALUType(pkt.Payload[1] & 0x1F)
		return (typ == h264.NALUTypeIDR)

	default:
		return false
	}
}

// H264 is a format that uses the H264 codec.
type H264 struct {
	PayloadTyp        uint8
	SPS               []byte
	PPS               []byte
	PacketizationMode int

	mutex sync.RWMutex
}

// String implements Format.
func (t *H264) String() string {
	return "H264"
}

// ClockRate implements Format.
func (t *H264) ClockRate() int {
	return 90000
}

// PayloadType implements Format.
func (t *H264) PayloadType() uint8 {
	return t.PayloadTyp
}

func (t *H264) unmarshal(payloadType uint8, clock string, codec string, rtpmap string, fmtp map[string]string) error {
	t.PayloadTyp = payloadType

	for key, val := range fmtp {
		switch key {
		case "sprop-parameter-sets":
			tmp2 := strings.Split(val, ",")
			if len(tmp2) >= 2 {
				sps, err := base64.StdEncoding.DecodeString(tmp2[0])
				if err != nil {
					return fmt.Errorf("invalid sprop-parameter-sets (%v)", val)
				}

				pps, err := base64.StdEncoding.DecodeString(tmp2[1])
				if err != nil {
					return fmt.Errorf("invalid sprop-parameter-sets (%v)", val)
				}

				t.SPS = sps
				t.PPS = pps
			}

		case "packetization-mode":
			tmp2, err := strconv.ParseInt(val, 10, 64)
			if err != nil {
				return fmt.Errorf("invalid packetization-mode (%v)", val)
			}

			t.PacketizationMode = int(tmp2)
		}
	}

	return nil
}

// Marshal implements Format.
func (t *H264) Marshal() (string, map[string]string) {
	t.mutex.RLock()
	defer t.mutex.RUnlock()

	fmtp := make(map[string]string)

	if t.PacketizationMode != 0 {
		fmtp["packetization-mode"] = strconv.FormatInt(int64(t.PacketizationMode), 10)
	}

	var tmp2 []string
	if t.SPS != nil {
		tmp2 = append(tmp2, base64.StdEncoding.EncodeToString(t.SPS))
	}
	if t.PPS != nil {
		tmp2 = append(tmp2, base64.StdEncoding.EncodeToString(t.PPS))
	}
	if tmp2 != nil {
		fmtp["sprop-parameter-sets"] = strings.Join(tmp2, ",")
	}
	if len(t.SPS) >= 4 {
		fmtp["profile-level-id"] = strings.ToUpper(hex.EncodeToString(t.SPS[1:4]))
	}

	return "H264/90000", fmtp
}

// PTSEqualsDTS implements Format.
func (t *H264) PTSEqualsDTS(pkt *rtp.Packet) bool {
	return rtpH264ContainsIDR(pkt)
}

// CreateDecoder creates a decoder able to decode the content of the format.
func (t *H264) CreateDecoder() *rtph264.Decoder {
	d := &rtph264.Decoder{
		PacketizationMode: t.PacketizationMode,
	}
	d.Init()
	return d
}

// CreateEncoder creates an encoder able to encode the content of the format.
func (t *H264) CreateEncoder() *rtph264.Encoder {
	e := &rtph264.Encoder{
		PayloadType:       t.PayloadTyp,
		PacketizationMode: t.PacketizationMode,
	}
	e.Init()
	return e
}

// SafeSPS returns the format SPS.
func (t *H264) SafeSPS() []byte {
	t.mutex.RLock()
	defer t.mutex.RUnlock()
	return t.SPS
}

// SafePPS returns the format PPS.
func (t *H264) SafePPS() []byte {
	t.mutex.RLock()
	defer t.mutex.RUnlock()
	return t.PPS
}

// SafeSetSPS sets the format SPS.
func (t *H264) SafeSetSPS(v []byte) {
	t.mutex.Lock()
	defer t.mutex.Unlock()
	t.SPS = v
}

// SafeSetPPS sets the format PPS.
func (t *H264) SafeSetPPS(v []byte) {
	t.mutex.Lock()
	defer t.mutex.Unlock()
	t.PPS = v
}
