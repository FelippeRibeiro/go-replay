package camera

import (
	"strings"

	"github.com/bluenviron/gortsplib/v5/pkg/base"
)

// udpProfile é como o header Transport começa quando a mídia vem por UDP.
const udpProfile = "RTP/AVP;"

// normalizeTransport conserta a resposta de SETUP de câmeras que confirmam o
// transporte TCP mas anunciam o perfil do UDP, como em:
//
//	RTP/AVP;unicast;destination=...;interleaved=0-1
//
// O campo interleaved só existe quando a mídia vem pela conexão TCP, então a
// resposta se contradiz. A biblioteca segue o padrão e recusa a sessão; aqui a
// contradição é resolvida a favor do interleaved, que é o que o firmware faz de
// fato. Devolve true quando houve correção.
func normalizeTransport(res *base.Response) bool {
	values, ok := res.Header["Transport"]
	if !ok {
		return false
	}
	fixed := false
	for i, value := range values {
		if strings.HasPrefix(value, udpProfile) && strings.Contains(value, "interleaved=") {
			values[i] = "RTP/AVP/TCP;" + value[len(udpProfile):]
			fixed = true
		}
	}
	return fixed
}
