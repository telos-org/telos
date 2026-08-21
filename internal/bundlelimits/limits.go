// Package bundlelimits defines resource limits shared by package and skill
// bundle readers.
package bundlelimits

const (
	// MaxFiles bounds the number of regular files retained from one package.
	MaxFiles = 2048
	// MaxExpandedBytes bounds the aggregate uncompressed file content.
	MaxExpandedBytes int64 = 64 * 1024 * 1024
	// MaxEntryBytes bounds any single uncompressed file.
	MaxEntryBytes int64 = MaxExpandedBytes
	// MaxPathBytes bounds retained tar path metadata.
	MaxPathBytes = 4 * 1024
	// MaxArchiveBytes leaves room for tar framing around expanded file content.
	MaxArchiveBytes int64 = MaxExpandedBytes + 16*1024*1024
	// MaxCompressedBytes bounds downloaded tar.gz bodies with framing headroom.
	MaxCompressedBytes int64 = MaxArchiveBytes + 1024*1024
)
