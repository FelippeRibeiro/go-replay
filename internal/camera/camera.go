// Package camera conecta em câmeras RTSP e entrega as imagens recebidas.
package camera

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/bluenviron/gortsplib/v5"
	"github.com/bluenviron/gortsplib/v5/pkg/base"
	"github.com/bluenviron/gortsplib/v5/pkg/description"
	"github.com/bluenviron/gortsplib/v5/pkg/format"
	"github.com/bluenviron/gortsplib/v5/pkg/format/rtph264"
	"github.com/bluenviron/gortsplib/v5/pkg/format/rtph265"
	"github.com/bluenviron/gortsplib/v5/pkg/liberrors"
	"github.com/pion/rtp"
)

// Codecs de vídeo suportados.
const (
	CodecH264 = "H264"
	CodecH265 = "H265"
)

// Transportes aceitos em Options.Transport.
const (
	// TransportAuto tenta TCP e cai para UDP se a câmera recusar.
	TransportAuto = "auto"
	TransportTCP  = "tcp"
	TransportUDP  = "udp"
)

// Options configura a conexão com a câmera.
type Options struct {
	// URL é o stream, já com as credenciais resolvidas.
	URL *base.URL
	// Timeout limita a espera por resposta e por dados.
	Timeout time.Duration
	// Transport escolhe como a mídia é entregue.
	Transport string

	// Logf, se definido, recebe avisos.
	Logf func(format string, args ...any)
	// Trace, se definido, recebe cada requisição e resposta RTSP.
	Trace func(format string, args ...any)
}

// Video descreve a trilha de vídeo publicada pela câmera.
type Video struct {
	Codec     string
	TimeScale uint32
	// VPS, SPS e PPS são a configuração do decodificador. Vêm do SDP quando a
	// câmera os publica lá, e do próprio stream quando não.
	VPS, SPS, PPS []byte
}

// Stats conta o caminho dos pacotes, para explicar uma captura vazia.
type Stats struct {
	Packets      int64
	Incomplete   int64
	Images       int64
	AudioPackets int64
	AudioFrames  int64
}

// Stream é uma sessão pronta para receber vídeo e, quando a câmera publica, áudio.
type Stream struct {
	client    *gortsplib.Client
	media     *description.Media
	format    format.Format
	decoder   auDecoder
	video     Video
	transport string

	audioMedia   *description.Media
	audioFormat  format.Format
	audioDecoder audioDecoder
	audio        Audio

	packets      atomic.Int64
	incomplete   atomic.Int64
	images       atomic.Int64
	audioPackets atomic.Int64
	audioFrames  atomic.Int64
	g711Locked   bool
	logf         func(string, ...any)
}

// auDecoder remonta uma imagem completa a partir dos pacotes RTP.
type auDecoder interface {
	Decode(pkt *rtp.Packet) ([][]byte, error)
}

// isPartial informa se o erro apenas significa que a imagem ainda não terminou.
// Cada codec tem os seus próprios sentinelas, com a mesma mensagem.
func isPartial(err error) bool {
	return errors.Is(err, rtph264.ErrMorePacketsNeeded) ||
		errors.Is(err, rtph264.ErrNonStartingPacketAndNoPrevious) ||
		errors.Is(err, rtph265.ErrMorePacketsNeeded) ||
		errors.Is(err, rtph265.ErrNonStartingPacketAndNoPrevious)
}

// Open conecta na câmera e negocia o transporte, tentando as opções em ordem.
//
// Há firmware que confirma o transporte TCP com o perfil do UDP no header
// Transport. O padrão manda rejeitar, e é o que a biblioteca faz, então a
// alternativa é cair para UDP em vez de desistir da câmera.
func Open(opts Options) (*Stream, error) {
	opts.applyDefaults()

	attempts := []gortsplib.Protocol{gortsplib.ProtocolTCP, gortsplib.ProtocolUDP}
	switch opts.Transport {
	case TransportTCP:
		attempts = attempts[:1]
	case TransportUDP:
		attempts = attempts[1:]
	}

	var lastErr error
	for i, protocol := range attempts {
		stream, err := dial(opts, protocol)
		if err == nil {
			return stream, nil
		}
		lastErr = err

		var wantsUDP liberrors.ErrClientServerRequestedUDP
		if i+1 < len(attempts) && errors.As(err, &wantsUDP) {
			opts.Logf("aviso: a câmera confirmou o transporte TCP com o perfil do UDP " +
				"(firmware fora do padrão); tentando UDP")
			continue
		}
		return nil, err
	}
	return nil, lastErr
}

// dial abre a sessão com um transporte específico, até o SETUP concluído.
func dial(opts Options, protocol gortsplib.Protocol) (*Stream, error) {
	client := &gortsplib.Client{
		Scheme:      opts.URL.Scheme,
		Host:        opts.URL.Host,
		Protocol:    &protocol,
		ReadTimeout: opts.Timeout,
	}

	var warned bool
	client.OnResponse = func(res *base.Response) {
		if normalizeTransport(res) && !warned {
			warned = true
			opts.Logf("aviso: a câmera anunciou o transporte TCP com o perfil do UDP; " +
				"seguindo pelo campo interleaved, que é o que ela realmente usa")
		}
		if opts.Trace != nil {
			opts.Trace("<< %d %s %v", res.StatusCode, res.StatusMessage, res.Header)
		}
	}
	if opts.Trace != nil {
		client.OnRequest = func(req *base.Request) {
			opts.Trace(">> %s %s %v", req.Method, req.URL, req.Header)
		}
	}

	if err := client.Start(); err != nil {
		return nil, err
	}
	stream, err := setup(client, opts)
	if err != nil {
		client.Close()
		return nil, err
	}
	stream.transport = protocol.String()
	return stream, nil
}

// setup escolhe as trilhas, negocia o transporte e prepara os remontadores.
func setup(client *gortsplib.Client, opts Options) (*Stream, error) {
	desc, res, err := client.Describe(opts.URL)
	if err != nil {
		return nil, err
	}
	sdp := ""
	if res != nil {
		sdp = string(res.Body)
	}

	stream, err := setupVideo(client, desc)
	if err != nil {
		return nil, err
	}
	stream.logf = opts.Logf
	if err := stream.setupAudio(client, desc, sdp); err != nil {
		opts.Logf("aviso: áudio indisponível (%v); gravando só o vídeo", err)
	}
	return stream, nil
}

// setupVideo escolhe a trilha de vídeo, negocia o transporte e prepara o
// remontador de imagens.
func setupVideo(client *gortsplib.Client, desc *description.Session) (*Stream, error) {
	var h265Format *format.H265
	if media := desc.FindFormat(&h265Format); media != nil {
		decoder, err := h265Format.CreateDecoder()
		if err != nil {
			return nil, err
		}
		if _, err := client.Setup(desc.BaseURL, media, 0, 0); err != nil {
			return nil, fmt.Errorf("negociando transporte de vídeo: %w", err)
		}
		return &Stream{
			client:  client,
			media:   media,
			format:  h265Format,
			decoder: decoder,
			video: Video{
				Codec:     CodecH265,
				TimeScale: uint32(h265Format.ClockRate()),
				VPS:       h265Format.VPS,
				SPS:       h265Format.SPS,
				PPS:       h265Format.PPS,
			},
		}, nil
	}

	var h264Format *format.H264
	if media := desc.FindFormat(&h264Format); media != nil {
		decoder, err := h264Format.CreateDecoder()
		if err != nil {
			return nil, err
		}
		if _, err := client.Setup(desc.BaseURL, media, 0, 0); err != nil {
			return nil, fmt.Errorf("negociando transporte de vídeo: %w", err)
		}
		return &Stream{
			client:  client,
			media:   media,
			format:  h264Format,
			decoder: decoder,
			video: Video{
				Codec:     CodecH264,
				TimeScale: uint32(h264Format.ClockRate()),
				SPS:       h264Format.SPS,
				PPS:       h264Format.PPS,
			},
		}, nil
	}

	return nil, errors.New("a câmera não publica vídeo em H264 ou H265")
}

// setupAudio tenta G.711 e depois AAC. Falha não derruba a sessão de vídeo.
func (s *Stream) setupAudio(client *gortsplib.Client, desc *description.Session, sdp string) error {
	var g711Format *format.G711
	if media := desc.FindFormat(&g711Format); media != nil {
		applyAdvertisedG711Clock(g711Format, sdp)
		decoder, audio, err := newG711Decoder(g711Format)
		if err != nil {
			return err
		}
		if _, err := client.Setup(desc.BaseURL, media, 0, 0); err != nil {
			return fmt.Errorf("negociando transporte de áudio: %w", err)
		}
		s.audioMedia = media
		s.audioFormat = g711Format
		s.audioDecoder = decoder
		s.audio = audio
		return nil
	}

	var aacFormat *format.MPEG4Audio
	if media := desc.FindFormat(&aacFormat); media != nil {
		decoder, audio, err := newAACDecoder(aacFormat)
		if err != nil {
			return err
		}
		if _, err := client.Setup(desc.BaseURL, media, 0, 0); err != nil {
			return fmt.Errorf("negociando transporte de áudio: %w", err)
		}
		s.audioMedia = media
		s.audioFormat = aacFormat
		s.audioDecoder = decoder
		s.audio = audio
		return nil
	}

	return errors.New("este stream não publica áudio em G.711 ou AAC (comum no substream /onvif2)")
}

// Video descreve a trilha que será recebida.
func (s *Stream) Video() Video { return s.video }

// Audio devolve a trilha de áudio, se a câmera publicou uma que sabemos gravar.
func (s *Stream) Audio() (Audio, bool) {
	return s.audio, s.audio.Present()
}

// Transport é como a mídia está sendo entregue: TCP ou UDP.
func (s *Stream) Transport() string { return s.transport }

// Stats conta os pacotes processados até agora.
func (s *Stream) Stats() Stats {
	return Stats{
		Packets:      s.packets.Load(),
		Incomplete:   s.incomplete.Load(),
		Images:       s.images.Load(),
		AudioPackets: s.audioPackets.Load(),
		AudioFrames:  s.audioFrames.Load(),
	}
}

func (s *Stream) Close() { s.client.Close() }

// Run recebe mídia e chama os callbacks até o contexto encerrar ou a sessão cair.
// O pts vem no relógio de cada trilha, alinhado entre vídeo e áudio pelo
// decodificador global de timestamps da biblioteca.
func (s *Stream) Run(ctx context.Context, onImage func(au [][]byte, pts int64), onAudio func(payload []byte, pts int64)) error {
	s.client.OnPacketRTP(s.media, s.format, func(pkt *rtp.Packet) {
		s.packets.Add(1)
		pts, ok := s.client.PacketPTS(s.media, pkt)
		if !ok {
			return // ainda sem referência de tempo
		}
		au, err := s.decoder.Decode(pkt)
		if err != nil {
			// Pacote do meio de um quadro fragmentado não é falha: a imagem só
			// fica pronta no último. Contar isso inflaria o diagnóstico com o
			// caso normal e esconderia a perda de verdade.
			if !isPartial(err) {
				s.incomplete.Add(1)
			}
			return
		}
		s.images.Add(1)
		onImage(au, pts)
	})

	if s.audioMedia != nil && onAudio != nil {
		s.client.OnPacketRTP(s.audioMedia, s.audioFormat, func(pkt *rtp.Packet) {
			s.audioPackets.Add(1)
			s.lockG711Clock(pkt)
			pts, ok := s.client.PacketPTS(s.audioMedia, pkt)
			if !ok {
				return
			}
			frames, err := s.audioDecoder.Decode(pkt)
			if err != nil {
				return
			}
			for i, frame := range frames {
				s.audioFrames.Add(1)
				onAudio(frame, pts+int64(i)*s.audio.FrameTicks())
			}
		})
	}

	if _, err := s.client.Play(nil); err != nil {
		return err
	}

	failed := make(chan error, 1)
	go func() { failed <- s.client.Wait() }()
	select {
	case err := <-failed:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (o *Options) applyDefaults() {
	if o.Timeout <= 0 {
		o.Timeout = 5 * time.Second
	}
	if o.Transport == "" {
		o.Transport = TransportAuto
	}
	if o.Logf == nil {
		o.Logf = func(string, ...any) {}
	}
}

// lockG711Clock ajusta a taxa antes do primeiro timestamp. O payload estático 8
// é 8000 Hz no RFC, mas o firmware desta câmera manda 320 amostras por pacote
// (20 ms a 16 kHz). Sem travar a taxa no tamanho do pacote, o áudio sai
// distorcido e o recorte do clipe escolhe o trecho errado.
func (s *Stream) lockG711Clock(pkt *rtp.Packet) {
	if s.g711Locked || s.audio.Codec == CodecAAC {
		return
	}
	forma, ok := s.audioFormat.(*format.G711)
	if !ok {
		s.g711Locked = true
		return
	}
	if rate := inferG711Rate(len(pkt.Payload)); validAudioRate(rate) && rate != forma.SampleRate {
		if s.logf != nil {
			s.logf("aviso: G.711 anunciado a %d Hz, mas os pacotes são de %d Hz; usando a taxa dos pacotes",
				forma.SampleRate, rate)
		}
		forma.SampleRate = rate
		s.audio.SampleRate = rate
		s.audio.TimeScale = uint32(rate)
	}
	s.g711Locked = true
}

func inferG711Rate(payloadBytes int) int {
	if payloadBytes <= 0 {
		return 0
	}
	// ptime usual do G.711 é 20 ms: taxa = amostras × 50. Pacotes mais longos
	// (esta câmera manda 512 bytes / 32 ms) não caem numa taxa padrão e o
	// relógio fica com o rtpmap.
	return payloadBytes * 50
}
