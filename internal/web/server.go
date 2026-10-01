// Package web serve a interface de replay e a API que gerencia as câmeras.
package web

import (
	"embed"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"time"

	"github.com/FelippeRibeiro/go-replay/internal/replay"
)

//go:embed index.html
var assets embed.FS

// maxBodySize limita o corpo das requisições; só recebemos uma url por vez.
const maxBodySize = 4 << 10

// Server expõe o estado das câmeras, o cadastro de novas e os replays.
type Server struct {
	manager  *replay.Manager
	store    *replay.Store
	clipSpan time.Duration
}

func NewServer(manager *replay.Manager, store *replay.Store, clipSpan time.Duration) *Server {
	return &Server{manager: manager, store: store, clipSpan: clipSpan}
}

// Handler monta as rotas.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.handleIndex)
	mux.HandleFunc("GET /api/status", s.handleStatus)
	mux.HandleFunc("GET /api/cameras", s.handleCameras)
	mux.HandleFunc("POST /api/cameras", s.handleAddCamera)
	mux.HandleFunc("GET /api/replays", s.handleReplays)
	mux.HandleFunc("POST /api/replays", s.handleCreateReplay)
	mux.Handle("GET /clips/", http.StripPrefix("/clips/",
		http.FileServer(http.Dir(s.store.Dir()))))
	return mux
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	page, err := assets.ReadFile("index.html")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(page)
}

type statusResponse struct {
	Cameras []replay.CameraStatus `json:"cameras"`
	// ClipSeconds é o tamanho do trecho que o botão salva de cada câmera.
	ClipSeconds float64 `json:"clip_seconds"`
	// Ready é quantas câmeras já têm o trecho inteiro em memória.
	Ready int `json:"ready"`
	Total int `json:"total"`
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	ready, total := s.manager.Ready(s.clipSpan)
	writeJSON(w, http.StatusOK, statusResponse{
		Cameras:     s.manager.Cameras(),
		ClipSeconds: s.clipSpan.Seconds(),
		Ready:       ready,
		Total:       total,
	})
}

func (s *Server) handleCameras(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"cameras": s.manager.Cameras()})
}

// handleAddCamera cadastra uma câmera nova. A conexão é testada antes de
// responder, então a requisição leva alguns segundos.
func (s *Server) handleAddCamera(w http.ResponseWriter, r *http.Request) {
	var body struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodySize)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, errors.New("corpo inválido: esperado {\"url\": \"rtsp://...\"}"))
		return
	}

	status, err := s.manager.Add(body.URL)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusCreated, status)
}

func (s *Server) handleReplays(w http.ResponseWriter, r *http.Request) {
	replays, err := s.store.List()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"replays": replays})
}

// handleCreateReplay é o que o botão chama: recorta o trecho final de todas as
// câmeras e grava tudo numa pasta nova.
func (s *Server) handleCreateReplay(w http.ResponseWriter, r *http.Request) {
	clips, failures := s.manager.Replay(s.clipSpan)
	if len(clips) == 0 {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":    "nenhuma câmera tem vídeo suficiente ainda",
			"failures": failures,
		})
		return
	}

	info, err := s.store.Save(clips, failures)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	log.Printf("replay %s salvo: %d câmera(s), %d falha(s)",
		info.ID, len(info.Clips), len(info.Failures))
	for _, clip := range info.Clips {
		log.Printf("  %s: %.1fs, %d quadros, %.2f MB",
			clip.Camera, clip.Seconds, clip.Frames, float64(clip.Size)/(1<<20))
	}
	writeJSON(w, http.StatusCreated, info)
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}
