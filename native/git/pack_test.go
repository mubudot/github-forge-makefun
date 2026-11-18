package git

import (
	"bytes"
	"compress/zlib"
	"crypto/sha1"
	"encoding/binary"
	"encoding/hex"
	"testing"
)

func compressed(data []byte) []byte {
	var b bytes.Buffer
	z := zlib.NewWriter(&b)
	z.Write(data)
	z.Close()
	return b.Bytes()
}
func packRecords(records ...[]byte) []byte {
	out := []byte("PACK\x00\x00\x00\x02\x00\x00\x00\x00")
	binary.BigEndian.PutUint32(out[8:], uint32(len(records)))
	for _, r := range records {
		out = append(out, r...)
	}
	s := sha1.Sum(out)
	return append(out, s[:]...)
}
func record(code int, data []byte) []byte {
	return append(entryHeader(code, len(data)), compressed(data)...)
}
func mustReject(t *testing.T, f func()) {
	t.Helper()
	defer func() {
		if recover() == nil {
			t.Fatal("accepted invalid input")
		}
	}()
	f()
}
func TestForwardAndOffsetDeltas(t *testing.T) {
	base := []byte("hello world\n")
	second := []byte("hello forge\n")
	third := []byte("hello forge!\n")
	deltaSecond := append([]byte{12, 12, 0x90, 6, 6}, []byte("forge\n")...)
	deltaThird := append([]byte{12, 13, 0x90, 11, 2}, []byte("!\n")...)
	oid, _ := hex.DecodeString(HashObject("blob", second))
	ref := append(entryHeader(7, len(deltaThird)), oid...)
	ref = append(ref, compressed(deltaThird)...)
	original := record(3, base)
	ofs := append(entryHeader(6, len(deltaSecond)), byte(len(original)))
	ofs = append(ofs, compressed(deltaSecond)...)
	db := NewDatabase()
	ReadPack(packRecords(ref, original, ofs), db)
	if len(db.Objects) != 3 {
		t.Fatalf("objects: %d", len(db.Objects))
	}
	for _, data := range [][]byte{base, second, third} {
		if !bytes.Equal(db.Get(HashObject("blob", data)).Data, data) {
			t.Fatal("delta contents differ")
		}
	}
	objects := []*Object{}
	for _, data := range [][]byte{base, second, third} {
		objects = append(objects, db.Get(HashObject("blob", data)))
	}
	round := NewDatabase()
	ReadPack(WritePack(objects), round)
	if len(round.Objects) != 3 {
		t.Fatal("round trip lost objects")
	}
}
func TestPackFailureIsAtomicAndInflaterRecovers(t *testing.T) {
	data := []byte("validation survives rejected streams\n")
	z := compressed(data)
	damaged := bytes.Clone(z)
	damaged[len(damaged)-1] ^= 1
	good := record(3, data)
	bad := [][]byte{append(entryHeader(3, len(data)), damaged...), append(entryHeader(3, len(data)), z[:len(z)-2]...), append(entryHeader(3, len(data)-1), z...), append(entryHeader(3, len(data)+1), z...)}
	for _, r := range bad {
		db := NewDatabase()
		mustReject(t, func() { ReadPack(packRecords(good, r), db) })
		if len(db.Objects) != 0 {
			t.Fatal("partial pack was published")
		}
		ReadPack(packRecords(good), db)
		if len(db.Objects) != 1 {
			t.Fatal("inflater failed to recover")
		}
	}
	for _, data := range [][]byte{{}, bytes.Repeat([]byte{'w'}, 131072)} {
		db := NewDatabase()
		ReadPack(packRecords(record(3, data)), db)
		if !bytes.Equal(db.Get(HashObject("blob", data)).Data, data) {
			t.Fatal("inflation mismatch")
		}
	}
}
func TestThinPackAndChecksum(t *testing.T) {
	db := NewDatabase()
	base := []byte("hello world\n")
	oid := db.Put("blob", base)
	oidBytes, _ := hex.DecodeString(oid)
	delta := append([]byte{12, 12, 0x90, 6, 6}, []byte("forge\n")...)
	r := append(entryHeader(7, len(delta)), oidBytes...)
	r = append(r, compressed(delta)...)
	p := packRecords(r)
	bad := bytes.Clone(p)
	bad[len(bad)-1] ^= 1
	mustReject(t, func() { ReadPack(bad, db) })
	if len(db.Objects) != 1 {
		t.Fatal("checksum failure mutated store")
	}
	ReadPack(p, db)
	if !bytes.Equal(db.Get(HashObject("blob", []byte("hello forge\n"))).Data, []byte("hello forge\n")) {
		t.Fatal("thin delta mismatch")
	}
}
func BenchmarkPushPack(b *testing.B) {
	db := NewDatabase()
	blob := db.Put("blob", bytes.Repeat([]byte("new content!"), 90))
	tree := db.WriteTree([]TreeEntry{{"100644", "file", blob}})
	commit := db.WriteCommit(tree, nil, "commit", 1767225600)
	p := WritePack([]*Object{db.Get(blob), db.Get(tree), db.Get(commit)})
	target := NewDatabase()
	b.ReportAllocs()
	b.SetBytes(int64(len(p)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ReadPack(p, target)
	}
}
