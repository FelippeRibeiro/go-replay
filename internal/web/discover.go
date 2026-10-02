package web

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"time"

	"github.com/FelippeRibeiro/go-replay/internal/discovery"
)

// scanTimeout limita a varredura da interface: um /24 nas portas padrão
// costuma acabar antes, mas host sem RST segura o timeout de cada sonda.
const scanTimeout = 45 * time.Second

// handleDiscover varre a LAN por servidores RTSP. Não descobre senha nem o
// caminho do stream (/onvif1); a página só preenche o host para o cadastro.
func (s *Server) handleDiscover(w http.ResponseWriter, r *http.Request) {
	if !s.scan.TryLock() {
		writeError(w, http.StatusConflict, errors.New("já existe uma varredura em andamento"))
		return
	}
	defer s.scan.Unlock()

	var body struct {
		CIDR  string `json:"cidr"`
		Ports string `json:"ports"`
	}
	if r.ContentLength != 0 {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodySize)).Decode(&body); err != nil {
			writeError(w, http.StatusBadRequest, errors.New("corpo inválido: esperado {\"cidr\": \"192.168.1.0/24\"}"))
			return
		}
	}

	prefixes, err := discovery.ResolvePrefixes(body.CIDR)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	ports := discovery.DefaultPorts
	if body.Ports != "" {
		ports, err = discovery.ParsePorts(body.Ports)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
	}

	networks := make([]string, len(prefixes))
	for i, p := range prefixes {
		networks[i] = p.String()
	}
	log.Printf("varredura: %v nas portas %v", networks, ports)

	ctx, cancel := context.WithTimeout(r.Context(), scanTimeout)
	defer cancel()
	start := time.Now()

	devices, err := discovery.Scan(ctx, discovery.Options{
		Prefixes: prefixes,
		Ports:    ports,
		Timeout:  1500 * time.Millisecond,
		Workers:  256,
	})
	if err != nil && !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) && len(devices) == 0 {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	found := make([]foundDevice, 0, len(devices))
	for _, d := range devices {
		found = append(found, foundDevice{
			Address:      d.Address,
			URL:          d.URL,
			Server:       d.Server,
			RequiresAuth: d.RequiresAuth,
			Medias:       d.Medias,
			RTTMs:        float64(d.RTT) / float64(time.Millisecond),
		})
	}

	resp := discoverResponse{
		Networks: networks,
		Devices:  found,
		Seconds:  time.Since(start).Seconds(),
	}
	if errors.Is(err, context.DeadlineExceeded) {
		resp.Warning = "a varredura parou no tempo limite; o que apareceu é o que deu tempo de achar"
	}
	writeJSON(w, http.StatusOK, resp)
}

type foundDevice struct {
	Address      string   `json:"address"`
	URL          string   `json:"url"`
	Server       string   `json:"server,omitempty"`
	RequiresAuth bool     `json:"requires_auth"`
	Medias       []string `json:"medias,omitempty"`
	RTTMs        float64  `json:"rtt_ms"`
}

type discoverResponse struct {
	Networks []string      `json:"networks"`
	Devices  []foundDevice `json:"devices"`
	Seconds  float64       `json:"seconds"`
	Warning  string        `json:"warning,omitempty"`
}
