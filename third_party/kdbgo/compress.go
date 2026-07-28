package kdb

import (
	"encoding/binary"
	"fmt"
	"math"
)

// Compress applies q IPC compression to a complete encoded frame. Inputs that
// are too small, malformed, too large for IPC, or do not compress smaller are
// returned unchanged.
func Compress(src []byte) []byte {
	if len(src) <= 17 || uint64(len(src)) > math.MaxUint32 || len(src) < 12 {
		return src
	}
	order, ok := byteOrder(src[0])
	if !ok ||
		src[1] > byte(RESPONSE) ||
		src[2] != 0 ||
		src[3] != 0 ||
		uint64(order.Uint32(src[4:8])) != uint64(len(src)) {
		return src
	}

	dst := make([]byte, len(src))
	copy(dst[:4], src[:4])
	dst[2] = 1
	order.PutUint32(dst[8:12], uint32(len(src)))

	var table [256]int
	var flags byte
	var flagIndex byte
	var pendingHash byte
	pendingPosition := 0
	flagOffset := 12
	writeOffset := 12
	readOffset := 8

	for readOffset < len(src) {
		if flagIndex == 0 {
			if writeOffset >= len(dst) {
				return src
			}
			flagIndex = 1
			flagOffset = writeOffset
			writeOffset++
			flags = 0
		}

		literal := readOffset > len(src)-3
		hash := byte(0)
		match := 0
		if !literal {
			hash = src[readOffset] ^ src[readOffset+1]
			match = table[hash]
			literal = match == 0 || src[readOffset] != src[match]
		}

		if pendingPosition > 0 {
			table[pendingHash] = pendingPosition
			pendingPosition = 0
		}
		if literal {
			if writeOffset >= len(dst) {
				return src
			}
			pendingHash = hash
			pendingPosition = readOffset
			dst[writeOffset] = src[readOffset]
			writeOffset++
			readOffset++
		} else {
			if writeOffset+2 > len(dst) {
				return src
			}
			table[hash] = readOffset
			flags |= flagIndex
			match += 2
			readOffset += 2
			start := readOffset
			limit := readOffset + 255
			if limit > len(src) {
				limit = len(src)
			}
			for readOffset < limit && src[match] == src[readOffset] {
				readOffset++
				if readOffset < limit {
					match++
				}
			}
			dst[writeOffset] = hash
			dst[writeOffset+1] = byte(readOffset - start)
			writeOffset += 2
		}

		dst[flagOffset] = flags
		flagIndex <<= 1
	}

	if writeOffset >= len(src)/2 {
		return src
	}
	order.PutUint32(dst[4:8], uint32(writeOffset))
	return dst[:writeOffset:writeOffset]
}

// Uncompress preserves the legacy API while making malformed input
// panic-free. It returns nil when the compressed body is invalid or expands
// beyond DefaultDecodeLimits.
func Uncompress(src []byte) []byte {
	dst, err := UncompressBounded(src, DefaultDecodeLimits().MaxUncompressedFrame)
	if err != nil {
		return nil
	}
	return dst
}

// UncompressBounded expands a compressed q IPC body (the bytes after the
// eight-byte wire header) using little-endian size encoding.
func UncompressBounded(src []byte, maxExpanded uint64) ([]byte, error) {
	return uncompressBounded(src, maxExpanded, binary.LittleEndian)
}

func uncompressBounded(src []byte, maxExpanded uint64, order binary.ByteOrder) ([]byte, error) {
	if len(src) < 6 {
		return nil, fmt.Errorf("compressed IPC body is too short: got %d bytes", len(src))
	}
	expanded := uint64(order.Uint32(src[:4]))
	if expanded < 9 {
		return nil, fmt.Errorf("compressed IPC expanded size %d is smaller than the minimum frame", expanded)
	}
	if maxExpanded == 0 {
		return nil, fmt.Errorf("compressed IPC expanded size limit must be positive")
	}
	if expanded > maxExpanded {
		return nil, fmt.Errorf("compressed IPC expanded size %d exceeds limit %d", expanded, maxExpanded)
	}
	maxInt := uint64(^uint(0) >> 1)
	if expanded > maxInt {
		return nil, fmt.Errorf("compressed IPC expanded size %d does not fit in memory", expanded)
	}

	dst := make([]byte, int(expanded))
	var table [256]int
	readOffset := 4
	writeOffset := 8
	hashPosition := 8
	var flags byte
	var flagBit byte

	for writeOffset < len(dst) {
		if flagBit == 0 {
			if readOffset >= len(src) {
				return nil, fmt.Errorf("compressed IPC stream ended before control byte at output offset %d", writeOffset)
			}
			flags = src[readOffset]
			readOffset++
			flagBit = 1
		}

		isReference := flags&flagBit != 0
		hashUpdateLimit := writeOffset
		if isReference {
			if readOffset+2 > len(src) {
				return nil, fmt.Errorf("compressed IPC reference is truncated at source offset %d", readOffset)
			}
			hash := src[readOffset]
			extra := int(src[readOffset+1])
			readOffset += 2
			reference := table[hash]
			length := 2 + extra
			if reference < 8 || reference >= writeOffset {
				return nil, fmt.Errorf("compressed IPC reference %d is invalid at output offset %d", reference, writeOffset)
			}
			if length > len(dst)-writeOffset {
				return nil, fmt.Errorf("compressed IPC reference length %d exceeds remaining output %d", length, len(dst)-writeOffset)
			}
			for i := 0; i < length; i++ {
				source := reference + i
				if source >= writeOffset+i {
					return nil, fmt.Errorf("compressed IPC reference reads unwritten output at offset %d", source)
				}
				dst[writeOffset+i] = dst[source]
			}
			writeOffset += length
			// q's codec updates the hash table for the first two expanded
			// bytes, then skips the remainder of a back-reference.
			hashUpdateLimit += 2
		} else {
			if readOffset >= len(src) {
				return nil, fmt.Errorf("compressed IPC literal is truncated at source offset %d", readOffset)
			}
			dst[writeOffset] = src[readOffset]
			readOffset++
			writeOffset++
			hashUpdateLimit = writeOffset
		}

		for hashPosition < hashUpdateLimit-1 {
			hash := dst[hashPosition] ^ dst[hashPosition+1]
			table[hash] = hashPosition
			hashPosition++
		}
		if isReference {
			hashPosition = writeOffset
		}
		flagBit <<= 1
	}
	if readOffset != len(src) {
		return nil, fmt.Errorf("compressed IPC stream has %d trailing source bytes", len(src)-readOffset)
	}
	return dst, nil
}

func byteOrder(marker byte) (binary.ByteOrder, bool) {
	switch marker {
	case 0:
		return binary.BigEndian, true
	case 1:
		return binary.LittleEndian, true
	default:
		return nil, false
	}
}
