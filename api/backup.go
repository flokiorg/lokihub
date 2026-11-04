package api

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"archive/zip"
	"os"
	"path/filepath"
	"slices"

	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/logger"
	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/pbkdf2"
)

// backupMagic prefixes backups written by encryptingWriter starting with the
// versioned format (backupFormatCTR). Legacy backups (pre-dating this
// versioning) have no prefix at all - their first bytes are just the random
// salt - so a 4-byte magic keeps the false-positive rate of misreading a
// legacy salt as a version header astronomically low (unlike a 1-byte
// marker, which a random salt byte would collide with 1/256 of the time).
var backupMagic = []byte("LKHB")

// Backup format versions (the byte after backupMagic).
//
//   - backupFormatCTR: AES-CTR keyed by PBKDF2-HMAC-SHA256 at 4096 iterations
//     with an 8-byte salt. Superseded — 4096 iterations is GPU-brute-forceable,
//     far weaker than the argon2 used for config at rest, yet this archive is
//     the most exposed artifact (it leaves the device and bundles the live
//     admin.macaroon). Read for restore only.
//   - backupFormatArgon2idCTR: AES-CTR keyed by argon2id with a 16-byte salt.
//     argon2id made the KDF memory-hard, but AES-CTR is an *unauthenticated*
//     stream cipher: a stored archive is malleable (bit-flippable) and a wrong
//     password decrypts to garbage with no clean error. Read for restore only.
//   - backupFormatArgon2idGCM: chunked AES-256-GCM (AEAD) keyed by argon2id with
//     a 16-byte salt — the current format. The whole archive is authenticated,
//     so tampering, truncation and a wrong password all fail with a clean,
//     detectable error instead of silently restoring corrupt or garbage state.
//     See backupstream.go for the chunk framing.
//
// AES-OFB (the unversioned legacy format, no magic prefix) is also read for
// restore; it too is an unauthenticated stream cipher and never written anymore.
const (
	backupFormatCTR         = 0x02
	backupFormatArgon2idCTR = 0x03
	backupFormatArgon2idGCM = 0x04
)

// Backup KDF parameters. argon2id costs are in the same range as config at rest
// (config/aesgcm.go) and comfortably above the OWASP minimum; the 16-byte salt
// replaces the superseded format's 8-byte salt.
const (
	backupArgon2idTime    = 3
	backupArgon2idMemory  = 64 * 1024 // KiB (64 MiB)
	backupArgon2idThreads = 4
	backupSaltLenCurrent  = 16
	backupSaltLenLegacy   = 8
)

// deriveBackupKeyArgon2id derives the 32-byte AES key for the current backup
// format.
func deriveBackupKeyArgon2id(password string, salt []byte) []byte {
	return argon2.IDKey([]byte(password), salt, backupArgon2idTime, backupArgon2idMemory, backupArgon2idThreads, 32)
}

// deriveBackupKeyPBKDF2 derives the key for the superseded/legacy formats, read
// only for restore.
func deriveBackupKeyPBKDF2(password string, salt []byte) []byte {
	return pbkdf2.Key([]byte(password), salt, 4096, 32, sha256.New)
}

// maxBackupEntrySize caps how much a single zip entry may decompress to when
// restoring a backup, guarding against a decompression bomb filling the
// disk. Generous enough to cover a Lightning node's db/channel state.
const maxBackupEntrySize = 20 * 1024 * 1024 * 1024 // 20 GiB

func (api *api) CreateBackup(unlockPassword string, w io.Writer) (err error) {
	logger.Logger.Info().Msg("Creating backup to migrate Lokihub to another device")

	if !api.cfg.CheckUnlockPassword(unlockPassword) {
		return errors.New("invalid unlock password")
	}

	autoUnlockPassword, err := api.cfg.Get("AutoUnlockPassword", "")
	if err != nil {
		return err
	}
	if autoUnlockPassword != "" {
		return errors.New("please disable auto-unlock before using this feature")
	}

	if api.db.Name() != "sqlite" {
		return errors.New("migration with non-sqlite backend is currently not supported")
	}

	workDir, err := filepath.Abs(api.cfg.GetEnv().Workdir)
	if err != nil {
		return fmt.Errorf("failed to get absolute workdir: %w", err)
	}

	lnStorageDir := ""

	if api.svc.GetLNClient() == nil {
		return fmt.Errorf("node not running")
	}
	lnStorageDir, err = api.svc.GetLNClient().GetStorageDir()
	if err != nil {
		return fmt.Errorf("failed to get storage dir: %w", err)
	}
	logger.Logger.Info().Interface("path", lnStorageDir).Msg("Found node storage dir")

	// Reset the routing data to decrease the LDK DB size
	err = api.svc.GetLNClient().ResetRouter("ALL")
	if err != nil {
		logger.Logger.Error().Err(err).Msg("Failed to reset router")
		return fmt.Errorf("failed to reset router: %w", err)
	}
	// Stop the app to ensure no new requests are processed.
	stopCtx, stopCancel := context.WithTimeout(context.Background(), constants.APP_SHUTDOWN_TIMEOUT)
	defer stopCancel()
	api.svc.StopApp(stopCtx)

	// Closing the database leaves the service in an inconsistent state,
	// but that should not be a problem since the app is not expected
	// to be used after its data is exported.
	err = db.Stop(api.db)
	if err != nil {
		logger.Logger.Error().Err(err).Msg("Failed to stop database")
		return fmt.Errorf("failed to close database: %w", err)
	}

	var filesToArchive []string

	if lnStorageDir != "" {
		lnFiles, err := filepath.Glob(filepath.Join(workDir, lnStorageDir, "*"))
		if err != nil {
			return fmt.Errorf("failed to list files in the LNClient storage directory: %w", err)
		}
		logger.Logger.Info().Interface("lnFiles", lnFiles).Msg("Listed node storage dir")

		// Avoid backing up log files.
		lnFiles = slices.DeleteFunc(lnFiles, func(s string) bool {
			return filepath.Ext(s) == ".log"
		})

		filesToArchive = append(filesToArchive, lnFiles...)
	}

	cw, err := encryptingWriter(w, unlockPassword)
	if err != nil {
		return fmt.Errorf("failed to create encrypted writer: %w", err)
	}

	zw := zip.NewWriter(cw)
	// Finalisation order matters: zw.Close() first flushes the zip's central
	// directory into the encrypting writer, then cw.Close() seals the final
	// AEAD chunk. A failed zw.Close() means the central directory never got
	// flushed (archive truncated/corrupt); a failed cw.Close() means the final
	// authenticated chunk was never written, so the archive would not restore.
	finalized := false
	finalize := func() error {
		if finalized {
			return nil
		}
		finalized = true
		zerr := zw.Close()
		cerr := cw.Close()
		if zerr != nil {
			return fmt.Errorf("failed to finalize backup zip archive: %w", zerr)
		}
		if cerr != nil {
			return fmt.Errorf("failed to finalize encrypted backup stream: %w", cerr)
		}
		return nil
	}
	defer func() {
		if ferr := finalize(); ferr != nil && err == nil {
			err = ferr
		}
	}()

	addFileToZip := func(fsPath, zipPath string) error {
		inF, err := os.Open(fsPath) //nolint:gosec // fsPath comes from this node's own configured DatabaseUri/workdir, not external input
		if err != nil {
			return fmt.Errorf("failed to open source file for reading: %w", err)
		}
		defer func() { _ = inF.Close() }()

		outW, err := zw.Create(zipPath)
		if err != nil {
			return fmt.Errorf("failed to create zip entry: %w", err)
		}

		_, err = io.Copy(outW, inF)
		return err
	}

	// Locate the main database file.
	dbFilePath := api.cfg.GetEnv().DatabaseUri
	// Add the database file to the archive.
	logger.Logger.Info().Interface("nwc.db", dbFilePath).Msg("adding nwc db to zip")
	err = addFileToZip(dbFilePath, "nwc.db")
	if err != nil {
		logger.Logger.Error().Err(err).Msg("Failed to zip nwc db")
		return fmt.Errorf("failed to write nwc db file to zip: %w", err)
	}

	for _, fileToArchive := range filesToArchive {
		logger.Logger.Info().Interface("fileToArchive", fileToArchive).Msg("adding file to zip")
		relPath, err := filepath.Rel(workDir, fileToArchive)
		if err != nil {
			logger.Logger.Error().Err(err).Msg("Failed to get relative path of input file")
			return fmt.Errorf("failed to get relative path of input file: %w", err)
		}

		// Ensure forward slashes for zip format compatibility.
		err = addFileToZip(fileToArchive, filepath.ToSlash(relPath))
		if err != nil {
			logger.Logger.Error().Err(err).Msg("Failed to write file to zip")
			return fmt.Errorf("failed to write input file to zip: %w", err)
		}
	}

	// Finalise explicitly so a failure to flush/seal the archive is returned to
	// the caller rather than only surfacing (and being swallowed) in the defer.
	if err := finalize(); err != nil {
		return err
	}

	logger.Logger.Info().Msg("Successfully created backup to migrate Lokihub to another device")

	return nil
}

func (api *api) RestoreBackup(unlockPassword string, r io.Reader) error {
	logger.Logger.Info().Msg("Restoring migration backup file")

	workDir, err := filepath.Abs(api.cfg.GetEnv().Workdir)
	if err != nil {
		return fmt.Errorf("failed to get absolute workdir: %w", err)
	}

	if strings.HasPrefix(api.cfg.GetEnv().DatabaseUri, "file:") {
		return errors.New("cannot restore backup when database path is a file URI")
	}

	if api.db.Name() != "sqlite" {
		return errors.New("migration to non-sqlite backend is currently not supported")
	}

	cr, err := decryptingReader(r, unlockPassword)
	if err != nil {
		return fmt.Errorf("failed to create decrypted reader: %w", err)
	}

	tmpF, err := os.CreateTemp(api.cfg.GetEnv().Workdir, "lokihub-*.bkp")
	if err != nil {
		return fmt.Errorf("failed to create temporary output file: %w", err)
	}
	tmpName := tmpF.Name()
	defer func() {
		if err := os.Remove(tmpName); err != nil {
			logger.Logger.Warn().Err(err).Str("path", tmpName).Msg("Failed to remove temporary restore file")
		}
	}()
	defer func() { _ = tmpF.Close() }()

	zipSize, err := io.Copy(tmpF, cr)
	if err != nil {
		return fmt.Errorf("failed to decrypt backup data into temporary file: %w", err)
	}

	if err = tmpF.Sync(); err != nil {
		return fmt.Errorf("failed to flush temporary file: %w", err)
	}

	if _, err = tmpF.Seek(0, 0); err != nil {
		return fmt.Errorf("failed to seek to beginning of temporary file: %w", err)
	}

	zr, err := zip.NewReader(tmpF, zipSize)
	if err != nil {
		return fmt.Errorf("failed to create zip reader: %w", err)
	}

	restoreDir := filepath.Join(workDir, "restore")

	extractZipEntry := func(zipFile *zip.File) error {
		fsFilePath := filepath.Join(restoreDir, filepath.FromSlash(zipFile.Name))

		// zip-slip guard: a crafted entry name like "../../etc/passwd" would
		// otherwise let a malicious backup file write outside restoreDir.
		if fsFilePath != restoreDir && !strings.HasPrefix(fsFilePath, restoreDir+string(os.PathSeparator)) {
			return fmt.Errorf("zip entry %q escapes restore directory", zipFile.Name)
		}

		if err = os.MkdirAll(filepath.Dir(fsFilePath), 0700); err != nil {
			return fmt.Errorf("failed to create directory for zip entry: %w", err)
		}

		inF, err := zipFile.Open()
		if err != nil {
			return fmt.Errorf("failed to open zip entry for reading: %w", err)
		}
		defer func() { _ = inF.Close() }()

		outF, err := os.OpenFile(fsFilePath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600) //nolint:gosec // fsFilePath is checked above to stay within restoreDir
		if err != nil {
			return fmt.Errorf("failed to create destination file: %w", err)
		}
		defer func() {
			// A failed Close here means a restored file may be truncated
			// on disk without RestoreBackup's caller ever finding out.
			if err := outF.Close(); err != nil {
				logger.Logger.Error().Err(err).Str("path", fsFilePath).Msg("Failed to close restored file")
			}
		}()

		// Cap decompressed output regardless of what the zip's central
		// directory claims, guarding against a decompression bomb filling
		// the disk.
		written, err := io.CopyN(outF, inF, maxBackupEntrySize+1)
		if err != nil && !errors.Is(err, io.EOF) {
			return fmt.Errorf("failed to write zip entry to destination file: %w", err)
		}
		if written > maxBackupEntrySize {
			return fmt.Errorf("zip entry %q exceeds maximum allowed size", zipFile.Name)
		}

		return nil
	}

	logger.Logger.Info().Int("count", len(zr.File)).Msg("Extracting files")
	for _, f := range zr.File {
		logger.Logger.Info().Interface("file", f.Name).Msg("Extracting file")
		if err = extractZipEntry(f); err != nil {
			return fmt.Errorf("failed to extract zip entry: %w", err)
		}
	}
	logger.Logger.Info().Int("count", len(zr.File)).Msg("Extracted files")

	go func() {
		logger.Logger.Info().Msg("Backup restored. Shutting down Lokihub...")
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), constants.APP_SHUTDOWN_TIMEOUT)
		defer shutdownCancel()
		api.svc.Shutdown(shutdownCtx)
		// ensure no -shm or -wal files exist as they will stop the restore
		for _, filename := range []string{"nwc.db", "nwc.db-shm", "nwc.db-wal"} {
			err = os.Remove(filepath.Join(workDir, filename))
			if err != nil {
				logger.Logger.Error().Err(err).Str("filename", filename).Msg("failed to remove old nwc db file before restore")
			}
		}

		// schedule node shutdown after a few seconds to ensure frontend updates
		time.Sleep(5 * time.Second)
		os.Exit(0)
	}()

	return nil
}

// encryptingWriter returns the writer for NEW backups: chunked AES-256-GCM
// (AEAD) keyed by argon2id. The returned writer MUST be closed to seal the final
// authenticated chunk (see CreateBackup's finalize step).
func encryptingWriter(w io.Writer, password string) (io.WriteCloser, error) {
	salt := make([]byte, backupSaltLenCurrent)
	if _, err := rand.Read(salt); err != nil {
		return nil, fmt.Errorf("failed to generate salt: %w", err)
	}

	encKey := deriveBackupKeyArgon2id(password, salt)
	aead, err := newBackupAEAD(encKey)
	if err != nil {
		return nil, err
	}

	if _, err = w.Write(backupMagic); err != nil {
		return nil, fmt.Errorf("failed to write format magic: %w", err)
	}
	if _, err = w.Write([]byte{backupFormatArgon2idGCM}); err != nil {
		return nil, fmt.Errorf("failed to write format version: %w", err)
	}
	if _, err = w.Write(salt); err != nil {
		return nil, fmt.Errorf("failed to write salt: %w", err)
	}

	return newAEADChunkWriter(w, aead), nil
}

// newBackupAEAD builds the AES-256-GCM AEAD used for the authenticated backup
// format.
func newBackupAEAD(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("failed to create AES cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("failed to create AES-GCM: %w", err)
	}
	return aead, nil
}

// decryptingReader dispatches on the backup format version and returns a reader
// over the plaintext. The current format (backupFormatArgon2idGCM) is
// authenticated: a tampered archive or wrong password surfaces as a read error.
// The older unauthenticated formats (argon2id/PBKDF2 AES-CTR and legacy AES-OFB)
// are still read so existing backups keep restoring.
func decryptingReader(r io.Reader, password string) (io.Reader, error) {
	header := make([]byte, len(backupMagic)+1)
	n, err := io.ReadFull(r, header)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
		return nil, fmt.Errorf("failed to read backup header: %w", err)
	}

	if n == len(header) && bytes.Equal(header[:len(backupMagic)], backupMagic) {
		switch header[len(backupMagic)] {
		case backupFormatArgon2idGCM:
			return newAEADDecryptingReader(r, password)
		case backupFormatArgon2idCTR:
			return newStreamDecryptingReader(r, password, cipher.NewCTR, deriveBackupKeyArgon2id, backupSaltLenCurrent)
		case backupFormatCTR:
			return newStreamDecryptingReader(r, password, cipher.NewCTR, deriveBackupKeyPBKDF2, backupSaltLenLegacy)
		default:
			return nil, fmt.Errorf("unsupported backup format version: %d", header[len(backupMagic)])
		}
	}

	// No recognized magic/version prefix: assume a legacy (pre-versioning)
	// OFB-encrypted backup, whose first bytes are the random salt itself.
	// Splice the bytes already consumed from r back onto the front.
	r = io.MultiReader(bytes.NewReader(header[:n]), r)
	return newStreamDecryptingReader(r, password, cipher.NewOFB, deriveBackupKeyPBKDF2, backupSaltLenLegacy) //nolint:staticcheck // required to decrypt pre-versioning backups; new backups use backupFormatArgon2idGCM
}

// newAEADDecryptingReader reads the argon2id salt header and returns the chunked
// AES-256-GCM reader for the current authenticated backup format.
func newAEADDecryptingReader(r io.Reader, password string) (io.Reader, error) {
	salt := make([]byte, backupSaltLenCurrent)
	if _, err := io.ReadFull(r, salt); err != nil {
		return nil, fmt.Errorf("failed to read salt: %w", err)
	}

	encKey := deriveBackupKeyArgon2id(password, salt)
	aead, err := newBackupAEAD(encKey)
	if err != nil {
		return nil, err
	}

	return newAEADChunkReader(r, aead), nil
}

// newStreamDecryptingReader reads the salt + IV header and returns a reader over
// one of the superseded unauthenticated stream-cipher formats (AES-CTR / legacy
// AES-OFB), kept for restoring existing backups only.
func newStreamDecryptingReader(
	r io.Reader,
	password string,
	newStream func(block cipher.Block, iv []byte) cipher.Stream,
	deriveKey func(password string, salt []byte) []byte,
	saltLen int,
) (io.Reader, error) {
	salt := make([]byte, saltLen)
	if _, err := io.ReadFull(r, salt); err != nil {
		return nil, fmt.Errorf("failed to read salt: %w", err)
	}

	iv := make([]byte, aes.BlockSize)
	if _, err := io.ReadFull(r, iv); err != nil {
		return nil, fmt.Errorf("failed to read IV: %w", err)
	}

	encKey := deriveKey(password, salt)
	block, err := aes.NewCipher(encKey)
	if err != nil {
		return nil, fmt.Errorf("failed to create AES cipher: %w", err)
	}

	return &cipher.StreamReader{S: newStream(block, iv), R: r}, nil
}
