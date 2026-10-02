package replay

import (
	"bytes"
	"encoding/binary"
	"fmt"
)

const pcmCSize = 14 // 4 size + 4 type + 4 fullbox + 2 payload

var mp4Containers = map[string]bool{
	"moov": true,
	"trak": true,
	"mdia": true,
	"minf": true,
	"stbl": true,
	"stsd": true,
}

// rewriteIPCMtoSOWT troca o sample entry ISO ipcm (que a maior parte dos
// players recusa e trata como arquivo corrompido) pelo sowt do QuickTime,
// que o VLC, o ffmpeg e o Windows entendem. O pcmC deixa de fazer sentido
// junto do sowt, então ele é removido e os offsets do mdat são corrigidos.
func rewriteIPCMtoSOWT(in []byte) ([]byte, error) {
	ipcm := bytes.Index(in, []byte("ipcm"))
	if ipcm < 4 {
		return in, nil
	}
	pcmC := bytes.Index(in, []byte("pcmC"))
	if pcmC < 4 {
		return in, nil
	}
	pcmCOff := pcmC - 4
	if binary.BigEndian.Uint32(in[pcmCOff:pcmCOff+4]) != pcmCSize {
		return nil, fmt.Errorf("caixa pcmC com tamanho inesperado")
	}

	out := append([]byte(nil), in...)
	copy(out[ipcm:ipcm+4], []byte("sowt"))

	ipcmSizeOff := ipcm - 4
	ipcmSize := binary.BigEndian.Uint32(out[ipcmSizeOff : ipcmSizeOff+4])
	if ipcmSize < pcmCSize+8 {
		return nil, fmt.Errorf("caixa ipcm pequena demais")
	}
	binary.BigEndian.PutUint32(out[ipcmSizeOff:ipcmSizeOff+4], ipcmSize-pcmCSize)

	if err := shrinkBoxesContaining(out, pcmCOff, pcmCSize); err != nil {
		return nil, err
	}
	shiftChunkOffsets(out, pcmCOff, -pcmCSize)

	patched := make([]byte, 0, len(out)-pcmCSize)
	patched = append(patched, out[:pcmCOff]...)
	patched = append(patched, out[pcmCOff+pcmCSize:]...)
	return patched, nil
}

func shrinkBoxesContaining(buf []byte, target, drop int) error {
	for _, off := range boxesContaining(buf, 0, indexOrEnd(buf, "mdat"), target) {
		size := binary.BigEndian.Uint32(buf[off : off+4])
		if size < uint32(drop)+8 {
			typ := string(buf[off+4 : off+8])
			return fmt.Errorf("caixa %s pequena demais para remover pcmC", typ)
		}
		binary.BigEndian.PutUint32(buf[off:off+4], size-uint32(drop))
	}
	return nil
}

func indexOrEnd(buf []byte, typ string) int {
	i := bytes.Index(buf, []byte(typ))
	if i < 4 {
		return len(buf)
	}
	return i - 4
}

func boxesContaining(buf []byte, start, end, target int) []int {
	var found []int
	off := start
	for off+8 <= end && off+8 <= len(buf) {
		size := binary.BigEndian.Uint32(buf[off : off+4])
		if size < 8 || off+int(size) > len(buf) {
			break
		}
		typ := string(buf[off+4 : off+8])
		boxEnd := off + int(size)
		if off <= target && target < boxEnd {
			if mp4Containers[typ] {
				found = append(found, off)
				child := off + 8
				if typ == "stsd" {
					child = off + 16
				}
				found = append(found, boxesContaining(buf, child, boxEnd, target)...)
			}
		}
		off = boxEnd
	}
	return found
}

func shiftChunkOffsets(buf []byte, removedAt, delta int) {
	_ = walkBoxes(buf, 0, len(buf), func(off int, size uint32, typ string) error {
		if typ != "stco" || size < 16 {
			return nil
		}
		// stco: 8 header + 4 fullbox + 4 entry count + 4*n offsets
		count := binary.BigEndian.Uint32(buf[off+12 : off+16])
		base := off + 16
		for i := 0; i < int(count); i++ {
			p := base + i*4
			if p+4 > off+int(size) {
				break
			}
			v := binary.BigEndian.Uint32(buf[p : p+4])
			if int(v) > removedAt {
				binary.BigEndian.PutUint32(buf[p:p+4], uint32(int(v)+delta))
			}
		}
		return nil
	})
}

func walkBoxes(buf []byte, start, end int, fn func(off int, size uint32, typ string) error) error {
	off := start
	for off+8 <= end && off+8 <= len(buf) {
		size := binary.BigEndian.Uint32(buf[off : off+4])
		if size < 8 || off+int(size) > len(buf) {
			return nil
		}
		typ := string(buf[off+4 : off+8])
		if err := fn(off, size, typ); err != nil {
			return err
		}
		if mp4Containers[typ] {
			if err := walkBoxes(buf, off+8, off+int(size), fn); err != nil {
				return err
			}
		}
		off += int(size)
	}
	return nil
}
