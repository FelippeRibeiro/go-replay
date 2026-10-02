package replay

import (
	"bytes"
	"fmt"
	"io"
	"time"

	mp4codecs "github.com/bluenviron/mediacommon/v2/pkg/formats/mp4/codecs"
	"github.com/bluenviron/mediacommon/v2/pkg/formats/pmp4"

	"github.com/FelippeRibeiro/go-replay/internal/camera"
)

// Clip é um trecho recortado do buffer, pronto para ser escrito em disco.
type Clip struct {
	Codec    string
	Audio    string
	Duration time.Duration
	// Bytes é o tamanho do vídeo e do áudio, sem contar o cabeçalho do MP4.
	Bytes int64

	timeScale uint32
	codecInfo mp4codecs.Codec
	samples   []*pmp4.Sample

	audioScale uint32
	audioCodec mp4codecs.Codec
	audio      []*pmp4.Sample
}

// Frames é quantas imagens o clipe contém.
func (c *Clip) Frames() int { return len(c.samples) }

// HasAudio informa se o clipe leva uma trilha de áudio.
func (c *Clip) HasAudio() bool { return len(c.audio) > 0 }

// Clip recorta os últimos d de vídeo do buffer.
//
// A duração sai exata, mas o início não cai onde se pede: um MP4 precisa abrir
// num quadro-chave, porque os quadros seguintes são descritos em relação a ele.
// O corte começa no último quadro-chave anterior à janela e segue por
// exatamente d, o que atrasa o fim do trecho em até um intervalo de keyframe.
func (b *Buffer) Clip(d time.Duration) (*Clip, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if len(b.samples) < 2 {
		return nil, ErrNotEnoughVideo
	}
	wanted := b.ticks(d)
	newest := b.samples[len(b.samples)-1].dts

	// Último ponto de entrada que deixa d de vídeo à frente dele.
	start := -1
	for i, s := range b.samples {
		if s.sync && newest-s.dts >= wanted {
			start = i
		}
	}
	if start < 0 {
		return nil, fmt.Errorf("%w: há %s, foram pedidos %s",
			ErrNotEnoughVideo, b.duration(newest-b.samples[0].dts).Round(time.Millisecond), d)
	}

	origin := b.samples[start].dts
	clip := &Clip{
		Codec:     b.codec,
		Duration:  b.duration(wanted),
		timeScale: b.timeScale,
		codecInfo: b.codecInfo(),
	}

	for i := start; i < len(b.samples); i++ {
		elapsed := b.samples[i].dts - origin
		if elapsed >= wanted {
			break
		}

		// A duração de cada imagem é a distância até a seguinte. A última do
		// trecho recebe só o tempo que falta para fechar d, o que torna a soma
		// exatamente igual ao pedido.
		duration := wanted - elapsed
		if i+1 < len(b.samples) {
			if next := b.samples[i+1].dts - b.samples[i].dts; next < duration {
				duration = next
			}
		}
		if duration <= 0 {
			duration = 1
		}

		payload := b.samples[i].payload
		clip.samples = append(clip.samples, &pmp4.Sample{
			Duration:        uint32(duration),
			PTSOffset:       b.samples[i].ptsOffset,
			IsNonSyncSample: !b.samples[i].sync,
			PayloadSize:     uint32(len(payload)),
			GetPayload:      func() ([]byte, error) { return payload, nil },
		})
		clip.Bytes += int64(len(payload))
	}

	if len(clip.samples) < 2 {
		return nil, ErrNotEnoughVideo
	}
	b.appendAudio(clip, origin+int64(b.samples[start].ptsOffset), wanted)
	return clip, nil
}

func (b *Buffer) appendAudio(clip *Clip, originPTS, wantedVideo int64) {
	if b.audio == nil || len(b.audio.samples) == 0 || b.timeScale == 0 {
		return
	}
	start := b.audioTicks(originPTS)
	wanted := b.audioTicks(wantedVideo)
	if wanted <= 0 {
		return
	}

	clip.Audio = b.audio.info.Codec
	clip.audioScale = b.audio.info.TimeScale
	clip.audioCodec = b.audioCodecInfo()

	end := start + wanted
	var covered int64
	for _, s := range b.audio.samples {
		duration := b.audioPayloadTicks(s.payload)
		if duration <= 0 {
			continue
		}
		if s.pts+duration <= start {
			continue
		}
		if s.pts >= end {
			break
		}

		payload := s.payload
		keep := duration
		skip := int64(0)
		if s.pts < start {
			skip = start - s.pts
			keep -= skip
		}
		if covered+keep > wanted {
			keep = wanted - covered
		}
		if keep <= 0 {
			break
		}
		payload = b.trimAudioPayload(payload, skip, keep)
		if len(payload) == 0 {
			continue
		}

		clip.audio = append(clip.audio, &pmp4.Sample{
			Duration:    uint32(keep),
			PayloadSize: uint32(len(payload)),
			GetPayload:  func() ([]byte, error) { return payload, nil },
		})
		clip.Bytes += int64(len(payload))
		covered += keep
		if covered >= wanted {
			break
		}
	}
}

func (b *Buffer) trimAudioPayload(payload []byte, skipTicks, keepTicks int64) []byte {
	if b.audio.info.Codec == camera.CodecAAC {
		return payload
	}
	channels := b.audio.info.Channels
	if channels <= 0 {
		channels = 1
	}
	bytesPer := 2 * channels
	skip := int(skipTicks) * bytesPer
	keep := int(keepTicks) * bytesPer
	if skip < 0 {
		skip = 0
	}
	if skip > len(payload) {
		return nil
	}
	payload = payload[skip:]
	if keep < 0 {
		keep = 0
	}
	if keep > len(payload) {
		keep = len(payload)
	}
	out := make([]byte, keep)
	copy(out, payload[:keep])
	return out
}

func (b *Buffer) audioTicks(videoTicks int64) int64 {
	return videoTicks * int64(b.audio.info.TimeScale) / int64(b.timeScale)
}

func (b *Buffer) audioPayloadTicks(payload []byte) int64 {
	if b.audio.info.Codec == camera.CodecAAC {
		return 1024
	}
	channels := b.audio.info.Channels
	if channels <= 0 {
		channels = 1
	}
	return int64(len(payload) / (2 * channels))
}

func (b *Buffer) audioCodecInfo() mp4codecs.Codec {
	a := b.audio.info
	if a.Codec == camera.CodecAAC && a.AAC != nil {
		return &mp4codecs.MPEG4Audio{Config: *a.AAC}
	}
	channels := a.Channels
	if channels <= 0 {
		channels = 1
	}
	rate := a.SampleRate
	if rate <= 0 {
		rate = int(a.TimeScale)
	}
	return &mp4codecs.LPCM{
		LittleEndian: true,
		BitDepth:     16,
		SampleRate:   rate,
		ChannelCount: channels,
	}
}

func (b *Buffer) codecInfo() mp4codecs.Codec {
	if b.codec == camera.CodecH265 {
		return &mp4codecs.H265{VPS: b.vps, SPS: b.sps, PPS: b.pps}
	}
	return &mp4codecs.H264{SPS: b.sps, PPS: b.pps}
}

// Encode escreve o clipe como um MP4.
func (c *Clip) Encode(w io.Writer) error {
	tracks := []*pmp4.Track{{
		ID:        1,
		TimeScale: c.timeScale,
		Codec:     c.codecInfo,
		Samples:   c.samples,
	}}
	if len(c.audio) > 0 && c.audioCodec != nil {
		tracks = append(tracks, &pmp4.Track{
			ID:        2,
			TimeScale: c.audioScale,
			Codec:     c.audioCodec,
			Samples:   c.audio,
		})
	}
	var buf bytes.Buffer
	if err := (pmp4.Presentation{Tracks: tracks}).Marshal(&buf); err != nil {
		return err
	}
	out := buf.Bytes()
	if c.HasAudio() && c.Audio != camera.CodecAAC {
		rewritten, err := rewriteIPCMtoSOWT(out)
		if err != nil {
			return err
		}
		out = rewritten
	}
	_, err := w.Write(out)
	return err
}
