package camera

import (
	"testing"

	"github.com/bluenviron/gortsplib/v5/pkg/format"
)

func TestParseRtpmapAudioRespeitaATaxaDoSDP(t *testing.T) {
	sdp := "m=audio 0 RTP/AVP 8\r\na=rtpmap:8 PCMA/16000\r\n"
	rate, channels, ok := parseRtpmapAudio(sdp, 8)
	if !ok {
		t.Fatal("esperava encontrar o rtpmap")
	}
	if rate != 16000 {
		t.Errorf("taxa = %d, esperado 16000", rate)
	}
	if channels != 1 {
		t.Errorf("canais = %d, esperado 1", channels)
	}
}

func TestParseRtpmapAudioComCanais(t *testing.T) {
	rate, channels, ok := parseRtpmapAudio("a=rtpmap:0 PCMU/8000/2\n", 0)
	if !ok || rate != 8000 || channels != 2 {
		t.Errorf("rate=%d channels=%d ok=%v", rate, channels, ok)
	}
}

func TestParseRtpmapAudioAusente(t *testing.T) {
	if _, _, ok := parseRtpmapAudio("a=rtpmap:96 H265/90000\n", 8); ok {
		t.Error("não deveria achar rtpmap de payload 8")
	}
}

func TestInferG711Rate(t *testing.T) {
	if got := inferG711Rate(160); got != 8000 {
		t.Errorf("160 bytes = %d Hz, esperado 8000", got)
	}
	if got := inferG711Rate(320); got != 16000 {
		t.Errorf("320 bytes = %d Hz, esperado 16000", got)
	}
	if inferG711Rate(512) != 25600 {
		t.Errorf("512 bytes = %d, esperado 25600 (ptime 20 ms)", inferG711Rate(512))
	}
	if validAudioRate(25600) {
		t.Error("25600 Hz não é taxa padrão; o rtpmap é quem manda")
	}
}

func TestApplyAdvertisedG711Clock(t *testing.T) {
	forma := &format.G711{PayloadTyp: 8, SampleRate: 8000, ChannelCount: 1}
	applyAdvertisedG711Clock(forma, "m=audio 0 RTP/AVP 8\r\na=rtpmap:8 PCMA/16000\r\n")
	if forma.SampleRate != 16000 {
		t.Errorf("taxa = %d, esperado 16000 do rtpmap", forma.SampleRate)
	}
}

func TestSwapEndian16(t *testing.T) {
	pcm := []byte{0x12, 0x34, 0x56, 0x78}
	swapEndian16(pcm)
	if pcm[0] != 0x34 || pcm[1] != 0x12 || pcm[2] != 0x78 || pcm[3] != 0x56 {
		t.Errorf("pcm = %v", pcm)
	}
}
