package replay

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStoreListaReplaysPeloResumo(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}

	first := ReplayInfo{
		ID:        "20261001-120000",
		CreatedAt: time.Date(2026, 10, 1, 12, 0, 0, 0, time.Local),
		Clips: []ClipInfo{{
			Camera: "camera-1",
			File:   "camera-1.mp4",
			URL:    "/clips/20261001-120000/camera-1.mp4",
			Size:   100,
		}},
	}
	second := ReplayInfo{
		ID:        "20261001-130000",
		CreatedAt: time.Date(2026, 10, 1, 13, 0, 0, 0, time.Local),
		Clips: []ClipInfo{
			{Camera: "camera-1", File: "camera-1.mp4", URL: "/clips/20261001-130000/camera-1.mp4", Size: 200},
			{Camera: "camera-2", File: "camera-2.mp4", URL: "/clips/20261001-130000/camera-2.mp4", Size: 300},
		},
	}
	writeTestReplay(t, dir, first)
	writeTestReplay(t, dir, second)

	replays, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(replays) != 2 {
		t.Fatalf("replays = %d, esperado 2", len(replays))
	}
	if replays[0].ID != second.ID {
		t.Errorf("primeiro = %s, esperado o mais recente %s", replays[0].ID, second.ID)
	}
	if len(replays[0].Clips) != 2 {
		t.Errorf("clipes do replay recente = %d, esperado 2", len(replays[0].Clips))
	}
}

func TestStoreCriaPastaPorIdUnico(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 10, 1, 17, 0, 0, 0, time.Local)
	id1, dir1, err := store.newReplayDir(now)
	if err != nil {
		t.Fatal(err)
	}
	id2, dir2, err := store.newReplayDir(now)
	if err != nil {
		t.Fatal(err)
	}
	if id1 == id2 || dir1 == dir2 {
		t.Fatalf("dois replays no mesmo segundo não podem dividir a pasta: %s", id1)
	}
	if id1 != "20261001-170000" {
		t.Errorf("id = %s, esperado 20261001-170000", id1)
	}
	if id2 != "20261001-170000-2" {
		t.Errorf("id do segundo = %s, esperado sufixo", id2)
	}
}

func TestStoreListaPastaSemResumo(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}

	replayDir := filepath.Join(dir, "20261001-170000")
	if err := os.Mkdir(replayDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(replayDir, "camera-1.mp4"), []byte("fake"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(replayDir, "camera-2.mp4"), []byte("fake"), 0o644); err != nil {
		t.Fatal(err)
	}

	replays, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(replays) != 1 || len(replays[0].Clips) != 2 {
		t.Fatalf("replays = %+v, esperado 1 replay com 2 câmeras", replays)
	}
	if replays[0].Clips[0].Camera != "camera-1" && replays[0].Clips[1].Camera != "camera-1" {
		t.Errorf("nomes = %+v, esperado camera-1 e camera-2", replays[0].Clips)
	}
}

func writeTestReplay(t *testing.T, root string, info ReplayInfo) {
	t.Helper()
	dir := filepath.Join(root, info.ID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeMetadata(dir, info); err != nil {
		t.Fatal(err)
	}
}
