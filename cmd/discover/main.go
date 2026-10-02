package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/netip"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/FelippeRibeiro/go-replay/internal/discovery"
)

func main() {
	cidrs := flag.String("cidr", "", "sub-redes a varrer, separadas por vírgula (padrão: as redes locais)")
	portList := flag.String("ports", joinInts(discovery.DefaultPorts), "portas RTSP a testar, separadas por vírgula")
	timeout := flag.Duration("timeout", 2*time.Second, "tempo máximo por tentativa de conexão")
	workers := flag.Int("workers", 256, "quantas sondagens simultâneas")
	asJSON := flag.Bool("json", false, "imprime o resultado em JSON")
	flag.Parse()

	if err := run(*cidrs, *portList, *timeout, *workers, *asJSON); err != nil {
		fmt.Fprintln(os.Stderr, "erro:", err)
		os.Exit(1)
	}
}

func run(cidrs, portList string, timeout time.Duration, workers int, asJSON bool) error {
	prefixes, err := discovery.ResolvePrefixes(cidrs)
	if err != nil {
		return err
	}
	ports, err := discovery.ParsePorts(portList)
	if err != nil {
		return err
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	fmt.Fprintf(os.Stderr, "varrendo %s nas portas %s\n", joinPrefixes(prefixes), joinInts(ports))
	start := time.Now()

	devices, err := discovery.Scan(ctx, discovery.Options{
		Prefixes:   prefixes,
		Ports:      ports,
		Timeout:    timeout,
		Workers:    workers,
		OnProgress: progressPrinter(),
	})
	fmt.Fprint(os.Stderr, "\r\033[K")
	if err != nil && len(devices) == 0 {
		return err
	}
	fmt.Fprintf(os.Stderr, "%d dispositivo(s) em %s\n\n", len(devices), time.Since(start).Round(time.Millisecond))

	if asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(devices)
	}
	printTable(devices)
	return nil
}

func printTable(devices []discovery.Device) {
	if len(devices) == 0 {
		fmt.Println("Nenhum servidor RTSP respondeu. Confira se as câmeras estão na mesma rede")
		fmt.Println("e tente outras portas com -ports.")
		return
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	fmt.Fprintln(tw, "URL\tLATÊNCIA\tAUTENTICAÇÃO\tSERVIDOR\tTRILHAS")
	for _, dev := range devices {
		auth := "aberto"
		if dev.RequiresAuth {
			auth = "exige senha"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
			dev.URL,
			dev.RTT.Round(time.Millisecond),
			auth,
			orDash(dev.Server),
			orDash(strings.Join(dev.Medias, " ")),
		)
	}
	tw.Flush()
}

// progressPrinter devolve um callback que atualiza uma única linha no stderr.
// Ele é chamado de várias goroutines, então só usa escritas idempotentes.
func progressPrinter() func(done, total int) {
	var last atomic.Int64
	return func(done, total int) {
		now := time.Now().UnixMilli()
		if done < total && now-last.Load() < 100 {
			return
		}
		last.Store(now)
		fmt.Fprintf(os.Stderr, "\r\033[Ksondando %d/%d", done, total)
	}
}

func joinInts(values []int) string {
	parts := make([]string, len(values))
	for i, v := range values {
		parts[i] = strconv.Itoa(v)
	}
	return strings.Join(parts, ",")
}

func joinPrefixes(prefixes []netip.Prefix) string {
	parts := make([]string, len(prefixes))
	for i, p := range prefixes {
		parts[i] = p.String()
	}
	return strings.Join(parts, ", ")
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
