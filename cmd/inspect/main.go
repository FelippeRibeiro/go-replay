package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/bluenviron/gortsplib/v5"

	"github.com/FelippeRibeiro/go-replay/internal/cli"
)

func main() {
	target := flag.String("url", os.Getenv("RTSP_URL"), "url do stream, ex: rtsp://user:senha@192.168.1.8:554/onvif1 (ou a env RTSP_URL)")
	user := flag.String("user", "", "usuário, se preferir não embutir na url")
	pass := flag.String("pass", os.Getenv("RTSP_PASSWORD"), "senha, se preferir não embutir na url (ou a env RTSP_PASSWORD)")
	timeout := flag.Duration("timeout", 5*time.Second, "tempo máximo de espera")
	showSDP := flag.Bool("sdp", false, "imprime o SDP cru devolvido pela câmera")
	asJSON := flag.Bool("json", false, "imprime o resultado em JSON")
	flag.Parse()

	streamURL := *target
	if streamURL == "" && flag.NArg() > 0 {
		streamURL = flag.Arg(0)
	}
	if streamURL == "" {
		fmt.Fprintln(os.Stderr, "informe a url do stream com -url ou como primeiro argumento")
		flag.Usage()
		os.Exit(2)
	}

	if err := run(streamURL, *user, *pass, *timeout, *showSDP, *asJSON); err != nil {
		fmt.Fprintln(os.Stderr, "erro:", cli.ExplainFailure(err, cli.HasCredentials(streamURL, *user, *pass)))
		os.Exit(1)
	}
}

type report struct {
	URL     string  `json:"url"`
	Server  string  `json:"server,omitempty"`
	Methods string  `json:"methods,omitempty"`
	Medias  []media `json:"medias"`
	SDP     string  `json:"sdp,omitempty"`
}

type media struct {
	Type    string            `json:"type"`
	Codec   string            `json:"codec"`
	Payload uint8             `json:"payload_type"`
	Clock   int               `json:"clock_rate"`
	Control string            `json:"control,omitempty"`
	Params  map[string]string `json:"params,omitempty"`
}

func run(streamURL, user, pass string, timeout time.Duration, showSDP, asJSON bool) error {
	cli.WarnEmptyPassword(os.Stderr, streamURL, user, pass)

	target, err := cli.ResolveURL(streamURL, user, pass)
	if err != nil {
		return err
	}

	client := &gortsplib.Client{
		Scheme:      target.Scheme,
		Host:        target.Host,
		ReadTimeout: timeout,
	}
	if err := client.Start(); err != nil {
		return err
	}
	defer client.Close()

	rep := report{URL: cli.SafeURL(target)}

	// Algumas câmeras recusam OPTIONS mas aceitam DESCRIBE, então a falha aqui
	// é só um aviso.
	if res, err := client.Options(target); err == nil {
		rep.Server = cli.Header(res.Header["Server"])
		rep.Methods = cli.Header(res.Header["Public"])
	} else {
		fmt.Fprintf(os.Stderr, "aviso: OPTIONS falhou (%v), seguindo para o DESCRIBE\n", err)
	}

	desc, res, err := client.Describe(target)
	if err != nil {
		return err
	}
	if showSDP {
		rep.SDP = string(res.Body)
	}
	for _, m := range desc.Medias {
		for _, f := range m.Formats {
			rep.Medias = append(rep.Medias, media{
				Type:    string(m.Type),
				Codec:   f.Codec(),
				Payload: f.PayloadType(),
				Clock:   f.ClockRate(),
				Control: m.Control,
				Params:  f.FMTP(),
			})
		}
	}

	if asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(rep)
	}
	printReport(rep, showSDP)
	return nil
}

func printReport(rep report, showSDP bool) {
	fmt.Printf("Stream:   %s\n", rep.URL)
	fmt.Printf("Servidor: %s\n", cli.OrDash(rep.Server))
	fmt.Printf("Métodos:  %s\n", cli.OrDash(rep.Methods))

	fmt.Printf("\n%d trilha(s):\n\n", len(rep.Medias))
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	fmt.Fprintln(tw, "TIPO\tCODEC\tPAYLOAD\tCLOCK\tCONTROL")
	for _, m := range rep.Medias {
		fmt.Fprintf(tw, "%s\t%s\t%d\t%d Hz\t%s\n", m.Type, m.Codec, m.Payload, m.Clock, m.Control)
	}
	tw.Flush()

	for _, m := range rep.Medias {
		if len(m.Params) == 0 {
			continue
		}
		fmt.Printf("\nParâmetros de %s/%s:\n", m.Type, m.Codec)
		for k, v := range m.Params {
			fmt.Printf("  %s = %s\n", k, truncate(v, 60))
		}
	}

	if showSDP {
		fmt.Printf("\nSDP cru:\n%s\n", rep.SDP)
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
