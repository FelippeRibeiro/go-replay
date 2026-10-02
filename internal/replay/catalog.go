package replay

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strings"
)

// catalogHeader é escrito quando o arquivo é criado, para que ele continue
// legível e editável à mão depois.
const catalogHeader = `# Câmeras do go-replay: uma url RTSP por linha.
# Linhas em branco e começadas por # são ignoradas.
# A ordem define o nome de cada câmera: a primeira é camera-1, a segunda camera-2.
`

// Catalog é o arquivo que lista as câmeras, normalmente cams.txt na raiz.
//
// Uma linha por câmera, e não valores separados por vírgula: uma url RTSP pode
// conter vírgula na senha, e uma linha por item é mais fácil de editar.
type Catalog struct {
	path string
}

func NewCatalog(path string) *Catalog {
	return &Catalog{path: path}
}

func (c *Catalog) Path() string { return c.path }

// Exists informa se o arquivo já foi criado.
func (c *Catalog) Exists() bool {
	_, err := os.Stat(c.path)
	return err == nil
}

// Load devolve as urls na ordem em que aparecem. Arquivo inexistente não é
// erro: é só uma lista vazia, e quem chama avisa o usuário.
func (c *Catalog) Load() ([]string, error) {
	file, err := os.Open(c.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var urls []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		urls = append(urls, line)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("lendo %s: %w", c.path, err)
	}
	return urls, nil
}

// Append acrescenta uma url ao fim do arquivo, criando-o se preciso.
func (c *Catalog) Append(target string) error {
	needsHeader := !c.Exists()

	file, err := os.OpenFile(c.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("abrindo %s: %w", c.path, err)
	}
	defer file.Close()

	text := target + "\n"
	if needsHeader {
		text = catalogHeader + text
	}
	if _, err := file.WriteString(text); err != nil {
		return fmt.Errorf("escrevendo em %s: %w", c.path, err)
	}
	return nil
}
