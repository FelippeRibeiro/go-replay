// Command server acompanha várias câmeras ao mesmo tempo, mantendo os últimos
// minutos de cada uma em memória, e serve a interface que salva o replay de
// todas de uma vez.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/FelippeRibeiro/go-replay/internal/camera"
	"github.com/FelippeRibeiro/go-replay/internal/replay"
	"github.com/FelippeRibeiro/go-replay/internal/web"
)

func main() {
	camsFile := flag.String("cams", "cams.txt", "arquivo com as urls das câmeras, uma por linha")
	user := flag.String("user", "", "usuário aplicado às urls que não trazem credenciais")
	pass := flag.String("pass", os.Getenv("RTSP_PASSWORD"), "senha aplicada às urls que não trazem credenciais (ou a env RTSP_PASSWORD)")
	addr := flag.String("addr", ":8080", "endereço do servidor web")
	outDir := flag.String("out", "replay", "pasta onde os replays são salvos")
	window := flag.Duration("window", 2*time.Minute, "quanto vídeo manter em memória por câmera")
	clipSpan := flag.Duration("clip", time.Minute, "tamanho do trecho que o botão salva de cada câmera")
	transport := flag.String("transport", camera.TransportAuto, "transporte da mídia: auto, tcp ou udp")
	timeout := flag.Duration("timeout", 5*time.Second, "tempo máximo de espera por dados")
	trace := flag.Bool("trace", false, "mostra as requisições e respostas RTSP")
	flag.Parse()

	if *window <= *clipSpan {
		fmt.Fprintf(os.Stderr, "a janela em memória (%s) precisa ser maior que o trecho do replay (%s)\n", *window, *clipSpan)
		os.Exit(2)
	}

	store, err := replay.NewStore(*outDir)
	if err != nil {
		log.Fatalln("erro:", err)
	}
	catalog := replay.NewCatalog(*camsFile)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	manager := replay.NewManager(catalog, camera.Options{
		Timeout:   *timeout,
		Transport: *transport,
		Logf:      log.Printf,
		Trace:     traceFunc(*trace),
	}, *window, *user, *pass)

	// Sem câmeras o servidor sobe de todo jeito: é pela interface que a
	// primeira câmera é cadastrada.
	if err := manager.Start(ctx); errors.Is(err, replay.ErrNoCameras) {
		warnNoCameras(catalog)
	} else if err != nil {
		log.Fatalln("erro:", err)
	}

	server := &http.Server{
		Addr:    *addr,
		Handler: web.NewServer(manager, store, *clipSpan).Handler(),
	}

	// Encerra o servidor junto com o sinal, para a porta não ficar presa.
	go func() {
		<-ctx.Done()
		shutdown, stop := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer stop()
		server.Shutdown(shutdown)
	}()

	log.Printf("janela de %s por câmera, replay de %s, replays em %s/", *window, *clipSpan, store.Dir())
	log.Printf("interface em http://localhost%s", *addr)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalln("erro:", err)
	}
}

// warnNoCameras explica o que fazer quando o catálogo não existe ou está vazio.
func warnNoCameras(catalog *replay.Catalog) {
	if catalog.Exists() {
		log.Printf("aviso: %s não lista nenhuma câmera", catalog.Path())
	} else {
		log.Printf("aviso: %s não existe", catalog.Path())
	}
	log.Printf("       nada será gravado até cadastrar a primeira câmera")
	log.Printf("       cadastre pela interface web, ou escreva uma url RTSP por linha em %s", catalog.Path())
}

// traceFunc devolve o registrador de protocolo, ou nil quando desligado.
func traceFunc(enabled bool) func(string, ...any) {
	if !enabled {
		return nil
	}
	return log.Printf
}
