package cmd

// Compression methods.
const (
	Store   uint16 = 0 // no compression
	Deflate uint16 = 8 // DEFLATE compressed
)

const (
	fileHeaderSignature      = 0x04034b50
	directoryHeaderSignature = 0x02014b50
	directoryEndSignature    = 0x06054b50
	directory64LocSignature  = 0x07064b50
	directory64EndSignature  = 0x06064b50
	dataDescriptorSignature  = 0x08074b50 // de-facto standard; required by OS X Finder
	fileHeaderLen            = 30         // + filename + extra
	directoryHeaderLen       = 46         // + filename + extra + comment
	directoryEndLen          = 22         // + comment
	dataDescriptorLen        = 16         // four uint32: descriptor signature, crc32, compressed size, size
	dataDescriptor64Len      = 24         // descriptor with 8 byte sizes
	directory64LocLen        = 20         //
	directory64EndLen        = 56         // + extra

	// Constants for the first byte in CreatorVersion.
	creatorFAT    = 0
	creatorUnix   = 3
	creatorNTFS   = 11
	creatorVFAT   = 14
	creatorMacOSX = 19

	// Version numbers.
	zipVersion20 = 20 // 2.0
	zipVersion45 = 45 // 4.5 (reads and writes zip64 archives)

	// Limits for non zip64 files.
	uint16max = (1 << 16) - 1
	uint32max = (1 << 32) - 1

	// Extra header IDs.
	//
	// IDs 0..31 are reserved for official use by PKWARE.
	// IDs above that range are defined by third-party vendors.
	// Since ZIP lacked high precision timestamps (nor a official specification
	// of the timezone used for the date fields), many competing extra fields
	// have been invented. Pervasive use effectively makes them "official".
	//
	// See http://mdfs.net/Docs/Comp/Archiving/Zip/ExtraField
	zip64ExtraID       = 0x0001 // Zip64 extended information
	ntfsExtraID        = 0x000a // NTFS
	unixExtraID        = 0x000d // UNIX
	extTimeExtraID     = 0x5455 // Extended timestamp
	infoZipUnixExtraID = 0x5855 // Info-ZIP Unix extension
)

type directoryEnd struct {
	diskNbr            uint32 // unused
	dirDiskNbr         uint32 // unused
	dirRecordsThisDisk uint64 // unused
	directoryRecords   uint64
	directorySize      uint64
	directoryOffset    uint64 // relative to file
	commentLen         uint16
	comment            string
}