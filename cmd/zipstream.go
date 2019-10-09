package cmd

import (
	"encoding/binary"
	"errors"
	"fmt"
	"github.com/spf13/cobra"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"os/exec"
	"path"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
	"unsafe"
)

var (
	ErrFormat    = errors.New("zip: not a valid zip file")
	ErrAlgorithm = errors.New("zip: unsupported compression algorithm")
)

type Reader struct {
	File          []*File
	Comment       string
	decompressors map[uint16]Decompressor
}

type File struct {
	FileHeader
	zip          *Reader
	headerOffset int64
	Path string
}

type FileHeader struct {
	Name string
	Comment string
	NonUTF8 bool

	CreatorVersion uint16
	ReaderVersion  uint16
	Flags          uint16

	Method uint16

	Modified     time.Time
	ModifiedTime uint16 // Deprecated: Legacy MS-DOS date; use Modified instead.
	ModifiedDate uint16 // Deprecated: Legacy MS-DOS time; use Modified instead.

	CRC32              uint32
	CompressedSize     uint32 // Deprecated: Use CompressedSize64 instead.
	UncompressedSize   uint32 // Deprecated: Use UncompressedSize64 instead.
	CompressedSize64   uint64
	UncompressedSize64 uint64
	Extra              []byte
	ExternalAttrs      uint32 // Meaning depends on CreatorVersion
}

type ReadCloser struct {
	f *os.File
	Reader
}

func getStream(c *cobra.Command, args []string) error{
	url, err := c.PersistentFlags().GetString("url")
	if err != nil {
		return err
	}

	// get content length
	size, err := getContentLength(url)
	if err != nil {
		return err
	}

	r := new(ReadCloser)
	if err := r.init(url, int64(size)); err != nil {
		return err
	}

	// create download location
	path, err := c.PersistentFlags().GetString("path")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(path, 0755); err != nil {
		return err
	}

	// download file
	for _, f := range r.File {
		f.setFilePath(path)
		bodyOffSet, err := f.findBodyOffset(url)
		if err != nil {
			return err
		}
		perSize := f.CompressedSize64 / 5
		for i := 0; i < 5; i++ {
			var b io.ReadCloser
			// get divided content
			switch i{
			case 0:
				if b, err = getFileBody(url, int(f.headerOffset+bodyOffSet), int(f.headerOffset+bodyOffSet)+int(perSize)); err != nil {
					return err
				}
			case 4:
				if b, err = getFileBody(url, int(f.headerOffset+bodyOffSet)+int(perSize)*i+1, int(f.headerOffset+bodyOffSet)+int(f.CompressedSize64)); err != nil {
					return err
				}
			default:
				if b, err = getFileBody(url, int(f.headerOffset+bodyOffSet)+int(perSize)*i+1, int(f.headerOffset+bodyOffSet)+int(perSize)*(i+1)); err != nil {
					return err
				}
			}
			// save divided content in file
			if err = f.saveFileBody(b); err != nil {
				return err
			}
		}
		// decompress content
		if f.Method != 0 {
			if err = f.decompress(); err != nil {
				return err
			}
		}
	}

	return err
}

func (z *Reader) init(url string, size int64) error {
	// get EOCD
	b, err := getFileBody(url, int(size) - 100, int(size))
	defer b.Close()
	if err != nil {
		return err
	}
	buf, err := ioutil.ReadAll(b)
	if err != nil {
		return err
	}
	startEndDir := findSignatureInBlock(buf)
	end, err := readDirectoryEnd(buf[startEndDir:])
	if err != nil {
		return err
	}
	if end.directoryRecords > uint64(size)/fileHeaderLen {
		return fmt.Errorf("archive/zip: TOC declares impossible %d files in %d byte zip", end.directoryRecords, size)
	}
	z.File = make([]*File, 0, end.directoryRecords)
	// get central directory
	b2, err := getFileBody(url, int(end.directoryOffset), int(size))
	if err != nil {
		return err
	}
	for {
		f := &File{zip:z}
		err = readDirectoryHeader(f, b2)
		if err == ErrFormat || err == io.ErrUnexpectedEOF {
			break
		}
		if err != nil {
			return err
		}
		z.File = append(z.File, f)
	}
	return nil
}

func getContentLength(url string) (int, error) {
	out, err := exec.Command("curl", "-L" , "-s", "-o", "/dev/null", url, "-w", "%{size_download}").Output()
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(*(*string)(unsafe.Pointer(&out)))
}

func (f File) decompress() error {
	fp, err := os.OpenFile(f.Path, os.O_RDWR|os.O_CREATE, 0666)
	defer fp.Close()
	if err != nil {
		return err
	}
	r := io.NewSectionReader(fp, 0, int64(f.CompressedSize64))
	dcomp := f.zip.decompressor(f.Method)
	if dcomp == nil {
		return ErrAlgorithm
	}
	rc := dcomp(r)
	defer rc.Close()

	_, err = io.Copy(fp, rc)
	return err
}

func getFileBody(url string, from ,to int) (io.ReadCloser, error) {
	// create get request
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}

	// set download ranges
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", from, to))

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}

	return res.Body, nil
}

func (f File)saveFileBody(b io.ReadCloser) error {
	defer b.Close()
	fp, err := os.OpenFile(f.Path, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0666)
	if err != nil {
		return err
	}
	defer fp.Close()

	_, err = io.Copy(fp, b)
	return err
}

func (f *File)setFilePath(p string) {
	f.Path = path.Join(p, f.Name[strings.LastIndex(f.Name, "/")+1:])
}

func readDirectoryHeader(f *File, r io.Reader) error {
		var buf [directoryHeaderLen]byte
		if _, err := io.ReadFull(r, buf[:]); err != nil {
			return err
		}
		b := readBuf(buf[:])
		if sig := b.uint32(); sig != directoryHeaderSignature {
			return ErrFormat
		}

	f.CreatorVersion = b.uint16()
	f.ReaderVersion = b.uint16()
	f.Flags = b.uint16()
	f.Method = b.uint16()
	f.ModifiedTime = b.uint16()
	f.ModifiedDate = b.uint16()
	f.CRC32 = b.uint32()
	f.CompressedSize = b.uint32()
	f.UncompressedSize = b.uint32()
	f.CompressedSize64 = uint64(f.CompressedSize)
	f.UncompressedSize64 = uint64(f.UncompressedSize)
	filenameLen := int(b.uint16())
	extraLen := int(b.uint16())
	commentLen := int(b.uint16())
	b = b[4:] // skipped start disk number and internal attributes (2x uint16)
	f.ExternalAttrs = b.uint32()
	f.headerOffset = int64(b.uint32())
	d := make([]byte, filenameLen+extraLen+commentLen)
	if _, err := io.ReadFull(r, d); err != nil {
		return err
	}
	f.Name = string(d[:filenameLen])
	f.Extra = d[filenameLen : filenameLen+extraLen]
	f.Comment = string(d[filenameLen+extraLen:])

	// Determine the character encoding.
	utf8Valid1, utf8Require1 := detectUTF8(f.Name)
	utf8Valid2, utf8Require2 := detectUTF8(f.Comment)
	switch {
	case !utf8Valid1 || !utf8Valid2:
		// Name and Comment definitely not UTF-8.
		f.NonUTF8 = true
	case !utf8Require1 && !utf8Require2:
		// Name and Comment use only single-byte runes that overlap with UTF-8.
		f.NonUTF8 = false
	default:
		// Might be UTF-8, might be some other encoding; preserve existing flag.
		// Some ZIP writers use UTF-8 encoding without setting the UTF-8 flag.
		// Since it is impossible to always distinguish valid UTF-8 from some
		// other encoding (e.g., GBK or Shift-JIS), we trust the flag.
		f.NonUTF8 = f.Flags&0x800 == 0
	}

	return nil
}

func readDirectoryEnd(buf []byte) (dir *directoryEnd, err error) {

	// read header into struct
	b := readBuf(buf[4:]) // skip signature
	d := &directoryEnd{
		diskNbr:            uint32(b.uint16()),
		dirDiskNbr:         uint32(b.uint16()),
		dirRecordsThisDisk: uint64(b.uint16()),
		directoryRecords:   uint64(b.uint16()),
		directorySize:      uint64(b.uint32()),
		directoryOffset:    uint64(b.uint32()),
		commentLen:         b.uint16(),
	}
	l := int(d.commentLen)
	if l > len(b) {
		return nil, errors.New("zip: invalid comment length")
	}
	d.comment = string(b[:l])

	return d, nil
}

func findDirectory64End(r io.ReaderAt, directoryEndOffset int64) (int64, error) {
	locOffset := directoryEndOffset - directory64LocLen
	if locOffset < 0 {
		return -1, nil // no need to look for a header outside the file
	}
	buf := make([]byte, directory64LocLen)
	if _, err := r.ReadAt(buf, locOffset); err != nil {
		return -1, err
	}
	b := readBuf(buf)
	if sig := b.uint32(); sig != directory64LocSignature {
		return -1, nil
	}
	if b.uint32() != 0 { // number of the disk with the start of the zip64 end of central directory
		return -1, nil // the file is not a valid zip64-file
	}
	p := b.uint64()      // relative offset of the zip64 end of central directory record
	if b.uint32() != 1 { // total number of disks
		return -1, nil // the file is not a valid zip64-file
	}
	return int64(p), nil
}

func readDirectory64End(r io.ReaderAt, offset int64, d *directoryEnd) (err error) {
	buf := make([]byte, directory64EndLen)
	if _, err := r.ReadAt(buf, offset); err != nil {
		return err
	}

	b := readBuf(buf)
	if sig := b.uint32(); sig != directory64EndSignature {
		return ErrFormat
	}

	b = b[12:]                        // skip dir size, version and version needed (uint64 + 2x uint16)
	d.diskNbr = b.uint32()            // number of this disk
	d.dirDiskNbr = b.uint32()         // number of the disk with the start of the central directory
	d.dirRecordsThisDisk = b.uint64() // total number of entries in the central directory on this disk
	d.directoryRecords = b.uint64()   // total number of entries in the central directory
	d.directorySize = b.uint64()      // size of the central directory
	d.directoryOffset = b.uint64()    // offset of start of central directory with respect to the starting disk number

	return nil
}

func findSignatureInBlock(b []byte) int {
	for i := len(b) - directoryEndLen; i >= 0; i-- {
		// defined from directoryEndSignature in struct.go
		if b[i] == 'P' && b[i+1] == 'K' && b[i+2] == 0x05 && b[i+3] == 0x06 {
			// n is length of comment
			n := int(b[i+directoryEndLen-2]) | int(b[i+directoryEndLen-1])<<8
			if n+directoryEndLen+i <= len(b) {
				return i
			}
		}
	}
	return -1
}

type readBuf []byte

func (b *readBuf) uint8() uint8 {
	v := (*b)[0]
	*b = (*b)[1:]
	return v
}

func (b *readBuf) uint16() uint16 {
	v := binary.LittleEndian.Uint16(*b)
	*b = (*b)[2:]
	return v
}

func (b *readBuf) uint32() uint32 {
	v := binary.LittleEndian.Uint32(*b)
	*b = (*b)[4:]
	return v
}

func (b *readBuf) uint64() uint64 {
	v := binary.LittleEndian.Uint64(*b)
	*b = (*b)[8:]
	return v
}

func (b *readBuf) sub(n int) readBuf {
	b2 := (*b)[:n]
	*b = (*b)[n:]
	return b2
}

func detectUTF8(s string) (valid, require bool) {
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		i += size
		// Officially, ZIP uses CP-437, but many readers use the system's
		// local character encoding. Most encoding are compatible with a large
		// subset of CP-437, which itself is ASCII-like.
		//
		// Forbid 0x7e and 0x5c since EUC-KR and Shift-JIS replace those
		// characters with localized currency and overline characters.
		if r < 0x20 || r > 0x7d || r == 0x5c {
			if !utf8.ValidRune(r) || (r == utf8.RuneError && size == 1) {
				return false, false
			}
			require = true
		}
	}
	return true, require
}

// findBodyOffset does the minimum work to verify the file has a header
// and returns the file body offset.
func (f *File) findBodyOffset(url string) (int64, error) {
	body, err := getFileBody(url, int(f.headerOffset), int(f.headerOffset) + fileHeaderLen)
	if err != nil {
		return 0, err
	}
	buf, err := ioutil.ReadAll(body)
	if err != nil {
		return 0, err
	}
	b := readBuf(buf[:])
	if sig := b.uint32(); sig != fileHeaderSignature {
		return 0, ErrFormat
	}
	b = b[22:] // skip over most of the header
	filenameLen := int(b.uint16())
	extraLen := int(b.uint16())
	return int64(fileHeaderLen + filenameLen + extraLen), nil
}

func (z *Reader) decompressor(method uint16) Decompressor {
	dcomp := z.decompressors[method]
	if dcomp == nil {
		dcomp = decompressor(method)
	}
	return dcomp
}