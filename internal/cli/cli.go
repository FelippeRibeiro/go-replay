// Package cli reúne o que os comandos têm em comum ao falar com câmeras.
package cli

import (
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"

	"github.com/bluenviron/gortsplib/v5/pkg/base"
	"github.com/bluenviron/gortsplib/v5/pkg/liberrors"
)

// WarnEmptyPassword avisa sobre o caso em que a senha some antes de chegar ao
// programa: senhas com $ ou ! são expandidas pelo shell quando a url não está
// entre aspas simples.
func WarnEmptyPassword(w io.Writer, target, user, pass string) {
	if user != "" || pass != "" {
		return
	}
	u, err := base.ParseURL(target)
	if err != nil || u.User == nil || u.User.Username() == "" {
		return
	}
	if password, _ := u.User.Password(); password != "" {
		return
	}
	fmt.Fprintf(w, "aviso: a url tem usuário %q mas senha vazia\n", u.User.Username())
	fmt.Fprintln(w, "       se a senha tem $ ou !, use aspas simples: --url 'rtsp://...'")
}

// ResolveURL monta a URL final do stream, combinando as credenciais embutidas
// com as informadas por flag. As flags têm preferência campo a campo, para que
// passar só a senha não apague o usuário que estava na url.
func ResolveURL(target, user, pass string) (*base.URL, error) {
	u, err := base.ParseURL(target)
	if err != nil {
		return nil, err
	}

	urlUser, urlPass := "", ""
	if u.User != nil {
		urlUser = u.User.Username()
		urlPass, _ = u.User.Password()
	}
	if user == "" {
		user = urlUser
	}
	if pass == "" {
		pass = urlPass
	}

	u.User = nil
	if user != "" {
		u.User = url.UserPassword(user, pass)
	}
	return u, nil
}

// SafeURL devolve a url sem credenciais, segura para exibir e registrar.
func SafeURL(u *base.URL) string {
	clean := *u
	clean.User = nil
	return (*url.URL)(&clean).String()
}

// HasCredentials informa se há usuário definido, na url ou nas flags.
func HasCredentials(target, user, pass string) bool {
	if user != "" || pass != "" {
		return true
	}
	u, err := base.ParseURL(target)
	return err == nil && u.User != nil && u.User.Username() != ""
}

// ExplainFailure traduz a falha de uma requisição em algo acionável. Nem todo
// firmware responde 401 para credencial errada: alguns devolvem 400 ou 403.
func ExplainFailure(err error, hasCredentials bool) error {
	var badStatus liberrors.ErrClientBadStatusCode
	if !errors.As(err, &badStatus) {
		return err
	}
	switch badStatus.Code {
	case base.StatusUnauthorized:
		if !hasCredentials {
			return fmt.Errorf("%w; informe as credenciais na url ou em -user/-pass", err)
		}
		return fmt.Errorf("%w; usuário ou senha incorretos", err)
	case base.StatusBadRequest, base.StatusForbidden:
		if hasCredentials {
			return fmt.Errorf("%w; esse status normalmente significa usuário ou senha incorretos", err)
		}
	case base.StatusNotFound:
		return fmt.Errorf("%w; o caminho do stream provavelmente está errado", err)
	}
	return err
}

// Header junta os valores de um header que pode aparecer repetido.
func Header(v base.HeaderValue) string {
	return strings.Join(v, ", ")
}

// OrDash troca string vazia por um traço, para a saída em tabela não ficar com
// colunas em branco.
func OrDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
