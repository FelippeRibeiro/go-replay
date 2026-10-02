package replay

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// metadataName guarda o resumo de um replay ao lado dos vídeos, para que a
// listagem não precise abrir cada MP4 de novo.
const metadataName = "replay.json"

// Store é a pasta onde os replays ficam. Cada replay é uma subpasta com o seu
// id, contendo um arquivo por câmera.
type Store struct {
	dir string
}

func NewStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("criando pasta %s: %w", dir, err)
	}
	return &Store{dir: dir}, nil
}

func (s *Store) Dir() string { return s.dir }

// ClipInfo descreve um vídeo salvo dentro de um replay.
type ClipInfo struct {
	Camera  string  `json:"camera"`
	File    string  `json:"file"`
	URL     string  `json:"url"`
	Size    int64   `json:"size"`
	Seconds float64 `json:"seconds"`
	Codec   string  `json:"codec"`
	Audio   string  `json:"audio,omitempty"`
	Frames  int     `json:"frames"`
}

// ReplayInfo é um replay inteiro: o recorte de todas as câmeras num instante.
type ReplayInfo struct {
	ID        string          `json:"id"`
	CreatedAt time.Time       `json:"created_at"`
	Clips     []ClipInfo      `json:"clips"`
	Failures  []CameraFailure `json:"failures,omitempty"`
}

// Save grava os trechos de todas as câmeras numa pasta nova.
func (s *Store) Save(clips []NamedClip, failures []CameraFailure) (ReplayInfo, error) {
	if len(clips) == 0 {
		return ReplayInfo{}, errors.New("nenhum trecho para salvar")
	}

	now := time.Now()
	id, dir, err := s.newReplayDir(now)
	if err != nil {
		return ReplayInfo{}, err
	}

	info := ReplayInfo{ID: id, CreatedAt: now, Failures: failures}
	for _, named := range clips {
		clipInfo, err := writeClip(dir, id, named)
		if err != nil {
			os.RemoveAll(dir)
			return ReplayInfo{}, err
		}
		info.Clips = append(info.Clips, clipInfo)
	}

	if err := writeMetadata(dir, info); err != nil {
		return ReplayInfo{}, err
	}
	return info, nil
}

// newReplayDir cria a pasta do replay. O id vem do instante, e ganha um sufixo
// se dois replays caírem no mesmo segundo.
func (s *Store) newReplayDir(now time.Time) (id, dir string, err error) {
	base := now.Format("20060102-150405")
	for attempt := 1; attempt <= 100; attempt++ {
		id = base
		if attempt > 1 {
			id = fmt.Sprintf("%s-%d", base, attempt)
		}
		dir = filepath.Join(s.dir, id)
		err = os.Mkdir(dir, 0o755)
		if err == nil {
			return id, dir, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return "", "", fmt.Errorf("criando pasta do replay: %w", err)
		}
	}
	return "", "", errors.New("não foi possível criar a pasta do replay")
}

func writeClip(dir, id string, named NamedClip) (ClipInfo, error) {
	name := named.Camera + ".mp4"
	path := filepath.Join(dir, name)

	file, err := os.Create(path)
	if err != nil {
		return ClipInfo{}, err
	}
	defer file.Close()

	if err := named.Clip.Encode(file); err != nil {
		return ClipInfo{}, fmt.Errorf("gravando %s: %w", name, err)
	}
	stat, err := file.Stat()
	if err != nil {
		return ClipInfo{}, err
	}

	return ClipInfo{
		Camera:  named.Camera,
		File:    name,
		URL:     "/clips/" + id + "/" + name,
		Size:    stat.Size(),
		Seconds: named.Clip.Duration.Seconds(),
		Codec:   named.Clip.Codec,
		Audio:   named.Clip.Audio,
		Frames:  named.Clip.Frames(),
	}, nil
}

func writeMetadata(dir string, info ReplayInfo) error {
	body, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, metadataName), append(body, '\n'), 0o644)
}

// List devolve os replays, do mais recente para o mais antigo.
func (s *Store) List() ([]ReplayInfo, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, err
	}

	replays := make([]ReplayInfo, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		info, err := s.read(entry.Name())
		if err != nil {
			continue // pasta que não é um replay, ou foi mexida à mão
		}
		replays = append(replays, info)
	}
	slices.SortFunc(replays, func(a, b ReplayInfo) int {
		return cmp.Compare(b.ID, a.ID)
	})
	return replays, nil
}

// read carrega um replay do disco, caindo para a varredura dos arquivos quando
// o resumo não existe — o que acontece com pastas copiadas à mão.
func (s *Store) read(id string) (ReplayInfo, error) {
	dir := filepath.Join(s.dir, id)

	body, err := os.ReadFile(filepath.Join(dir, metadataName))
	if err == nil {
		var info ReplayInfo
		if err := json.Unmarshal(body, &info); err == nil && len(info.Clips) > 0 {
			info.ID = id
			return info, nil
		}
	}

	files, err := os.ReadDir(dir)
	if err != nil {
		return ReplayInfo{}, err
	}
	info := ReplayInfo{ID: id}
	for _, file := range files {
		if file.IsDir() || !strings.HasSuffix(file.Name(), ".mp4") {
			continue
		}
		stat, err := file.Info()
		if err != nil {
			continue
		}
		if info.CreatedAt.IsZero() {
			info.CreatedAt = stat.ModTime()
		}
		info.Clips = append(info.Clips, ClipInfo{
			Camera: strings.TrimSuffix(file.Name(), ".mp4"),
			File:   file.Name(),
			URL:    "/clips/" + id + "/" + file.Name(),
			Size:   stat.Size(),
		})
	}
	if len(info.Clips) == 0 {
		return ReplayInfo{}, errors.New("pasta sem vídeos")
	}
	return info, nil
}
