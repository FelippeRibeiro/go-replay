package replay

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestCatalogArquivoInexistenteNaoEErro(t *testing.T) {
	catalog := NewCatalog(filepath.Join(t.TempDir(), "cams.txt"))

	if catalog.Exists() {
		t.Fatal("arquivo novo não deveria existir")
	}
	urls, err := catalog.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(urls) != 0 {
		t.Errorf("urls = %v, esperado lista vazia", urls)
	}
}

func TestCatalogIgnoraComentariosELinhasEmBranco(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cams.txt")
	body := catalogHeader + `
rtsp://a@192.168.1.8:554/onvif1

# uma câmera desligada
rtsp://b@192.168.1.9:554/stream
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	urls, err := NewCatalog(path).Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := []string{
		"rtsp://a@192.168.1.8:554/onvif1",
		"rtsp://b@192.168.1.9:554/stream",
	}
	if !slices.Equal(urls, want) {
		t.Errorf("urls = %v, esperado %v", urls, want)
	}
}

func TestCatalogAppendCriaArquivoComCabecalho(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cams.txt")
	catalog := NewCatalog(path)

	if err := catalog.Append("rtsp://a@192.168.1.8:554/onvif1"); err != nil {
		t.Fatal(err)
	}
	if err := catalog.Append("rtsp://b@192.168.1.9:554/stream"); err != nil {
		t.Fatal(err)
	}

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if !strings.HasPrefix(text, "# Câmeras do go-replay") {
		t.Errorf("arquivo deveria começar com o cabeçalho, veio:\n%s", text)
	}
	if strings.Count(text, catalogHeader) != 1 {
		t.Error("o cabeçalho deveria ser escrito só na criação")
	}

	urls, err := catalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(urls) != 2 {
		t.Errorf("urls = %v, esperado 2 linhas", urls)
	}
}
