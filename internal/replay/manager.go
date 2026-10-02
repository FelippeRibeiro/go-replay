package replay

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/FelippeRibeiro/go-replay/internal/camera"
	"github.com/FelippeRibeiro/go-replay/internal/cli"
)

// ErrNoCameras indica que nenhuma câmera foi cadastrada ainda.
var ErrNoCameras = errors.New("nenhuma câmera cadastrada")

// feed é uma câmera acompanhada pelo gerenciador.
type feed struct {
	name string
	// raw é a linha como está no catálogo, com as credenciais.
	raw string
	// safe é a mesma url sem senha, para exibir e registrar.
	safe string
	live *Live
}

// Manager acompanha várias câmeras ao mesmo tempo, cada uma com seu próprio
// buffer, e recorta o replay de todas de uma vez.
type Manager struct {
	catalog *Catalog
	base    camera.Options
	window  time.Duration
	// user e pass completam as urls do catálogo que não trazem credenciais,
	// o que permite manter a senha fora do arquivo.
	user, pass string

	mu    sync.Mutex
	feeds []*feed
	ctx   context.Context
}

func NewManager(catalog *Catalog, base camera.Options, window time.Duration, user, pass string) *Manager {
	if base.Logf == nil {
		base.Logf = func(string, ...any) {}
	}
	return &Manager{catalog: catalog, base: base, window: window, user: user, pass: pass}
}

// Start lê o catálogo e põe cada câmera a gravar. O contexto fica guardado para
// que as câmeras adicionadas depois, pela rota HTTP, usem o mesmo ciclo de vida.
func (m *Manager) Start(ctx context.Context) error {
	urls, err := m.catalog.Load()
	if err != nil {
		return err
	}

	m.mu.Lock()
	m.ctx = ctx
	m.mu.Unlock()

	for _, raw := range urls {
		if _, err := m.start(raw); err != nil {
			// Uma câmera com url inválida no arquivo não impede as outras de
			// gravar; o aviso basta.
			m.base.Logf("aviso: ignorando %q no catálogo: %v", raw, err)
		}
	}

	if len(m.Cameras()) == 0 {
		return ErrNoCameras
	}
	return nil
}

// Add cadastra uma câmera nova: valida a conexão, grava no catálogo e começa a
// gravar. A validação é feita antes de persistir para que uma url errada não
// fique no arquivo passando por câmera que nunca grava.
func (m *Manager) Add(raw string) (CameraStatus, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return CameraStatus{}, errors.New("informe a url da câmera")
	}

	resolved, err := cli.ResolveURL(raw, m.user, m.pass)
	if err != nil {
		return CameraStatus{}, fmt.Errorf("url inválida: %w", err)
	}
	if m.hasURL(cli.SafeURL(resolved)) {
		return CameraStatus{}, errors.New("essa câmera já está cadastrada")
	}

	probe := m.base
	probe.URL = resolved
	stream, err := camera.Open(probe)
	if err != nil {
		return CameraStatus{}, cli.ExplainFailure(err, cli.HasCredentials(raw, m.user, m.pass))
	}
	stream.Close()

	if err := m.catalog.Append(raw); err != nil {
		return CameraStatus{}, err
	}
	return m.start(raw)
}

// start põe uma câmera a gravar sem tocar no catálogo.
func (m *Manager) start(raw string) (CameraStatus, error) {
	resolved, err := cli.ResolveURL(raw, m.user, m.pass)
	if err != nil {
		return CameraStatus{}, fmt.Errorf("url inválida: %w", err)
	}

	opts := m.base
	opts.URL = resolved
	safe := cli.SafeURL(resolved)

	m.mu.Lock()
	if m.ctx == nil {
		m.mu.Unlock()
		return CameraStatus{}, errors.New("o gerenciador ainda não foi iniciado")
	}
	entry := &feed{
		name: fmt.Sprintf("camera-%d", len(m.feeds)+1),
		raw:  raw,
		safe: safe,
	}
	// Cada câmera registra com o próprio nome, senão não se sabe de qual
	// câmera veio o aviso quando há quatro gravando.
	opts.Logf = func(format string, args ...any) {
		m.base.Logf("["+entry.name+"] "+format, args...)
	}
	entry.live = NewLive(opts, m.window)
	m.feeds = append(m.feeds, entry)
	ctx := m.ctx
	m.mu.Unlock()

	go entry.live.Run(ctx)
	m.base.Logf("câmera %s adicionada: %s", entry.name, safe)
	return entry.status(), nil
}

// CameraStatus é o estado de uma câmera, no formato que a interface consome.
type CameraStatus struct {
	Name string `json:"name"`
	URL  string `json:"url"`
	Status
}

func (f *feed) status() CameraStatus {
	return CameraStatus{Name: f.name, URL: f.safe, Status: f.live.Status()}
}

// Cameras devolve o estado de todas as câmeras, na ordem do catálogo.
func (m *Manager) Cameras() []CameraStatus {
	feeds := m.snapshot()
	all := make([]CameraStatus, 0, len(feeds))
	for _, f := range feeds {
		all = append(all, f.status())
	}
	return all
}

// NamedClip associa um trecho recortado à câmera de onde veio.
type NamedClip struct {
	Camera string
	Clip   *Clip
}

// CameraFailure explica por que uma câmera ficou fora de um replay.
type CameraFailure struct {
	Camera string `json:"camera"`
	Reason string `json:"reason"`
}

// Replay recorta d de vídeo de todas as câmeras.
//
// O recorte de todas acontece antes de qualquer escrita em disco, que é a parte
// lenta: assim os trechos cobrem praticamente o mesmo intervalo de tempo real,
// em vez de ficarem defasados pelo tempo de gravar o arquivo anterior.
func (m *Manager) Replay(d time.Duration) ([]NamedClip, []CameraFailure) {
	feeds := m.snapshot()

	clips := make([]NamedClip, 0, len(feeds))
	var failures []CameraFailure
	for _, f := range feeds {
		buffer := f.live.Buffer()
		if buffer == nil {
			failures = append(failures, CameraFailure{f.name, "sem conexão com a câmera"})
			continue
		}
		clip, err := buffer.Clip(d)
		if err != nil {
			failures = append(failures, CameraFailure{f.name, err.Error()})
			continue
		}
		clips = append(clips, NamedClip{Camera: f.name, Clip: clip})
	}
	return clips, failures
}

// Ready informa quantas câmeras já têm o trecho pedido em memória.
func (m *Manager) Ready(d time.Duration) (ready, total int) {
	feeds := m.snapshot()
	for _, f := range feeds {
		if buffer := f.live.Buffer(); buffer != nil && buffer.Buffered() >= d {
			ready++
		}
	}
	return ready, len(feeds)
}

func (m *Manager) hasURL(safe string) bool {
	for _, f := range m.snapshot() {
		if f.safe == safe {
			return true
		}
	}
	return false
}

// snapshot copia a lista para que a leitura do estado aconteça fora do mutex.
func (m *Manager) snapshot() []*feed {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]*feed(nil), m.feeds...)
}
