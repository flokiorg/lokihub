package api

import (
	"crypto/cipher"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// Chunked AES-256-GCM ("STREAM"-style) framing for the backup archive.
//
// A stored backup can be many gigabytes (a Lightning node's channel/DB state),
// so it cannot be sealed as a single AEAD message held in memory. Instead the
// plaintext is split into fixed-size chunks, each sealed independently with
// AES-256-GCM under a per-chunk nonce formed from an incrementing counter plus a
// final-chunk flag. This gives the whole archive authenticated encryption while
// still streaming:
//
//   - tampering with any byte makes that chunk's GCM tag fail -> the read errors
//     out cleanly instead of yielding silently corrupt plaintext;
//   - a wrong password derives the wrong key, so the very first chunk fails to
//     open -> a clean, detectable error rather than garbage;
//   - dropping, reordering or duplicating chunks desynchronises the counter
//     nonce, so those chunks fail to open;
//   - truncating the stream is caught because the real last chunk carries the
//     final flag: hitting EOF without having seen it is rejected as truncation.
//
// The per-backup key is derived with a fresh random salt (argon2id), so the
// (key, nonce) pairs never repeat across backups.
const (
	// backupGCMChunkSize is the plaintext size of each chunk.
	backupGCMChunkSize = 64 * 1024
	// backupGCMTagSize is the AES-GCM authentication tag length appended to each
	// sealed chunk.
	backupGCMTagSize = 16
	// backupGCMEncChunkSize is the on-the-wire size of a full (non-final) chunk.
	backupGCMEncChunkSize = backupGCMChunkSize + backupGCMTagSize
)

// aeadStreamNonce builds the per-chunk GCM nonce: an 8-byte big-endian counter
// in the leading bytes and a final-chunk flag in the last byte. The counter
// alone already makes every nonce unique within a backup; the flag exists so a
// truncated prefix of a longer stream cannot pass as complete.
func aeadStreamNonce(size int, counter uint64, final bool) []byte {
	nonce := make([]byte, size)
	binary.BigEndian.PutUint64(nonce[:8], counter)
	if final {
		nonce[size-1] = 1
	}
	return nonce
}

// aeadChunkWriter seals plaintext into chunked AES-GCM. A full chunk is flushed
// as non-final the instant the buffer fills, so the chunk emitted by Close is
// always strictly shorter than a full encrypted chunk (possibly an empty,
// tag-only chunk) — which is exactly how aeadChunkReader recognises the end of
// the stream. Close MUST be called to finalise the archive.
type aeadChunkWriter struct {
	w       io.Writer
	aead    cipher.AEAD
	buf     []byte
	counter uint64
	closed  bool
	err     error
}

func newAEADChunkWriter(w io.Writer, aead cipher.AEAD) *aeadChunkWriter {
	return &aeadChunkWriter{
		w:    w,
		aead: aead,
		buf:  make([]byte, 0, backupGCMChunkSize),
	}
}

func (cw *aeadChunkWriter) Write(p []byte) (int, error) {
	if cw.err != nil {
		return 0, cw.err
	}
	if cw.closed {
		return 0, errors.New("write after close on backup stream")
	}

	written := 0
	for len(p) > 0 {
		n := min(backupGCMChunkSize-len(cw.buf), len(p))
		cw.buf = append(cw.buf, p[:n]...)
		p = p[n:]
		written += n

		if len(cw.buf) == backupGCMChunkSize {
			if err := cw.flush(false); err != nil {
				cw.err = err
				return written, err
			}
		}
	}
	return written, nil
}

// Close seals and writes the trailing bytes as the final chunk. If the stream
// ended exactly on a chunk boundary this is an empty (tag-only) final chunk, so
// the reader always sees an explicit end marker.
func (cw *aeadChunkWriter) Close() error {
	if cw.err != nil {
		return cw.err
	}
	if cw.closed {
		return nil
	}
	cw.closed = true
	if err := cw.flush(true); err != nil {
		cw.err = err
		return err
	}
	return nil
}

func (cw *aeadChunkWriter) flush(final bool) error {
	nonce := aeadStreamNonce(cw.aead.NonceSize(), cw.counter, final)
	sealed := cw.aead.Seal(nil, nonce, cw.buf, nil)
	cw.buf = cw.buf[:0]
	cw.counter++
	if _, err := cw.w.Write(sealed); err != nil {
		return fmt.Errorf("failed to write encrypted backup chunk: %w", err)
	}
	return nil
}

// aeadChunkReader decrypts and authenticates a stream written by
// aeadChunkWriter. It implements io.Reader; a failed authentication (tampering,
// wrong password, truncation, reordering) surfaces as a read error, so callers
// that copy to completion never act on unauthenticated plaintext.
type aeadChunkReader struct {
	r       io.Reader
	aead    cipher.AEAD
	encBuf  []byte
	plain   []byte
	off     int
	counter uint64
	eof     bool
	err     error
}

func newAEADChunkReader(r io.Reader, aead cipher.AEAD) *aeadChunkReader {
	return &aeadChunkReader{
		r:      r,
		aead:   aead,
		encBuf: make([]byte, backupGCMEncChunkSize),
	}
}

func (cr *aeadChunkReader) Read(p []byte) (int, error) {
	// Drain any already-decrypted plaintext first.
	if cr.off < len(cr.plain) {
		n := copy(p, cr.plain[cr.off:])
		cr.off += n
		return n, nil
	}
	if cr.err != nil {
		return 0, cr.err
	}
	if cr.eof {
		return 0, io.EOF
	}
	if err := cr.readChunk(); err != nil {
		cr.err = err
		return 0, err
	}
	n := copy(p, cr.plain[cr.off:])
	cr.off += n
	return n, nil
}

func (cr *aeadChunkReader) readChunk() error {
	n, err := io.ReadFull(cr.r, cr.encBuf)

	final := false
	switch {
	case err == nil:
		// A full-size chunk is always non-final (the writer guarantees the final
		// chunk is strictly shorter).
	case errors.Is(err, io.ErrUnexpectedEOF):
		// A short read is the final chunk.
		final = true
	case errors.Is(err, io.EOF):
		// A clean EOF on a fresh chunk boundary means the stream ended without a
		// final chunk: it was truncated after a non-final chunk.
		return errors.New("backup stream truncated: missing final authenticated chunk")
	default:
		return fmt.Errorf("failed to read encrypted backup chunk: %w", err)
	}

	if n < backupGCMTagSize {
		return errors.New("backup stream truncated: final chunk shorter than authentication tag")
	}

	nonce := aeadStreamNonce(cr.aead.NonceSize(), cr.counter, final)
	plain, err := cr.aead.Open(nil, nonce, cr.encBuf[:n], nil)
	if err != nil {
		return fmt.Errorf("backup authentication failed (wrong password or tampered/corrupt backup): %w", err)
	}

	cr.plain = plain
	cr.off = 0
	cr.counter++
	if final {
		cr.eof = true
	}
	return nil
}
