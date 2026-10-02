// Package replay mantém os últimos minutos de vídeo em memória e recorta
// trechos sob demanda.
package replay

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/bluenviron/mediacommon/v2/pkg/codecs/h264"
	"github.com/bluenviron/mediacommon/v2/pkg/codecs/h265"

	"github.com/FelippeRibeiro/go-replay/internal/camera"
)

// dtsExtractor deduz o tempo de decodificação a partir do de apresentação.
// Os dois codecs expõem a mesma operação.
type dtsExtractor interface {
	Extract(au [][]byte, pts int64) (int64, error)
}

// maxConsecutiveFailures é quantas falhas seguidas de extração de tempo toleramos
// antes de recomeçar. O extrator guarda o DTS anterior e só o avança quando dá
// certo, então um único salto de tempo da câmera o deixa travado para sempre:
// sem este limite a gravação para em silêncio, com a conexão ainda de pé.
const maxConsecutiveFailures = 30

// sample é uma imagem pronta para entrar num MP4.
type sample struct {
	dts       int64
	ptsOffset int32
	sync      bool
	payload   []byte
}

// Buffer guarda as imagens mais recentes da câmera, descartando as que saem da
// janela. É alimentado pela goroutine de captura e lido pelas requisições web,
// por isso todo acesso passa pelo mutex.
type Buffer struct {
	mu sync.Mutex

	codec     string
	timeScale uint32
	window    time.Duration
	dts       dtsExtractor
	// newExtractor recria o extrator quando a linha de tempo quebra.
	newExtractor func() dtsExtractor

	// Parameter sets correntes, usados no cabeçalho dos clipes.
	vps, sps, pps []byte

	samples  []sample
	bytes    int64
	accepted int64
	dropped  int64
	failures int
	resets   int64
	lastErr  error

	audio *audioTrack
}

// audioSample é um bloco de áudio pronto para o MP4.
type audioSample struct {
	pts     int64
	payload []byte
}

// audioTrack guarda o áudio na mesma janela do vídeo.
type audioTrack struct {
	info    camera.Audio
	samples []audioSample
}

// NewBuffer cria um buffer para uma trilha de vídeo, guardando window de mídia.
func NewBuffer(video camera.Video, window time.Duration) *Buffer {
	buffer := &Buffer{
		codec:     video.Codec,
		timeScale: video.TimeScale,
		window:    window,
		vps:       video.VPS,
		sps:       video.SPS,
		pps:       video.PPS,
	}
	if buffer.timeScale == 0 {
		buffer.timeScale = 90000 // padrão de vídeo em RTP
	}
	buffer.newExtractor = func() dtsExtractor { return newDTSExtractor(video.Codec) }
	buffer.dts = buffer.newExtractor()
	return buffer
}

// EnableAudio passa a guardar a trilha de áudio junto com o vídeo.
func (b *Buffer) EnableAudio(audio camera.Audio) {
	if !audio.Present() {
		return
	}
	if audio.TimeScale == 0 {
		audio.TimeScale = uint32(audio.SampleRate)
	}
	if audio.TimeScale == 0 {
		audio.TimeScale = 8000
	}
	b.mu.Lock()
	b.audio = &audioTrack{info: audio}
	b.mu.Unlock()
}

// AddAudio registra um bloco de áudio. O pts está no relógio da trilha de áudio,
// já alinhado com o do vídeo pelo decodificador global de timestamps.
func (b *Buffer) AddAudio(payload []byte, pts int64) {
	if len(payload) == 0 {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.audio == nil {
		return
	}
	if len(b.audio.samples) == 0 {
		b.inferAudioClock(payload)
	}
	if n := len(b.audio.samples); n > 0 && pts < b.audio.samples[n-1].pts {
		return // relógio do áudio recuou; o vídeo decide o recomeço
	}
	b.audio.samples = append(b.audio.samples, audioSample{pts: pts, payload: payload})
	b.bytes += int64(len(payload))
}

func (b *Buffer) inferAudioClock(pcm []byte) {
	if b.audio.info.Codec == camera.CodecAAC {
		return
	}
	channels := b.audio.info.Channels
	if channels <= 0 {
		channels = 1
	}
	samples := len(pcm) / (2 * channels)
	rate := samples * 50 // ptime de 20 ms
	if !validPCMRate(rate) {
		return
	}
	b.audio.info.SampleRate = rate
	b.audio.info.TimeScale = uint32(rate)
}

func validPCMRate(rate int) bool {
	switch rate {
	case 8000, 16000, 32000, 48000:
		return true
	}
	return false
}

func newDTSExtractor(codec string) dtsExtractor {
	if codec == camera.CodecH265 {
		extractor := &h265.DTSExtractor{}
		extractor.Initialize()
		return extractor
	}
	extractor := &h264.DTSExtractor{}
	extractor.Initialize()
	return extractor
}

// Add registra uma imagem recebida da câmera.
func (b *Buffer) Add(au [][]byte, pts int64) {
	b.mu.Lock()
	defer b.mu.Unlock()

	payload, randomAccess := b.split(au)
	if len(payload) == 0 {
		return // o access unit só trazia configuração
	}
	// A primeira imagem do buffer precisa ser um ponto de entrada completo,
	// senão nada do que vier depois é decodificável.
	if len(b.samples) == 0 && (!randomAccess || !b.hasParameterSets()) {
		return
	}

	// Relógio da câmera andando para trás: o que está em memória e o que vem
	// agora não pertencem à mesma linha de tempo.
	if n := len(b.samples); n > 0 && pts < b.samples[n-1].dts {
		b.reset(fmt.Errorf("o relógio da câmera voltou %s",
			b.duration(b.samples[n-1].dts-pts).Round(time.Millisecond)))
		return
	}

	// A câmera pode publicar a configuração num access unit separado do
	// quadro-chave. Reinseri-la aqui deixa cada ponto de entrada autossuficiente
	// e é o que permite ao extrator de tempo ler o SPS.
	if randomAccess {
		payload = append(b.parameterSets(), payload...)
	}

	dts, err := b.dts.Extract(payload, pts)
	if err != nil {
		b.dropped++
		b.failures++
		b.lastErr = fmt.Errorf("tempo de decodificação: %w", err)
		if b.failures >= maxConsecutiveFailures {
			b.reset(b.lastErr)
		}
		return
	}
	b.failures = 0
	encoded, err := h264.AVCC(payload).Marshal()
	if err != nil {
		b.dropped++
		b.lastErr = fmt.Errorf("convertendo imagem: %w", err)
		return
	}

	b.samples = append(b.samples, sample{
		dts:       dts,
		ptsOffset: int32(pts - dts),
		sync:      randomAccess,
		payload:   encoded,
	})
	b.bytes += int64(len(encoded))
	b.accepted++
	b.prune()
}

// reset recomeça do zero depois de uma quebra na linha de tempo.
//
// O que já está em memória é descartado de propósito: com o relógio da câmera
// deslocado, os tempos antigos e os novos não são comparáveis, e um MP4 montado
// a partir da mistura sairia quebrado. Perde-se o histórico, mas a gravação
// volta sozinha no próximo quadro-chave.
func (b *Buffer) reset(cause error) {
	b.samples = nil
	b.bytes = 0
	b.failures = 0
	b.resets++
	b.lastErr = fmt.Errorf("recomeçando o buffer: %w", cause)
	b.dts = b.newExtractor()
	if b.audio != nil {
		b.audio.samples = nil
	}
}

// prune descarta o excedente da janela. O corte acontece sempre num
// quadro-chave, para que o início do buffer continue decodificável.
func (b *Buffer) prune() {
	limit := b.ticks(b.window)
	newest := b.samples[len(b.samples)-1].dts

	cut := 0
	for i, s := range b.samples {
		if newest-s.dts <= limit {
			break
		}
		if s.sync {
			cut = i
		}
	}
	if cut == 0 {
		return
	}

	for _, s := range b.samples[:cut] {
		b.bytes -= int64(len(s.payload))
	}
	// Move o restante para o começo da fatia, para que o array não cresça sem
	// limite à medida que as imagens entram e saem.
	remaining := copy(b.samples, b.samples[cut:])
	clear(b.samples[remaining:])
	b.samples = b.samples[:remaining]
	b.pruneAudio()
}

func (b *Buffer) pruneAudio() {
	if b.audio == nil || len(b.audio.samples) == 0 || len(b.samples) == 0 {
		return
	}
	// O corte do áudio acompanha o vídeo: o que restou de imagem define o
	// instante mais antigo que ainda interessa.
	oldest := b.duration(b.samples[0].dts)
	cutoff := int64(oldest.Seconds() * float64(b.audio.info.TimeScale))

	cut := 0
	for cut < len(b.audio.samples) && b.audio.samples[cut].pts < cutoff {
		cut++
	}
	if cut == 0 {
		return
	}
	for _, s := range b.audio.samples[:cut] {
		b.bytes -= int64(len(s.payload))
	}
	remaining := copy(b.audio.samples, b.audio.samples[cut:])
	clear(b.audio.samples[remaining:])
	b.audio.samples = b.audio.samples[:remaining]
}

// Stats resume o estado do buffer.
type Stats struct {
	Codec    string        `json:"codec"`
	Buffered time.Duration `json:"-"`
	Seconds  float64       `json:"buffered_seconds"`
	Samples  int           `json:"samples"`
	Bytes    int64         `json:"bytes"`
	FPS      float64       `json:"fps"`
	Dropped  int64         `json:"dropped"`
	// Resets conta as quebras na linha de tempo da câmera. Deve ficar em zero;
	// subindo, a câmera está mandando tempos inconsistentes.
	Resets  int64  `json:"resets"`
	Audio   string `json:"audio,omitempty"`
	LastErr string `json:"last_error,omitempty"`
}

func (b *Buffer) Stats() Stats {
	b.mu.Lock()
	defer b.mu.Unlock()

	stats := Stats{
		Codec:   b.codec,
		Samples: len(b.samples),
		Bytes:   b.bytes,
		Dropped: b.dropped,
		Resets:  b.resets,
	}
	if b.audio != nil {
		stats.Audio = b.audio.info.Codec
	}
	if b.lastErr != nil {
		stats.LastErr = b.lastErr.Error()
	}
	if len(b.samples) > 1 {
		span := b.samples[len(b.samples)-1].dts - b.samples[0].dts
		stats.Buffered = b.duration(span)
		stats.Seconds = stats.Buffered.Seconds()
		if stats.Seconds > 0 {
			stats.FPS = float64(len(b.samples)-1) / stats.Seconds
		}
	}
	return stats
}

// Buffered é quanto vídeo está disponível para recortar.
func (b *Buffer) Buffered() time.Duration {
	return b.Stats().Buffered
}

func (b *Buffer) split(au [][]byte) (payload [][]byte, randomAccess bool) {
	if b.codec == camera.CodecH265 {
		randomAccess = h265.IsRandomAccess(au)
		for _, nal := range au {
			if len(nal) == 0 {
				continue
			}
			switch h265.NALUType((nal[0] >> 1) & 0b111111) {
			case h265.NALUType_VPS_NUT:
				b.vps = nal
			case h265.NALUType_SPS_NUT:
				b.sps = nal
			case h265.NALUType_PPS_NUT:
				b.pps = nal
			default:
				payload = append(payload, nal)
			}
		}
		return payload, randomAccess
	}

	randomAccess = h264.IsRandomAccess(au)
	for _, nal := range au {
		if len(nal) == 0 {
			continue
		}
		switch h264.NALUType(nal[0] & 0b11111) {
		case h264.NALUTypeSPS:
			b.sps = nal
		case h264.NALUTypePPS:
			b.pps = nal
		default:
			payload = append(payload, nal)
		}
	}
	return payload, randomAccess
}

// parameterSets devolve a configuração na ordem em que o decodificador espera.
func (b *Buffer) parameterSets() [][]byte {
	if b.codec == camera.CodecH265 {
		return [][]byte{b.vps, b.sps, b.pps}
	}
	return [][]byte{b.sps, b.pps}
}

func (b *Buffer) hasParameterSets() bool {
	if b.codec == camera.CodecH265 {
		return len(b.vps) > 0 && len(b.sps) > 0 && len(b.pps) > 0
	}
	return len(b.sps) > 0 && len(b.pps) > 0
}

// ticks converte tempo real para o relógio da mídia.
func (b *Buffer) ticks(d time.Duration) int64 {
	return int64(d.Seconds() * float64(b.timeScale))
}

// duration converte o relógio da mídia para tempo real.
func (b *Buffer) duration(ticks int64) time.Duration {
	return time.Duration(ticks) * time.Second / time.Duration(b.timeScale)
}

// ErrNotEnoughVideo indica que o buffer ainda não tem o trecho pedido.
var ErrNotEnoughVideo = errors.New("vídeo insuficiente no buffer")
