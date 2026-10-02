package replay

import (
	"errors"
	"testing"
	"time"

	"github.com/FelippeRibeiro/go-replay/internal/camera"
)

// As imagens de teste são H.264 porque basta o primeiro byte de cada NAL para
// o buffer classificá-la; o conteúdo em si nunca é decodificado.
const (
	nalIDR    = 0x65 // tipo 5: quadro-chave
	nalSlice  = 0x41 // tipo 1: quadro comum
	nalSPS    = 0x67 // tipo 7
	nalPPS    = 0x68 // tipo 8
	testScale = 90000
	testStep  = testScale / 15 // 15 quadros por segundo
)

// fakeExtractor devolve o DTS que mandarmos, e falha quando pedido. O extrator
// de verdade depende de SPS válido, que não vem ao caso aqui.
type fakeExtractor struct {
	fail bool
	last int64
}

func (f *fakeExtractor) Extract(_ [][]byte, pts int64) (int64, error) {
	if f.fail {
		return 0, errors.New("DTS is greater than PTS")
	}
	f.last = pts
	return pts, nil
}

// newTestBuffer monta um buffer já com a configuração publicada e com o
// extrator trocado pelo de teste.
func newTestBuffer(window time.Duration) (*Buffer, *fakeExtractor) {
	buffer := NewBuffer(camera.Video{
		Codec:     camera.CodecH264,
		TimeScale: testScale,
		SPS:       []byte{nalSPS, 0x01},
		PPS:       []byte{nalPPS, 0x01},
	}, window)
	extractor := &fakeExtractor{}
	// A fábrica também é trocada, senão o recomeço traria de volta o extrator
	// real, que recusaria estas imagens sintéticas.
	buffer.newExtractor = func() dtsExtractor { return extractor }
	buffer.dts = extractor
	return buffer, extractor
}

// feed entrega quadros sequenciais, sendo quadro-chave a cada keyEvery.
func feedFrames(b *Buffer, from int64, frames, keyEvery int) int64 {
	pts := from
	for i := range frames {
		nal := byte(nalSlice)
		if i%keyEvery == 0 {
			nal = nalIDR
		}
		b.Add([][]byte{{nal, 0x00, 0x01, 0x02}}, pts)
		pts += testStep
	}
	return pts
}

func TestBufferDescartaForaDaJanela(t *testing.T) {
	buffer, _ := newTestBuffer(2 * time.Second)

	feedFrames(buffer, 0, 150, 15) // 10 segundos numa janela de 2

	stats := buffer.Stats()
	if stats.Resets != 0 || stats.Dropped != 0 {
		t.Fatalf("buffer saudável não deveria reiniciar nem descartar: %+v", stats)
	}
	// O corte acontece em quadro-chave, então sobra um pouco mais que a janela.
	if stats.Buffered < 2*time.Second || stats.Buffered > 3*time.Second {
		t.Errorf("em memória = %s, esperado entre 2s e 3s", stats.Buffered)
	}
	if stats.Bytes <= 0 {
		t.Error("o tamanho em memória deveria acompanhar as imagens guardadas")
	}
}

// Este é o caso que travava a gravação em produção: a câmera salta o relógio
// para trás e o buffer precisa recomeçar em vez de recusar tudo para sempre.
func TestBufferRecomecaQuandoORelogioVoltaAtras(t *testing.T) {
	buffer, _ := newTestBuffer(2 * time.Minute)

	pts := feedFrames(buffer, 1_000_000, 60, 15)
	if before := buffer.Stats().Samples; before == 0 {
		t.Fatal("nada foi guardado antes do salto")
	}

	// Salto de dez segundos para trás, como o de um relógio ressincronizado.
	buffer.Add([][]byte{{nalIDR, 0x00}}, pts-10*testScale)

	if resets := buffer.Stats().Resets; resets != 1 {
		t.Fatalf("recomeços = %d, esperado 1", resets)
	}

	// E o mais importante: volta a gravar na nova linha de tempo.
	feedFrames(buffer, pts-10*testScale, 60, 15)
	stats := buffer.Stats()
	if stats.Samples < 50 {
		t.Errorf("imagens guardadas após recomeçar = %d, esperado perto de 60", stats.Samples)
	}
	if stats.Buffered < 3*time.Second {
		t.Errorf("em memória após recomeçar = %s, esperado perto de 4s", stats.Buffered)
	}
}

func TestBufferRecomecaDepoisDeFalhasSeguidas(t *testing.T) {
	buffer, extractor := newTestBuffer(2 * time.Minute)

	pts := feedFrames(buffer, 0, 30, 15)

	extractor.fail = true
	pts = feedFrames(buffer, pts, maxConsecutiveFailures, 15)
	if resets := buffer.Stats().Resets; resets != 1 {
		t.Fatalf("recomeços = %d, esperado 1 após %d falhas seguidas",
			resets, maxConsecutiveFailures)
	}

	extractor.fail = false
	feedFrames(buffer, pts, 60, 15)
	if stats := buffer.Stats(); stats.Samples < 50 {
		t.Errorf("imagens guardadas após recomeçar = %d, esperado perto de 60", stats.Samples)
	}
}

// Falhas isoladas não devem jogar fora o histórico: só o recomeço faz isso, e
// ele é caro.
func TestBufferToleraFalhasIsoladas(t *testing.T) {
	buffer, extractor := newTestBuffer(2 * time.Minute)

	pts := feedFrames(buffer, 0, 60, 15)
	guardadas := buffer.Stats().Samples

	for range maxConsecutiveFailures - 1 {
		extractor.fail = true
		buffer.Add([][]byte{{nalSlice, 0x00}}, pts)
		extractor.fail = false
		pts += testStep
		buffer.Add([][]byte{{nalSlice, 0x00}}, pts)
		pts += testStep
	}

	stats := buffer.Stats()
	if stats.Resets != 0 {
		t.Errorf("recomeços = %d, esperado 0 com falhas intercaladas", stats.Resets)
	}
	if stats.Samples <= guardadas {
		t.Errorf("imagens = %d, deveria ter crescido a partir de %d", stats.Samples, guardadas)
	}
}

func TestClipTemADuracaoExata(t *testing.T) {
	buffer, _ := newTestBuffer(2 * time.Minute)

	feedFrames(buffer, 0, 15*90, 15) // 90 segundos

	clip, err := buffer.Clip(time.Minute)
	if err != nil {
		t.Fatalf("recortando um minuto: %v", err)
	}
	if clip.Duration != time.Minute {
		t.Errorf("duração = %s, esperado exatamente 1m", clip.Duration)
	}

	var total uint32
	for _, s := range clip.samples {
		total += s.Duration
	}
	if want := uint32(60 * testScale); total != want {
		t.Errorf("soma das durações = %d, esperado %d", total, want)
	}
	if clip.samples[0].IsNonSyncSample {
		t.Error("o clipe precisa começar num quadro-chave")
	}
}

func TestClipRecusaVideoInsuficiente(t *testing.T) {
	buffer, _ := newTestBuffer(2 * time.Minute)

	feedFrames(buffer, 0, 150, 15) // 10 segundos

	if _, err := buffer.Clip(time.Minute); !errors.Is(err, ErrNotEnoughVideo) {
		t.Errorf("erro = %v, esperado ErrNotEnoughVideo", err)
	}
}

func TestClipAlinhaAudioComOVideo(t *testing.T) {
	buffer, _ := newTestBuffer(2 * time.Minute)
	buffer.EnableAudio(camera.Audio{
		Codec:      camera.CodecPCMA,
		TimeScale:  8000,
		SampleRate: 8000,
		Channels:   1,
	})

	feedFrames(buffer, 0, 15*90, 15) // 90 segundos de vídeo

	pcm := make([]byte, 320) // 20 ms de PCM 16-bit mono a 8 kHz
	const audioStep int64 = 160
	for pts := int64(0); pts < 90*8000; pts += audioStep {
		buffer.AddAudio(pcm, pts)
	}

	clip, err := buffer.Clip(time.Minute)
	if err != nil {
		t.Fatalf("recortando: %v", err)
	}
	if clip.Duration != time.Minute {
		t.Errorf("duração de vídeo = %s, esperado 1m", clip.Duration)
	}
	if !clip.HasAudio() || clip.Audio != camera.CodecPCMA {
		t.Fatalf("áudio = %q (%d blocos), esperado PCMA", clip.Audio, len(clip.audio))
	}

	var total uint32
	for _, s := range clip.audio {
		total += s.Duration
	}
	if want := uint32(60 * 8000); total != want {
		t.Errorf("soma das durações de áudio = %d, esperado %d (60s a 8 kHz)", total, want)
	}
}

func TestClipInfereClockDoPCM16kHz(t *testing.T) {
	buffer, _ := newTestBuffer(2 * time.Minute)
	buffer.EnableAudio(camera.Audio{
		Codec:      camera.CodecPCMA,
		TimeScale:  8000, // o SDP/RFC mente; a taxa real vem do tamanho do bloco
		SampleRate: 8000,
		Channels:   1,
	})

	feedFrames(buffer, 0, 15*90, 15)

	pcm := make([]byte, 640) // 20 ms de PCM 16-bit mono a 16 kHz
	const audioStep int64 = 320
	for pts := int64(0); pts < 90*16000; pts += audioStep {
		buffer.AddAudio(pcm, pts)
	}

	clip, err := buffer.Clip(time.Minute)
	if err != nil {
		t.Fatalf("recortando: %v", err)
	}
	var total uint32
	for _, s := range clip.audio {
		total += s.Duration
	}
	if want := uint32(60 * 16000); total != want {
		t.Errorf("soma das durações de áudio = %d, esperado %d (60s a 16 kHz)", total, want)
	}
}
