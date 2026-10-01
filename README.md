# go-replay

Sistema de replay para câmeras RTSP. Cada câmera fica conectada o tempo todo e guarda os últimos minutos **em memória**. Não há gravação contínua em disco: o arquivo só nasce quando alguém pede o trecho.

O caso de uso é “acabou de acontecer alguma coisa — salva o último minuto de todas as câmeras”.

```
câmera RTSP ──► buffer em RAM (2 min) ──► botão / API
                                           │
                                           ▼
                              replay/20261001-180000/
                                camera-1.mp4
                                camera-2.mp4
                                replay.json
```

## Regras de negócio

Estas regras são o contrato do sistema. O código recusa o que está fora delas.

### Câmeras

1. **A unidade de cadastro é a URL RTSP**, não o aparelho físico. O stream principal (`/onvif1`) e o substream (`/onvif2`) da mesma câmera são duas câmeras distintas, cada uma com o próprio buffer.
2. **O catálogo é `cams.txt` na raiz**, uma URL por linha. Linhas em branco e começadas por `#` são ignoradas. Não se usa CSV: a senha de uma URL RTSP pode conter vírgula.
3. **A ordem do arquivo define o nome.** A primeira linha válida vira `camera-1`, a segunda `camera-2`, e assim por diante. Os nomes são fictícios e estáveis só enquanto a ordem do arquivo não muda.
4. **Arquivo ausente ou vazio não derruba o servidor.** Ele sobe, avisa no log e não grava nada até a primeira câmera ser cadastrada — pela interface ou editando `cams.txt` e reiniciando.
5. **URL inválida no arquivo é ignorada.** As outras câmeras seguem gravando. O aviso vai para o log.
6. **Cadastro pela API testa a conexão antes de persistir.** URL que não conecta não entra no arquivo. Duplicata (mesma URL sem a senha) é recusada.
7. **Credenciais não aparecem na interface nem no log.** O que se exibe é a URL sem usuário/senha. `cams.txt` fica de fora do git porque guarda senha.
8. **A senha pode ficar fora do arquivo.** `-user` / `-pass` (ou a env `RTSP_PASSWORD`) completam URLs que não trazem credenciais. Se a senha tem `$` ou `!`, a URL na linha de comando precisa de aspas simples, senão o shell a esvazia.

### Buffer em memória

9. **Cada câmera tem o próprio buffer**, padrão de **2 minutos**. O que sai da janela é descartado. O corte da janela só acontece num quadro-chave, para o que restar continuar decodificável.
10. **A gravação começa no instante em que a câmera entra** — na subida do servidor ou no `POST` de cadastro. Não existe modo “armada, esperando o botão”.
11. **A janela tem que ser maior que o trecho do replay.** Padrão: 2 min em memória, 1 min no arquivo. Sem essa folga o recorte não encontra um quadro-chave anterior à janela pedida.
12. **Reconexão substitui o buffer.** Quando a sessão RTSP cai, o processo tenta de novo sozinho. Os timestamps da câmera recomeçam, então o histórico antigo é jogado fora de propósito: misturar as duas linhas de tempo geraria um MP4 quebrado.
13. **Relógio da câmera andando para trás também zera o buffer.** O mesmo vale para falhas seguidas ao extrair o tempo de decodificação. Perde-se o histórico; a gravação volta no próximo quadro-chave.

### Replay

14. **Um clique recorta todas as câmeras no mesmo instante**, antes de escrever qualquer arquivo. A parte lenta é o disco; se o recorte fosse câmera a câmera, os trechos sairiam defasados pelo tempo de gravar o arquivo anterior.
15. **O trecho tem duração exata** (padrão: 60 s). A soma das durações dos quadros no MP4 é exatamente o pedido.
16. **O início não cai onde se pede.** Um MP4 precisa abrir num quadro-chave. O corte começa no último quadro-chave *anterior* à janela e segue por exatamente N segundos. O fim do trecho atrasa em até um intervalo de GOP (nesta câmera, ~1 s a 15 fps).
17. **Câmera sem vídeo suficiente fica de fora, as outras entram.** Se nenhuma tiver o trecho inteiro, a API responde 409 e nada é gravado. Se só uma falhar, o replay nasce com as que deram certo e a falha vai listada em `replay.json`.
18. **Cada replay é uma pasta nova**, identificada pelo instante (`replay/YYYYMMDD-HHMMSS/`). Dois cliques no mesmo segundo ganham sufixo (`…-2`). Dentro: um MP4 por câmera (`camera-1.mp4`, …) e um `replay.json` com o resumo.
19. **Não existe apagar, editar ou recortar de novo pela interface.** O que foi salvo permanece em disco até alguém remover a pasta à mão.

### Vídeo e áudio

20. **Vídeo aceito: H.264 ou H.265.** Outro codec, a sessão de vídeo falha.
21. **Áudio é opcional.** Só entra no MP4 se aquele stream publicar G.711 (PCMA/PCMU) ou AAC. Substream tipo `/onvif2` frequentemente não publica áudio — o arquivo sai mudo de propósito, não por falha do mux.
22. **G.711 vira PCM little-endian `sowt` no MP4.** O mux nativo grava `ipcm`, que a maior parte dos players trata como arquivo corrompido; o arquivo é reescrito para o formato que o VLC e o ffmpeg entendem.
23. **O relógio do G.711 segue o `rtpmap` do SDP quando ele é 8/16/32/48 kHz.** Payload estático 8 é 8000 Hz no RFC; firmware desta câmera anuncia `PCMA/16000` e envia nesse ritmo. Usar 8000 Hz distorce o som e escolhe o trecho errado.
24. **H.265 o navegador em geral não toca.** A página avisa e oferece o download. O arquivo é para abrir no VLC (ou equivalente). H.264 no stream da câmera é o caminho para o player HTML funcionar.

### Transporte RTSP

25. **Padrão: tenta TCP e cai para UDP** se a câmera recusar. Há firmware que confirma TCP com o perfil de UDP no header `Transport`; o cliente segue pelo campo interleaved em vez de desistir.

## Como usar

Go 1.26+. Sem banco, sem FFmpeg em runtime, sem Docker.

```bash
# interface em http://localhost:8080
go run ./cmd/server

# senha fora do arquivo (cams.txt só com rtsp://192.168.1.8:554/onvif1)
RTSP_PASSWORD='…' go run ./cmd/server -user admin

# janela e trecho diferentes
go run ./cmd/server -window 3m -clip 90s
```

Na página: acompanhe o buffer de cada câmera, adicione URLs e clique **Salvar replay** quando pelo menos uma tiver o trecho pronto.

### `cams.txt`

```
# uma url por linha; # e linhas vazias são ignoradas
rtsp://admin:senha@192.168.1.8:554/onvif1
rtsp://admin:senha@192.168.1.8:554/onvif2
```

Permissão do arquivo criado pela API: `0600`.

## Outros comandos

| Comando | Para quê |
| --- | --- |
| `go run ./cmd/server` | Buffer contínuo + interface + API |
| `go run ./cmd/record -url 'rtsp://…' -duration 10s` | Gravar um trecho avulso em MP4 |
| `go run ./cmd/inspect -url 'rtsp://…' -sdp` | Ver trilhas, codec e SDP |
| `go run ./cmd/discover` | Varrer a LAN por servidores RTSP (não descobre caminho `/onvif1` nem senha) |

## API

| Método | Rota | Efeito |
| --- | --- | --- |
| `GET` | `/` | Interface |
| `GET` | `/api/status` | Câmeras, buffer, quantas já têm o trecho pronto |
| `GET` | `/api/cameras` | Lista de câmeras |
| `POST` | `/api/cameras` | `{"url":"rtsp://…"}` — testa, persiste, começa a gravar |
| `GET` | `/api/replays` | Replays salvos, do mais recente ao mais antigo |
| `POST` | `/api/replays` | Recorta o trecho de todas e grava uma pasta nova |
| `GET` | `/clips/<id>/<arquivo>` | Arquivo MP4 |

## Arquitetura

```
cmd/server          HTTP, flags, ciclo de vida
internal/web        rotas e a página embutida
internal/replay     catálogo, buffer, recorte, MP4, reconexão
internal/camera     sessão RTSP (gortsplib), H.264/H.265, G.711/AAC
internal/discovery  varredura de rede (só o comando discover)
internal/cli        URL, senha, mensagens de erro da câmera
```

O buffer guarda access units já remontados, com DTS e flag de quadro-chave. O recorte escolhe o ponto de entrada, trunca a duração do último quadro para fechar o tempo pedido, alinha o áudio no mesmo intervalo e escreve um MP4 progressivo (`pmp4`). G.711 é decodificado para PCM 16-bit LE antes de entrar no arquivo.

## Flags do servidor

| Flag | Padrão | |
| --- | --- | --- |
| `-cams` | `cams.txt` | Catálogo |
| `-addr` | `:8080` | Endereço HTTP |
| `-out` | `replay` | Pasta dos arquivos |
| `-window` | `2m` | Buffer por câmera |
| `-clip` | `1m` | Trecho que o botão salva |
| `-user` / `-pass` | vazio / `RTSP_PASSWORD` | Credenciais para URLs sem usuário |
| `-transport` | `auto` | `auto`, `tcp` ou `udp` |
| `-timeout` | `5s` | Espera por dados da câmera |
| `-trace` | off | Log de cada request/resposta RTSP |
