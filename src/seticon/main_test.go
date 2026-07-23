//go:build windows

package main

import (
	"encoding/binary"
	"testing"
)

// buildICO synthesises a container with n images. The pixel payloads are
// nonsense -- parseICO only walks the directory, it never decodes images.
func buildICO(n int, payload int) []byte {
	header := make([]byte, iconDirHeaderSize+n*iconDirEntrySize)
	binary.LittleEndian.PutUint16(header[0:], 0)
	binary.LittleEndian.PutUint16(header[2:], 1)
	binary.LittleEndian.PutUint16(header[4:], uint16(n))

	body := []byte{}
	offset := uint32(len(header))
	for i := 0; i < n; i++ {
		b := header[iconDirHeaderSize+i*iconDirEntrySize:]
		size := byte(16 << i)
		b[0], b[1] = size, size
		b[2], b[3] = 0, 0
		binary.LittleEndian.PutUint16(b[4:], 1)
		binary.LittleEndian.PutUint16(b[6:], 32)
		binary.LittleEndian.PutUint32(b[8:], uint32(payload))
		binary.LittleEndian.PutUint32(b[12:], offset)

		body = append(body, make([]byte, payload)...)
		offset += uint32(payload)
	}
	return append(header, body...)
}

func TestParseICO(t *testing.T) {
	raw := buildICO(3, 64)
	entries, err := parseICO(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("got %d entries, want 3", len(entries))
	}
	if entries[0].width != 16 || entries[1].width != 32 || entries[2].width != 64 {
		t.Errorf("sizes wrong: %d %d %d", entries[0].width, entries[1].width, entries[2].width)
	}
	if entries[0].bitCount != 32 {
		t.Errorf("bitCount = %d, want 32", entries[0].bitCount)
	}
	for i, e := range entries {
		if e.bytesInRes != 64 {
			t.Errorf("entry %d bytesInRes = %d, want 64", i, e.bytesInRes)
		}
		if int(e.offset)+int(e.bytesInRes) > len(raw) {
			t.Errorf("entry %d runs past the buffer", i)
		}
	}
}

func TestParseICORejectsBadInput(t *testing.T) {
	cases := []struct {
		name string
		raw  []byte
	}{
		{"empty", nil},
		{"too short", []byte{0, 0, 1}},
		{"not an icon", []byte{0, 0, 2, 0, 1, 0}}, // type 2 is a cursor
		{"reserved not zero", []byte{1, 0, 1, 0, 1, 0}},
		{"zero images", []byte{0, 0, 1, 0, 0, 0}},
		{"directory truncated", []byte{0, 0, 1, 0, 4, 0, 1, 2, 3}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := parseICO(c.raw); err == nil {
				t.Error("expected an error")
			}
		})
	}
}

// An image whose declared extent runs past the end of the file would otherwise
// slice out of range when the payload is copied into the resource.
func TestParseICORejectsOutOfBoundsImage(t *testing.T) {
	raw := buildICO(1, 64)
	binary.LittleEndian.PutUint32(raw[iconDirHeaderSize+8:], 1<<20) // bytesInRes
	if _, err := parseICO(raw); err == nil {
		t.Error("expected an error for an image running past end of file")
	}
}

// The RT_GROUP_ICON directory has the same 6-byte header but 14-byte entries:
// the 4-byte file offset is replaced by a 2-byte resource id.
func TestBuildGroup(t *testing.T) {
	entries, err := parseICO(buildICO(3, 64))
	if err != nil {
		t.Fatal(err)
	}
	group := buildGroup(entries)

	if len(group) != iconDirHeaderSize+3*grpIconEntrySize {
		t.Fatalf("group is %d bytes, want %d", len(group), iconDirHeaderSize+3*grpIconEntrySize)
	}
	if binary.LittleEndian.Uint16(group[2:]) != 1 {
		t.Error("type should be 1 (icon)")
	}
	if binary.LittleEndian.Uint16(group[4:]) != 3 {
		t.Error("count should be 3")
	}
	for i := range entries {
		b := group[iconDirHeaderSize+i*grpIconEntrySize:]
		// Resource ids are 1-based and must match the RT_ICON ids written
		// alongside them, or the shell resolves an empty icon.
		if id := binary.LittleEndian.Uint16(b[12:]); id != uint16(i+1) {
			t.Errorf("entry %d has resource id %d, want %d", i, id, i+1)
		}
		if b[0] != entries[i].width {
			t.Errorf("entry %d width = %d, want %d", i, b[0], entries[i].width)
		}
		if got := binary.LittleEndian.Uint32(b[8:]); got != entries[i].bytesInRes {
			t.Errorf("entry %d bytesInRes = %d, want %d", i, got, entries[i].bytesInRes)
		}
	}
}
