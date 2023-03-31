package gortsplib

import (
	"github.com/pipe01/gortsplib/v3/pkg/format"
	"github.com/pipe01/gortsplib/v3/pkg/rtcpsender"
)

type serverStreamFormat struct {
	format     format.Format
	rtcpSender *rtcpsender.RTCPSender
}
