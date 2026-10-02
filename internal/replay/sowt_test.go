package replay

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	mp4codecs "github.com/bluenviron/mediacommon/v2/pkg/formats/mp4/codecs"
	"github.com/bluenviron/mediacommon/v2/pkg/formats/pmp4"

	"github.com/FelippeRibeiro/go-replay/internal/camera"
)

func TestRewriteIPCMtoSOWTNoAudio(t *testing.T) {
	in := []byte("ftyp....moov....mdat....")
	out, err := rewriteIPCMtoSOWT(in)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(in, out) {
		t.Fatal("arquivo sem ipcm não deveria mudar")
	}
}

func TestClipEncodePCMViraSOWT(t *testing.T) {
	pcm := make([]byte, 320)
	clip := &Clip{
		Codec:     camera.CodecH264,
		Audio:     camera.CodecPCMA,
		timeScale: 90000,
		codecInfo: &mp4codecs.H264{
			SPS: []byte{
				0x67, 0x42, 0xc0, 0x28, 0xd9, 0x00, 0x78, 0x02,
				0x27, 0xe5, 0x84, 0x00, 0x00, 0x03, 0x00, 0x04,
				0x00, 0x00, 0x03, 0x00, 0xf0, 0x3c, 0x60, 0xc9, 0x20,
			},
			PPS: []byte{0x08, 0x06, 0x07, 0x08},
		},
		samples: []*pmp4.Sample{{
			Duration:    90000,
			PayloadSize: 4,
			GetPayload:  func() ([]byte, error) { return []byte{0, 0, 0, 1}, nil },
		}},
		audioScale: 8000,
		audioCodec: &mp4codecs.LPCM{
			LittleEndian: true,
			BitDepth:     16,
			SampleRate:   8000,
			ChannelCount: 1,
		},
		audio: []*pmp4.Sample{{
			Duration:    160,
			PayloadSize: uint32(len(pcm)),
			GetPayload:  func() ([]byte, error) { return pcm, nil },
		}},
	}

	var raw bytes.Buffer
	if err := (pmp4.Presentation{Tracks: []*pmp4.Track{
		{ID: 1, TimeScale: clip.timeScale, Codec: clip.codecInfo, Samples: clip.samples},
		{ID: 2, TimeScale: clip.audioScale, Codec: clip.audioCodec, Samples: clip.audio},
	}}).Marshal(&raw); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw.Bytes(), []byte("ipcm")) || !bytes.Contains(raw.Bytes(), []byte("pcmC")) {
		t.Fatal("o mux deveria ter escrito ipcm+pcmC antes do rewrite")
	}

	var buf bytes.Buffer
	if err := clip.Encode(&buf); err != nil {
		t.Fatal(err)
	}
	out := buf.Bytes()
	if bytes.Contains(out, []byte("ipcm")) {
		t.Fatal("ipcm deveria ter virado sowt")
	}
	if !bytes.Contains(out, []byte("sowt")) {
		t.Fatal("faltou a marca sowt")
	}
	if bytes.Contains(out, []byte("pcmC")) {
		t.Fatal("pcmC deveria ter saído")
	}
	if len(out) != raw.Len()-pcmCSize {
		t.Fatalf("tamanho = %d, esperado %d", len(out), raw.Len()-pcmCSize)
	}

	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		return
	}
	tmp := filepath.Join(t.TempDir(), "sowt.mp4")
	if err := os.WriteFile(tmp, out, 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(ffprobe, "-v", "error", "-show_entries",
		"stream=codec_name,codec_tag_string,duration", "-of", "csv=p=0", tmp)
	got, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("ffprobe recusou o arquivo: %v\n%s", err, got)
	}
	if !bytes.Contains(got, []byte("sowt")) {
		t.Fatalf("ffprobe sem sowt:\n%s", got)
	}
}
