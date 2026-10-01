package camera

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/bluenviron/gortsplib/v5/pkg/format"
	"github.com/bluenviron/mediacommon/v2/pkg/codecs/g711"
	"github.com/bluenviron/mediacommon/v2/pkg/codecs/mpeg4audio"
	"github.com/pion/rtp"
)

// Codecs de áudio suportados.
const (
	CodecPCMA = "PCMA"
	CodecPCMU = "PCMU"
	CodecAAC  = "AAC"
)

// Audio descreve a trilha de áudio, quando a câmera publica uma.
type Audio struct {
	Codec      string
	TimeScale  uint32
	SampleRate int
	Channels   int
	// MULaw vale só para G.711: true é µ-law (PCMU), false é A-law (PCMA).
	MULaw bool
	// AAC é a configuração do decodificador, quando o codec é AAC.
	AAC *mpeg4audio.AudioSpecificConfig
}

// Present informa se a câmera publicou áudio que sabemos gravar.
func (a Audio) Present() bool { return a.Codec != "" }

// FrameTicks é o avanço do relógio entre access units do mesmo pacote RTP.
// G.711 manda um bloco por pacote, então não há avanço interno.
func (a Audio) FrameTicks() int64 {
	if a.Codec == CodecAAC {
		return mpeg4audio.SamplesPerAccessUnit
	}
	return 0
}

// audioDecoder remonta o payload de um pacote RTP de áudio.
type audioDecoder interface {
	Decode(pkt *rtp.Packet) ([][]byte, error)
}

type g711Decoder struct {
	rtp interface {
		Decode(*rtp.Packet) ([]byte, error)
	}
	mulaw bool
}

func (d *g711Decoder) Decode(pkt *rtp.Packet) ([][]byte, error) {
	samples, err := d.rtp.Decode(pkt)
	if err != nil {
		return nil, err
	}
	var pcm []byte
	if d.mulaw {
		var raw g711.Mulaw
		raw.Unmarshal(samples)
		pcm = raw
	} else {
		var raw g711.Alaw
		raw.Unmarshal(samples)
		pcm = raw
	}
	// O Unmarshal devolve big-endian; o MP4 e os players esperam little-endian
	// no LPCM (sowt/ipcm).
	swapEndian16(pcm)
	return [][]byte{pcm}, nil
}

type aacDecoder struct {
	rtp interface {
		Decode(*rtp.Packet) ([][]byte, error)
	}
}

func (d *aacDecoder) Decode(pkt *rtp.Packet) ([][]byte, error) {
	return d.rtp.Decode(pkt)
}

// applyAdvertisedG711Clock corrige o relógio do payload estático 0/8. O RFC
// manda 8000 Hz, então a biblioteca ignora o rtpmap; este firmware anuncia
// PCMA/16000 e de fato envia nesse ritmo.
func applyAdvertisedG711Clock(forma *format.G711, sdp string) {
	rate, channels, ok := parseRtpmapAudio(sdp, forma.PayloadType())
	if !ok {
		return
	}
	if !validAudioRate(rate) {
		return
	}
	forma.SampleRate = rate
	if channels > 0 {
		forma.ChannelCount = channels
	}
}

func validAudioRate(rate int) bool {
	switch rate {
	case 8000, 16000, 32000, 48000:
		return true
	}
	return false
}

func newG711Decoder(forma *format.G711) (*g711Decoder, Audio, error) {
	rtpDec, err := forma.CreateDecoder()
	if err != nil {
		return nil, Audio{}, err
	}
	codec := CodecPCMA
	if forma.MULaw {
		codec = CodecPCMU
	}
	channels := forma.ChannelCount
	if channels <= 0 {
		channels = 1
	}
	return &g711Decoder{rtp: rtpDec, mulaw: forma.MULaw}, Audio{
		Codec:      codec,
		TimeScale:  uint32(forma.ClockRate()),
		SampleRate: forma.SampleRate,
		Channels:   channels,
		MULaw:      forma.MULaw,
	}, nil
}

func newAACDecoder(forma *format.MPEG4Audio) (*aacDecoder, Audio, error) {
	rtpDec, err := forma.CreateDecoder()
	if err != nil {
		return nil, Audio{}, err
	}
	channels := 1
	if forma.Config != nil && forma.Config.ChannelConfig >= 1 && forma.Config.ChannelConfig <= 6 {
		channels = int(forma.Config.ChannelConfig)
	}
	return &aacDecoder{rtp: rtpDec}, Audio{
		Codec:      CodecAAC,
		TimeScale:  uint32(forma.ClockRate()),
		SampleRate: forma.Config.SampleRate,
		Channels:   channels,
		AAC:        forma.Config,
	}, nil
}

// parseRtpmapAudio lê a taxa e os canais de um `a=rtpmap:<pt> NAME/rate[/ch]`.
func parseRtpmapAudio(sdp string, payloadType uint8) (rate, channels int, ok bool) {
	prefix := fmt.Sprintf("rtpmap:%d ", payloadType)
	idx := strings.Index(sdp, prefix)
	if idx < 0 {
		return 0, 0, false
	}
	rest := sdp[idx+len(prefix):]
	if end := strings.IndexAny(rest, "\r\n"); end >= 0 {
		rest = rest[:end]
	}
	// NAME/rate ou NAME/rate/channels
	parts := strings.Split(strings.TrimSpace(rest), "/")
	if len(parts) < 2 {
		return 0, 0, false
	}
	parsed, err := strconv.Atoi(parts[1])
	if err != nil || parsed <= 0 {
		return 0, 0, false
	}
	channels = 1
	if len(parts) >= 3 {
		if n, err := strconv.Atoi(parts[2]); err == nil && n > 0 {
			channels = n
		}
	}
	return parsed, channels, true
}

func swapEndian16(pcm []byte) {
	for i := 0; i+1 < len(pcm); i += 2 {
		pcm[i], pcm[i+1] = pcm[i+1], pcm[i]
	}
}
