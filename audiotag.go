package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"os"
	"path/filepath"
	"strings"

	"github.com/bogem/id3v2"
	"github.com/gcottom/mp4meta"
)

// AudioTag abstracts over the tag formats this tool supports (ID3v2 for
// .mp3, MP4 atoms for .m4a) so the rest of the program can edit either
// without caring which one it's dealing with.
type AudioTag interface {
	Title() string
	SetTitle(string)
	Artist() string
	SetArtist(string)
	Album() string
	SetAlbum(string)
	SetCoverArt(data []byte, mimeType string)
	Save() error
	Close() error
}

// isAudioFile reports whether name has a supported extension.
func isAudioFile(name string) bool {
	name = strings.ToLower(name)
	return strings.HasSuffix(name, ".mp3") || strings.HasSuffix(name, ".m4a")
}

// openAudioTag opens path, dispatching to the right tag format by extension.
func openAudioTag(path string) (AudioTag, error) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".m4a":
		return openM4ATag(path)
	default:
		return openMP3Tag(path)
	}
}

// ---- MP3 (ID3v2) ----

type mp3Tag struct {
	tag *id3v2.Tag
}

func openMP3Tag(path string) (AudioTag, error) {
	tag, err := id3v2.Open(path, id3v2.Options{Parse: true})
	if err != nil {
		return nil, err
	}
	if tag == nil {
		return nil, fmt.Errorf("file doesn't exist or is empty")
	}
	return &mp3Tag{tag: tag}, nil
}

func (m *mp3Tag) Title() string      { return m.tag.Title() }
func (m *mp3Tag) SetTitle(v string)  { m.tag.SetTitle(v) }
func (m *mp3Tag) Artist() string     { return m.tag.Artist() }
func (m *mp3Tag) SetArtist(v string) { m.tag.SetArtist(v) }
func (m *mp3Tag) Album() string      { return m.tag.Album() }
func (m *mp3Tag) SetAlbum(v string)  { m.tag.SetAlbum(v) }
func (m *mp3Tag) Save() error        { return m.tag.Save() }
func (m *mp3Tag) Close() error       { return m.tag.Close() }

func (m *mp3Tag) SetCoverArt(data []byte, mimeType string) {
	coverArt := id3v2.PictureFrame{
		Encoding:    id3v2.EncodingUTF8,
		MimeType:    mimeType,
		PictureType: id3v2.PTFrontCover,
		Description: "Front cover",
		Picture:     data,
	}
	m.tag.DeleteFrames(m.tag.CommonID("Attached picture"))
	m.tag.AddAttachedPicture(coverArt)
}

// ---- M4A (MP4 atoms) ----
//
// Uses gcottom/mp4meta (built on abema/go-mp4) rather than go-mp4tag:
// go-mp4tag's box parser doesn't understand ISO-BMFF's 64-bit "extended
// size" box header (size field == 1, real size in the following 8 bytes),
// which some encoders use for mdat. That mis-parses the file and fails
// with "moov box not present" even though the file is perfectly valid.

type m4aTag struct {
	path string
	tag  *mp4meta.MP4Tag
}

func openM4ATag(path string) (AudioTag, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	tag, err := mp4meta.ReadMP4(f)
	if err != nil {
		return nil, err
	}
	return &m4aTag{path: path, tag: tag}, nil
}

func (m *m4aTag) Title() string      { return m.tag.GetTitle() }
func (m *m4aTag) SetTitle(v string)  { m.tag.SetTitle(v) }
func (m *m4aTag) Artist() string     { return m.tag.GetArtist() }
func (m *m4aTag) SetArtist(v string) { m.tag.SetArtist(v) }
func (m *m4aTag) Album() string      { return m.tag.GetAlbum() }
func (m *m4aTag) SetAlbum(v string)  { m.tag.SetAlbum(v) }
func (m *m4aTag) Close() error       { return nil }

func (m *m4aTag) SetCoverArt(data []byte, mimeType string) {
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		fmt.Println("Error decoding cover art:", err)
		return
	}
	m.tag.SetCoverArt(&img)
}

// Save re-muxes the file into a temp file with the updated tags, then
// swaps it in atomically so a failed write never corrupts the original.
func (m *m4aTag) Save() error {
	src, err := os.Open(m.path)
	if err != nil {
		return err
	}
	defer src.Close()

	tmpPath := m.path + ".tmp"
	out, err := os.Create(tmpPath)
	if err != nil {
		return err
	}

	if err := mp4meta.SaveMP4(src, out, m.tag); err != nil {
		out.Close()
		os.Remove(tmpPath)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmpPath)
		return err
	}

	if err := fixBareMetaBox(tmpPath); err != nil {
		os.Remove(tmpPath)
		return err
	}

	if err := fixChunkOffsets(m.path, tmpPath); err != nil {
		os.Remove(tmpPath)
		return err
	}

	return os.Rename(tmpPath, m.path)
}

// fixBareMetaBox normalizes the moov/udta/meta atom into a proper ISO-BMFF
// FullBox (4-byte version+flags before its children) if it isn't one
// already. Some non-Apple encoders write "meta" as a legacy bare QuickTime
// box; mp4meta preserves whatever form the source file used, and lenient
// readers like ffprobe tolerate it, but Apple's own metadata readers (Quick
// Look, Music.app, Spotlight) silently show no tags at all for a bare meta
// box. Only the ordinary 32-bit box-size form is patched — if moov/udta/meta
// use the rare 64-bit extended size, the file is left untouched.
func fixBareMetaBox(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	moovOff, moovHdr, moovSize, ok := findBox(data, 0, len(data), "moov")
	if !ok || moovHdr != 8 {
		return nil
	}
	udtaOff, udtaHdr, udtaSize, ok := findBox(data, moovOff+moovHdr, moovOff+moovSize, "udta")
	if !ok || udtaHdr != 8 {
		return nil
	}
	metaOff, metaHdr, _, ok := findBox(data, udtaOff+udtaHdr, udtaOff+udtaSize, "meta")
	if !ok || metaHdr != 8 {
		return nil
	}

	childStart := metaOff + metaHdr
	if !looksLikeBoxHeader(data[childStart:]) {
		return nil // already a FullBox (or unrecognized) — leave it alone
	}

	patched := make([]byte, 0, len(data)+4)
	patched = append(patched, data[:childStart]...)
	patched = append(patched, 0, 0, 0, 0) // version(1) + flags(3), both zero
	patched = append(patched, data[childStart:]...)

	growBoxSize(patched, metaOff, 4)
	growBoxSize(patched, udtaOff, 4)
	growBoxSize(patched, moovOff, 4)

	return os.WriteFile(path, patched, 0644)
}

// looksLikeBoxHeader reports whether b starts with a plausible box header
// (size prefix in range, printable fourcc), which for meta's first child
// means meta itself has no FullBox version/flags before it.
func looksLikeBoxHeader(b []byte) bool {
	if len(b) < 8 {
		return false
	}
	size := binary.BigEndian.Uint32(b[:4])
	if size < 8 || uint64(size) > uint64(len(b)) {
		return false
	}
	for _, c := range b[4:8] {
		if c < 0x20 || c > 0x7e {
			return false
		}
	}
	return true
}

// findBox scans the sibling boxes in data[start:end] for one with the given
// fourcc, returning its offset, header size (8, or 16 for a 64-bit extended
// size), and total size (header + payload).
func findBox(data []byte, start, end int, fourcc string) (offset, hdrSize, size int, ok bool) {
	pos := start
	for pos+8 <= end {
		size32 := binary.BigEndian.Uint32(data[pos : pos+4])
		typ := string(data[pos+4 : pos+8])
		hdr := 8
		sz := int(size32)
		switch size32 {
		case 1:
			if pos+16 > end {
				return 0, 0, 0, false
			}
			sz = int(binary.BigEndian.Uint64(data[pos+8 : pos+16]))
			hdr = 16
		case 0:
			sz = end - pos
		}
		if sz < hdr || pos+sz > end {
			return 0, 0, 0, false
		}
		if typ == fourcc {
			return pos, hdr, sz, true
		}
		pos += sz
	}
	return 0, 0, 0, false
}

// growBoxSize adds delta to the ordinary 32-bit size field of the box at
// offset. Callers only pass offsets from findBox results whose hdrSize was
// confirmed to be 8, i.e. using the 32-bit size form.
func growBoxSize(data []byte, offset int, delta uint32) {
	size := binary.BigEndian.Uint32(data[offset : offset+4])
	binary.BigEndian.PutUint32(data[offset:offset+4], size+delta)
}

// fixChunkOffsets repairs stco/co64 chunk-offset tables that mp4meta's
// SaveMP4 sometimes recomputes incorrectly once moov grows (observed as a
// constant per-file byte error applied to every entry, landing shortly
// before the real mdat payload and making the audio undecodable even
// though the sample data itself is untouched). It measures the true shift
// by comparing mdat's position in the original file against its position
// in the freshly-written one, then rewrites every chunk offset as
// (original offset + verified shift) — ignoring whatever mp4meta computed.
func fixChunkOffsets(origPath, newPath string) error {
	origData, err := os.ReadFile(origPath)
	if err != nil {
		return err
	}
	newData, err := os.ReadFile(newPath)
	if err != nil {
		return err
	}

	origMdatOff, origMdatHdr, _, ok := findBox(origData, 0, len(origData), "mdat")
	if !ok {
		return fmt.Errorf("original file has no mdat box")
	}
	newMdatOff, newMdatHdr, _, ok := findBox(newData, 0, len(newData), "mdat")
	if !ok {
		return fmt.Errorf("saved file has no mdat box")
	}
	shift := int64(newMdatOff+newMdatHdr) - int64(origMdatOff+origMdatHdr)
	if shift == 0 {
		return nil
	}

	origMoovOff, origMoovHdr, origMoovSize, ok := findBox(origData, 0, len(origData), "moov")
	if !ok {
		return fmt.Errorf("original file has no moov box")
	}
	newMoovOff, newMoovHdr, newMoovSize, ok := findBox(newData, 0, len(newData), "moov")
	if !ok {
		return fmt.Errorf("saved file has no moov box")
	}

	origTables := findChunkOffsetTables(origData, origMoovOff+origMoovHdr, origMoovOff+origMoovSize)
	newTables := findChunkOffsetTables(newData, newMoovOff+newMoovHdr, newMoovOff+newMoovSize)
	if len(origTables) != len(newTables) {
		return fmt.Errorf("chunk-offset table count changed (%d -> %d), refusing to patch", len(origTables), len(newTables))
	}

	for i, newLoc := range newTables {
		origEntries := readChunkOffsets(origData, origTables[i])
		newEntries := readChunkOffsets(newData, newLoc)
		if len(origEntries) != len(newEntries) {
			return fmt.Errorf("chunk-offset entry count mismatch, refusing to patch")
		}

		fixed := make([]uint64, len(origEntries))
		for j, v := range origEntries {
			fixed[j] = uint64(int64(v) + shift)
		}
		if !newLoc.is64 {
			for _, v := range fixed {
				if v > 0xFFFFFFFF {
					return fmt.Errorf("corrected offset overflows 32-bit stco table")
				}
			}
		}
		writeChunkOffsets(newData, newLoc, fixed)
	}

	return os.WriteFile(newPath, newData, 0644)
}

// chunkOffsetContainers are the moov descendants that must be walked to
// find stco/co64 boxes; other box types (mdat, free, udta, stsd, ...) are
// left alone so their raw bytes never get misread as box headers.
var chunkOffsetContainers = map[string]bool{
	"trak": true, "mdia": true, "minf": true, "stbl": true,
}

// stcoLocation is the exact position and word size of one chunk-offset
// table (stco: 32-bit entries, co64: 64-bit entries), so its entries can be
// read or overwritten in place.
type stcoLocation struct {
	entriesOff int
	count      int
	is64       bool
}

// findChunkOffsetTables recursively walks data[start:end], descending only
// into known container box types, and returns every stco/co64 table found,
// in the order they appear.
func findChunkOffsetTables(data []byte, start, end int) []stcoLocation {
	var out []stcoLocation
	pos := start
	for pos+8 <= end {
		size32 := binary.BigEndian.Uint32(data[pos : pos+4])
		typ := string(data[pos+4 : pos+8])
		hdr := 8
		sz := int(size32)
		switch size32 {
		case 1:
			if pos+16 > end {
				return out
			}
			sz = int(binary.BigEndian.Uint64(data[pos+8 : pos+16]))
			hdr = 16
		case 0:
			sz = end - pos
		}
		if sz < hdr || pos+sz > end {
			return out
		}

		switch {
		case typ == "stco" || typ == "co64":
			// stco/co64 payload: 4-byte version+flags, 4-byte entry count,
			// then entry_count fixed-width entries.
			countOff := pos + hdr + 4
			count := int(binary.BigEndian.Uint32(data[countOff : countOff+4]))
			out = append(out, stcoLocation{
				entriesOff: countOff + 4,
				count:      count,
				is64:       typ == "co64",
			})
		case chunkOffsetContainers[typ]:
			out = append(out, findChunkOffsetTables(data, pos+hdr, pos+sz)...)
		}
		pos += sz
	}
	return out
}

// readChunkOffsets returns loc's chunk-offset entries widened to uint64.
func readChunkOffsets(data []byte, loc stcoLocation) []uint64 {
	entries := make([]uint64, loc.count)
	off := loc.entriesOff
	for i := 0; i < loc.count; i++ {
		if loc.is64 {
			entries[i] = binary.BigEndian.Uint64(data[off : off+8])
			off += 8
		} else {
			entries[i] = uint64(binary.BigEndian.Uint32(data[off : off+4]))
			off += 4
		}
	}
	return entries
}

// writeChunkOffsets overwrites loc's entries in place with entries.
func writeChunkOffsets(data []byte, loc stcoLocation, entries []uint64) {
	off := loc.entriesOff
	for _, v := range entries {
		if loc.is64 {
			binary.BigEndian.PutUint64(data[off:off+8], v)
			off += 8
		} else {
			binary.BigEndian.PutUint32(data[off:off+4], uint32(v))
			off += 4
		}
	}
}
