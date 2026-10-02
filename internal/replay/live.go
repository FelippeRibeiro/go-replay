package replay

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/FelippeRibeiro/go-replay/internal/camera"
)

// reconnectDelay é a pausa entre tentativas de reconexão.
const reconnectDelay = 3 * time.Second

// Live mantém a conexão com a câmera e o buffer em memória sempre atualizado,
// reconectando sozinho quando a sessão cai.
type Live struct {
	opts   camera.Options
	window time.Duration

	mu         sync.RWMutex
	buffer     *Buffer
	connected  bool
	transport  string
	since      time.Time
	lastError  string
	reconnects int
	stats      camera.Stats
}

func NewLive(opts camera.Options, window time.Duration) *Live {
	if opts.Logf == nil {
		opts.Logf = func(string, ...any) {}
	}
	return &Live{opts: opts, window: window}
}

// Buffer devolve o buffer atual, ou nil antes da primeira conexão.
func (l *Live) Buffer() *Buffer {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.buffer
}

// Status descreve a conexão para a interface web.
type Status struct {
	Connected  bool    `json:"connected"`
	Transport  string  `json:"transport,omitempty"`
	UptimeSecs float64 `json:"uptime_seconds"`
	Reconnects int     `json:"reconnects"`
	LastError  string  `json:"last_error,omitempty"`
	Packets    int64   `json:"packets"`
	Images     int64   `json:"images"`
	Incomplete int64   `json:"incomplete"`
	AudioPkts  int64   `json:"audio_packets,omitempty"`
	Buffer     *Stats  `json:"buffer,omitempty"`
}

func (l *Live) Status() Status {
	l.mu.RLock()
	status := Status{
		Connected:  l.connected,
		Transport:  l.transport,
		Reconnects: l.reconnects,
		LastError:  l.lastError,
		Packets:    l.stats.Packets,
		Images:     l.stats.Images,
		Incomplete: l.stats.Incomplete,
		AudioPkts:  l.stats.AudioPackets,
	}
	if l.connected && !l.since.IsZero() {
		status.UptimeSecs = time.Since(l.since).Seconds()
	}
	buffer := l.buffer
	l.mu.RUnlock()

	if buffer != nil {
		stats := buffer.Stats()
		status.Buffer = &stats
	}
	return status
}

// Run conecta e recebe vídeo até o contexto encerrar, reconectando em caso de
// falha. Bloqueia; chame numa goroutine.
func (l *Live) Run(ctx context.Context) {
	for ctx.Err() == nil {
		if err := l.session(ctx); err != nil && ctx.Err() == nil {
			l.fail(err)
			l.opts.Logf("conexão perdida (%v); tentando de novo em %s", err, reconnectDelay)
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(reconnectDelay):
		}
	}
}

// session é uma conexão do início ao fim.
func (l *Live) session(ctx context.Context) error {
	stream, err := camera.Open(l.opts)
	if err != nil {
		return err
	}
	defer stream.Close()

	video := stream.Video()
	buffer := l.attach(video, stream)
	if audio, ok := stream.Audio(); ok {
		buffer.EnableAudio(audio)
		l.opts.Logf("conectado por %s: %s a %d Hz + %s %d Hz, guardando %s em memória",
			stream.Transport(), video.Codec, video.TimeScale, audio.Codec, audio.SampleRate, l.window)
	} else {
		l.opts.Logf("conectado por %s: %s a %d Hz, guardando %s em memória (stream sem áudio)",
			stream.Transport(), video.Codec, video.TimeScale, l.window)
	}

	// A cada reconexão os timestamps da câmera recomeçam, então o buffer
	// anterior não pode ser continuado: ele é substituído.
	err = stream.Run(ctx, func(au [][]byte, pts int64) {
		buffer.Add(au, pts)
		l.observe(stream.Stats())
	}, func(payload []byte, pts int64) {
		buffer.AddAudio(payload, pts)
	})
	l.disconnect()
	if errors.Is(err, ctx.Err()) {
		return nil
	}
	return err
}

func (l *Live) attach(video camera.Video, stream *camera.Stream) *Buffer {
	buffer := NewBuffer(video, l.window)

	l.mu.Lock()
	defer l.mu.Unlock()
	l.buffer = buffer
	l.connected = true
	l.transport = stream.Transport()
	l.since = time.Now()
	l.lastError = ""
	return buffer
}

func (l *Live) observe(stats camera.Stats) {
	l.mu.Lock()
	l.stats = stats
	l.mu.Unlock()
}

func (l *Live) disconnect() {
	l.mu.Lock()
	l.connected = false
	l.mu.Unlock()
}

func (l *Live) fail(err error) {
	l.mu.Lock()
	l.connected = false
	l.lastError = err.Error()
	l.reconnects++
	l.mu.Unlock()
}
