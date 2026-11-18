package git

import (
	"bytes"
	"compress/flate"
	"compress/zlib"
	"crypto/sha1"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"sync"
)

const maxObject = 16 << 20

var typeNames = map[int]string{1: "commit", 2: "tree", 3: "blob", 4: "tag"}
var typeCodes = map[string]int{"commit": 1, "tree": 2, "blob": 3, "tag": 4}
var inflaters sync.Pool

func entryHeader(code, size int) []byte {
	out := []byte{}
	b := byte(code<<4 | size&15)
	size >>= 4
	for size > 0 {
		out = append(out, b|128)
		b = byte(size & 127)
		size >>= 7
	}
	return append(out, b)
}
func applyDelta(base, data []byte) []byte {
	pos := 0
	readSize := func() int {
		v, shift := 0, 0
		for {
			if pos >= len(data) || shift > 28 {
				panic("Invalid delta size")
			}
			b := data[pos]
			pos++
			v |= int(b&127) << shift
			shift += 7
			if b&128 == 0 {
				return v
			}
		}
	}
	if readSize() != len(base) {
		panic("Delta base size mismatch")
	}
	size := readSize()
	if size > maxObject {
		panic("Delta too large")
	}
	out := make([]byte, 0, size)
	next := func() int {
		if pos >= len(data) {
			panic("Truncated delta")
		}
		b := int(data[pos])
		pos++
		return b
	}
	for pos < len(data) {
		op := next()
		if op&128 != 0 {
			offset, count := 0, 0
			for bit := 0; bit < 4; bit++ {
				if op&(1<<bit) != 0 {
					offset |= next() << (8 * bit)
				}
			}
			for bit := 4; bit < 7; bit++ {
				if op&(1<<bit) != 0 {
					count |= next() << (8 * (bit - 4))
				}
			}
			if count == 0 {
				count = 65536
			}
			if offset+count > len(base) || len(out)+count > size {
				panic("Invalid delta copy")
			}
			out = append(out, base[offset:offset+count]...)
		} else {
			if op == 0 || pos+op > len(data) || len(out)+op > size {
				panic("Invalid delta insert")
			}
			out = append(out, data[pos:pos+op]...)
			pos += op
		}
	}
	if len(out) != size {
		panic("Delta length mismatch")
	}
	return out
}

// ReadPack validates the complete pack, zlib checksums and deltas before publishing
// objects. The reusable zlib reader does not buffer past its bytes.Reader input.
func ReadPack(pack []byte, db *Database) {
	if len(pack) < 32 || string(pack[:4]) != "PACK" {
		panic("Invalid pack header")
	}
	version := binary.BigEndian.Uint32(pack[4:8])
	if version != 2 && version != 3 {
		panic("Unsupported pack version")
	}
	end := len(pack) - 20
	sum := sha1.Sum(pack[:end])
	if !bytes.Equal(sum[:], pack[end:]) {
		panic("Pack checksum mismatch")
	}
	count := int(binary.BigEndian.Uint32(pack[8:12]))
	if count > 200000 {
		panic("Too many pack objects")
	}
	type entry struct {
		offset, code, baseOffset int
		data                     []byte
		baseOID, oid             string
		object                   *Object
	}
	entries := make([]*entry, 0, count)
	offsets := map[int]*entry{}
	resolved := map[string]*Object{}
	pos, expanded := 12, 0
	var zr io.ReadCloser
	if v := inflaters.Get(); v != nil {
		zr = v.(io.ReadCloser)
	}
	valid := false
	defer func() {
		if zr != nil {
			if valid {
				inflaters.Put(zr)
			} else {
				zr.Close()
			}
		}
	}()
	for i := 0; i < count; i++ {
		offset := pos
		if pos >= end {
			panic("Truncated pack")
		}
		b := pack[pos]
		pos++
		size, shift, code := int(b&15), 4, int(b>>4&7)
		for b&128 != 0 {
			if pos >= end || shift > 28 {
				panic("Invalid pack object size")
			}
			b = pack[pos]
			pos++
			size |= int(b&127) << shift
			shift += 7
		}
		if size > maxObject {
			panic("Pack object too large")
		}
		expanded += size
		if expanded > 64<<20 {
			panic("Pack expands beyond the memory limit")
		}
		e := &entry{offset: offset, code: code}
		if code == 6 {
			if pos >= end {
				panic("Truncated delta")
			}
			b = pack[pos]
			pos++
			distance := int(b & 127)
			for b&128 != 0 {
				if pos >= end || distance > end {
					panic("Invalid delta offset")
				}
				b = pack[pos]
				pos++
				distance = (distance+1)*128 + int(b&127)
			}
			e.baseOffset = offset - distance
			if distance <= 0 || e.baseOffset < 12 {
				panic("Invalid delta base")
			}
		} else if code == 7 {
			if pos+20 > end {
				panic("Truncated delta")
			}
			e.baseOID = hex.EncodeToString(pack[pos : pos+20])
			pos += 20
		} else if typeNames[code] == "" {
			panic("Invalid pack object type")
		}
		input := bytes.NewReader(pack[pos:end])
		var err error
		if zr == nil {
			zr, err = zlib.NewReader(input)
		} else {
			err = zr.(zlib.Resetter).Reset(input, nil)
		}
		if err != nil {
			panic(err)
		}
		data := make([]byte, size+1)
		if _, err := io.ReadFull(zr, data[:size]); err != nil {
			panic(err)
		}
		// Reaching the declared size does not validate the stream's trailer.
		// Require zlib's own EOF; ErrUnexpectedEOF also means truncation when
		// every declared output byte happened to arrive before the cut.
		n, err := zr.Read(data[size:])
		if n != 0 || err != io.EOF {
			if err != nil {
				panic(err)
			}
			panic("Pack object size mismatch")
		}
		pos = end - input.Len()
		e.data = data[:size:size]
		if t := typeNames[code]; t != "" {
			e.oid = HashObject(t, e.data)
			e.object = &Object{Type: t, Data: e.data, Packed: bytes.Clone(pack[offset:pos])}
			resolved[e.oid] = e.object
		}
		entries = append(entries, e)
		offsets[offset] = e
	}
	if pos != end {
		panic("Trailing pack data")
	}
	pending := []*entry{}
	for _, e := range entries {
		if e.object == nil {
			pending = append(pending, e)
		}
	}
	for len(pending) > 0 {
		next := []*entry{}
		for _, e := range pending {
			var base *Object
			if e.code == 6 {
				if at := offsets[e.baseOffset]; at != nil {
					base = at.object
				}
			} else {
				base = resolved[e.baseOID]
				if base == nil {
					base = db.Objects[e.baseOID]
				}
			}
			if base == nil {
				next = append(next, e)
				continue
			}
			data := applyDelta(base.Data, e.data)
			expanded += len(data)
			if expanded > 64<<20 {
				panic("Pack expands beyond the memory limit")
			}
			e.object = &Object{Type: base.Type, Data: data}
			e.oid = HashObject(base.Type, data)
			resolved[e.oid] = e.object
		}
		if len(next) == len(pending) {
			panic("Unresolved pack delta")
		}
		pending = next
	}
	for oid, o := range resolved {
		if db.Objects[oid] == nil {
			db.Objects[oid] = o
		}
	}
	valid = true
}
func WritePack(objects []*Object) []byte {
	var b bytes.Buffer
	b.WriteString("PACK")
	binary.Write(&b, binary.BigEndian, uint32(2))
	binary.Write(&b, binary.BigEndian, uint32(len(objects)))
	for _, o := range objects {
		if o.Packed == nil {
			var p bytes.Buffer
			p.Write(entryHeader(typeCodes[o.Type], len(o.Data)))
			z, err := zlib.NewWriterLevel(&p, flate.BestSpeed)
			if err != nil {
				panic(err)
			}
			z.Write(o.Data)
			z.Close()
			o.Packed = p.Bytes()
		}
		b.Write(o.Packed)
	}
	sum := sha1.Sum(b.Bytes())
	b.Write(sum[:])
	return b.Bytes()
}
func packError(f func()) (reason string) {
	defer func() {
		if r := recover(); r != nil {
			reason = fmt.Sprint(r)
		}
	}()
	f()
	return ""
}
