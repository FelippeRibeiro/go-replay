// Command record grava um trecho de vídeo da câmera num arquivo MP4.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/FelippeRibeiro/go-replay/internal/camera"
	"github.com/FelippeRibeiro/go-replay/internal/cli"
	"github.com/FelippeRibeiro/go-replay/internal/replay"
)

func main() {
	target := flag.String("url", os.Getenv("RTSP_URL"), "url do stream, ex: rtsp://user:senha@192.168.1.8:554/onvif1 (ou a env RTSP_URL)")
	user := flag.String("user", "", "usuário, se preferir não embutir na url")
	pass := flag.String("pass", os.Getenv("RTSP_PASSWORD"), "senha, se preferir não embutir na url (ou a env RTSP_PASSWORD)")
	duration := flag.Duration("duration", 10*time.Second, "quanto tempo de vídeo gravar")
	outDir := flag.String("out", "replay", "pasta onde salvar a gravação")
	transport := flag.String("transport", camera.TransportAuto, "transporte da mídia: auto, tcp ou udp")
	timeout := flag.Duration("timeout", 5*time.Second, "tempo máximo de espera por dados")
	trace := flag.Bool("trace", false, "mostra as requisições e respostas RTSP")
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

	cli.WarnEmptyPassword(os.Stderr, streamURL, *user, *pass)
	resolved, err := cli.ResolveURL(streamURL, *user, *pass)
	if err != nil {
		log.Fatalln("erro:", err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	clip, err := record(ctx, camera.Options{
		URL:       resolved,
		Timeout:   *timeout,
		Transport: *transport,
		Logf:      log.Printf,
		Trace:     traceFunc(*trace),
	}, *duration)
	if err != nil {
		fmt.Fprintln(os.Stderr, "erro:", cli.ExplainFailure(err, cli.HasCredentials(streamURL, *user, *pass)))
		os.Exit(1)
	}

	path, size, err := save(clip, *outDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "erro:", err)
		os.Exit(1)
	}

	fmt.Printf("\nGravado:  %s\n", path)
	fmt.Printf("Codec:    %s\n", clip.Codec)
	if clip.HasAudio() {
		fmt.Printf("Áudio:    %s\n", clip.Audio)
	}
	fmt.Printf("Duração:  %.3fs em %d quadros\n", clip.Duration.Seconds(), clip.Frames())
	fmt.Printf("Tamanho:  %.2f MB\n", float64(size)/(1<<20))
}

// save escreve o clipe num arquivo nomeado pelo instante da gravação.
func save(clip *replay.Clip, dir string) (path string, size int64, err error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", 0, err
	}
	path = filepath.Join(dir, "record-"+time.Now().Format("20060102-150405")+".mp4")

	file, err := os.Create(path)
	if err != nil {
		return "", 0, err
	}
	defer file.Close()

	if err := clip.Encode(file); err != nil {
		os.Remove(path)
		return "", 0, err
	}
	stat, err := file.Stat()
	if err != nil {
		return "", 0, err
	}
	return path, stat.Size(), nil
}

// record acumula vídeo até completar a duração pedida e recorta o trecho.
//
// A janela em memória tem folga sobre a duração porque o corte precisa começar
// num quadro-chave anterior ao trecho desejado.
func record(ctx context.Context, opts camera.Options, duration time.Duration) (*replay.Clip, error) {
	stream, err := camera.Open(opts)
	if err != nil {
		return nil, err
	}
	defer stream.Close()

	video := stream.Video()
	buffer := replay.NewBuffer(video, duration+30*time.Second)
	if audio, ok := stream.Audio(); ok {
		buffer.EnableAudio(audio)
		log.Printf("gravando %s de %s+%s por %s", duration, video.Codec, audio.Codec, stream.Transport())
	} else {
		log.Printf("gravando %s de %s por %s (sem áudio)", duration, video.Codec, stream.Transport())
	}

	ready, cancel := context.WithCancel(ctx)
	defer cancel()

	lastLog := time.Now()
	err = stream.Run(ready, func(au [][]byte, pts int64) {
		buffer.Add(au, pts)
		buffered := buffer.Buffered()
		if time.Since(lastLog) >= time.Second {
			lastLog = time.Now()
			log.Printf("  %4.1fs de vídeo", buffered.Seconds())
		}
		// Para quando houver o trecho pedido mais a folga de um keyframe.
		if buffered >= duration+2*time.Second {
			cancel()
		}
	}, func(payload []byte, pts int64) {
		buffer.AddAudio(payload, pts)
	})
	if err != nil && ready.Err() == nil {
		return nil, err
	}

	clip, err := buffer.Clip(duration)
	if err != nil {
		return nil, fmt.Errorf("%w (diagnóstico: %+v)", err, stream.Stats())
	}
	return clip, nil
}

// traceFunc devolve o registrador de protocolo, ou nil quando desligado.
func traceFunc(enabled bool) func(string, ...any) {
	if !enabled {
		return nil
	}
	return log.Printf
}
