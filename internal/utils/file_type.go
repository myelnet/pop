package utils

import (
	"github.com/gabriel-vasile/mimetype"
	"io"
	"mime"
	"path/filepath"
	"strings"
)

type FileType int

const (
	Unknown FileType = iota
	Application
	Audio
	Video
	Image
	Chemical
	Font
	Message
	Model
	Text
	Archive
)

var MimeTypes = map[string]FileType{
	"application": Application,
	"archive":     Archive,
	"audio":       Audio,
	"video":       Video,
	"image":       Image,
	"text":        Text,
	"chemical":    Chemical,
	"font":        Font,
	"message":     Message,
	"model":       Model,
}

// DetectFileType detects the FileType of a file using it's extension.
// The path argument should be absolute
func DetectFileType(path string, buf io.ReadSeeker) FileType {
	ext := filepath.Ext(path)
	if ext != "" {
		return fileTypeByExtension(ext)
	}

	// if no extension is given, it will use the "mimetype" lib
	// and try to guess the type by using the first 512 bytes of the file,
	// given its full path
	mtype, err := mimetype.DetectFile(path)
	if err != nil {
		// if given a reader instead of a path, it will consume
		// the first 512 bytes of it then rewind the buf to 0
		defer buf.Seek(0, io.SeekStart)

		mtype, err = mimetype.DetectReader(buf)
		if err != nil || mtype.Extension() == "" {
			return Unknown
		}
	}

	ext = filepath.Ext(mtype.Extension())

	return fileTypeByExtension(ext)
}

// fileTypeByExtension matches an extension with its FileType, i.e : fileTypeByExtension("file.mp3") returns Audio
func fileTypeByExtension(ext string) FileType {
	// Since some Archive types are not always found by filepath.Ext(), like ".gz"
	// And because archive types are considered as an Application mimetype,
	// we bypass the process and compare the extension with a hardcoded "archive" map
	_, ok := archive[ext]
	if ok {
		return Archive
	}

	// get full mime from extension: ".mp3" -> "audio/mpeg"
	fullMime := mime.TypeByExtension(ext)
	if fullMime == "" {
		return Unknown
	}

	// extract mimetype: "audio/mpeg" -> "audio"
	mimes := strings.Split(fullMime, "/")
	if len(mimes) != 2 {
		return Unknown
	}
	mimeType := mimes[0]

	// get the FileType from a mimeType: "audio" -> Audio
	fileType, ok := MimeTypes[mimeType]
	if ok {
		return fileType
	}

	return Unknown
}

// list of all archive mime types :
// https://en.wikipedia.org/wiki/List_of_archive_formats
var archive = map[string]string{
	".a":    "application/x-archive",
	".ar":   "application/x-cpio",
	".shar": "application/x-shar",
	".iso":  "application/x-iso9660-image",
	".sbx":  "application/x-sbx",
	".lz":   "application/x-lzip",
	".lz4":  "application/x-lzip",
	".lzo":  "application/x-lzop",
	".sz":   "application/x-snappy-framed",
	".z":    "application/x-compress",
	".Z":    "application/x-compress",
	".zst":  "application/zst",
	".afa":  "application/x-astrotite-afa",
	".alz":  "application/x-alz-compressed",
	".apk":  "application/octet-stream",
	".ark":  "application/octet-stream",
	".cdx":  "application/x-freearc",
	".arj":  "application/x-arj",
	".b1":   "application/x-b1",
	".dar":  "application/x-dar",
	".dmg":  "application/x-apple-diskimage",
	".sit":  "application/x-stuffit",
	".sitx": "application/x-stuffitx",
	".tgz":  "application/x-gtar",
	".tlz":  "application/x-gtar",
	".txz":  "application/x-gtar",
	".tbz2": "application/x-gtar",
	".wim":  "application/x-ms-wim",
	".xar":  "application/x-xar",
	".zoo":  "application/x-zoo",
	".zip":  "application/zip",
	".zipx": "application/zip",
	".gz":   "application/gzip",
	".bz":   "application/x-bzip",
	".boz":  "application/x-bzip2",
	".bz2":  "application/x-bzip2",
	".7z":   "application/x-7z-compressed",
	".s7z":  "application/x-7z-compressed",
	".ace":  "application/x-ace-compressed",
	".arc":  "application/x-freearc",
	".jar":  "application/java-archive",
	".ear":  "application/java-archive",
	".war":  "application/java-archive",
	".rar":  "application/x-rar-compressed",
	".tar":  "application/x-tar",
	".lzma": "application/x-lzma",
	".xz":   "application/x-lzma",
	".dgc":  "application/x-dgc-compressed",
	".cfs":  "application/x-cfs-compressed",
	".gca":  "application/x-gca-compressed",
	".lha":  "application/x-lzh-compressed",
	".lzh":  "application/x-lzh-compressed",
	".cab":  "application/vnd.ms-cab-compressed",
}
