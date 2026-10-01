package discovery

import (
	"context"
	"errors"
	"net"
	"strconv"
	"time"

	"github.com/bluenviron/gortsplib/v5"
	"github.com/bluenviron/gortsplib/v5/pkg/base"

	"github.com/FelippeRibeiro/go-replay/internal/cli"
	"github.com/bluenviron/gortsplib/v5/pkg/liberrors"
)

// Device é um servidor RTSP encontrado na rede.
type Device struct {
	Address      string        `json:"address"`
	URL          string        `json:"url"`
	Server       string        `json:"server,omitempty"`
	Methods      string        `json:"methods,omitempty"`
	RequiresAuth bool          `json:"requires_auth"`
	Medias       []string      `json:"medias,omitempty"`
	RTT          time.Duration `json:"rtt"`
}

// Probe conversa em RTSP com host:port para saber se há uma câmera ali.
// Devolve erro quando a porta está fechada ou responde outro protocolo.
func Probe(ctx context.Context, host string, port int, timeout time.Duration) (*Device, error) {
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	target, err := base.ParseURL("rtsp://" + addr + "/")
	if err != nil {
		return nil, err
	}

	client := &gortsplib.Client{
		Scheme:      target.Scheme,
		Host:        target.Host,
		ReadTimeout: timeout,
	}
	start := time.Now()
	if err := client.Start(); err != nil {
		return nil, err
	}
	defer client.Close()

	// O OPTIONS é a requisição mais barata do protocolo e nunca exige senha,
	// então serve para confirmar que existe um servidor RTSP do outro lado.
	res, err := client.Options(target)
	if err != nil {
		return nil, err
	}
	dev := &Device{
		Address: addr,
		URL:     target.String(),
		RTT:     time.Since(start),
		Server:  cli.Header(res.Header["Server"]),
		Methods: cli.Header(res.Header["Public"]),
	}

	// O DESCRIBE revela as trilhas quando o stream é aberto, e a exigência de
	// credenciais quando não é. Os dois desfechos são informação útil.
	desc, _, err := client.Describe(target)
	switch {
	case err == nil:
		for _, m := range desc.Medias {
			for _, f := range m.Formats {
				dev.Medias = append(dev.Medias, string(m.Type)+"/"+f.Codec())
			}
		}
	case requiresAuth(err):
		dev.RequiresAuth = true
	}
	return dev, nil
}

func requiresAuth(err error) bool {
	var badStatus liberrors.ErrClientBadStatusCode
	return errors.As(err, &badStatus) && badStatus.Code == base.StatusUnauthorized
}
