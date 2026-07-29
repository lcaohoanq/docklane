package installmanifest

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"syscall"
	"time"

	"docklane.local/docklane/internal/domain"
)

var (
	ErrNotFound           = errors.New("installation manifest not found")
	ErrAlreadyExists      = errors.New("installation manifest already exists")
	ErrGenerationConflict = errors.New("installation manifest generation changed")
	ErrUpgradeRequired    = errors.New("installation manifest upgrade required")
)

type Store struct {
	path string
}

type UpgradeSource struct {
	Manifest    domain.InstallationManifest
	Fingerprint string
}

func NewStore(path string) (*Store, error) {
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("installation manifest path must be absolute")
	}
	return &Store{path: filepath.Clean(path)}, nil
}

func New(
	productVersion string,
	baseDomain string,
	proxyNetwork string,
	now time.Time,
) (domain.InstallationManifest, error) {
	installationID, err := newInstallationID()
	if err != nil {
		return domain.InstallationManifest{}, err
	}
	now = now.UTC()
	manifest := domain.InstallationManifest{
		SchemaVersion:  domain.InstallationManifestSchemaVersion,
		InstallationID: installationID,
		Generation:     1,
		ProductVersion: productVersion,
		State:          domain.InstallationPlanned,
		CreatedAt:      now,
		UpdatedAt:      now,
		Settings: domain.InstallationSettings{
			BaseDomain:   baseDomain,
			ProxyNetwork: proxyNetwork,
		},
		Resources: []domain.InstallationResource{},
	}
	if err := manifest.Validate(); err != nil {
		return domain.InstallationManifest{}, err
	}
	return manifest, nil
}

func (store *Store) Path() string {
	return store.path
}

func (store *Store) Load() (domain.InstallationManifest, error) {
	return store.loadUnlocked()
}

func (store *Store) LoadForUpgrade() (UpgradeSource, error) {
	source, _, err := store.loadUpgradeSourceUnlocked()
	return source, err
}

func (store *Store) UpgradeBackupPath(
	schemaVersion int,
	generation uint64,
) string {
	return fmt.Sprintf(
		"%s.schema-v%d-generation-%d.bak",
		store.path,
		schemaVersion,
		generation,
	)
}

func (store *Store) Create(manifest domain.InstallationManifest) error {
	if manifest.Generation != 1 {
		return fmt.Errorf("new installation manifest generation must be 1")
	}
	if err := manifest.Validate(); err != nil {
		return err
	}
	return store.withLock(true, func() error {
		if _, err := os.Lstat(store.path); err == nil {
			return ErrAlreadyExists
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return store.writeUnlocked(manifest)
	})
}

func (store *Store) Save(
	expectedGeneration uint64,
	manifest domain.InstallationManifest,
) error {
	if manifest.Generation != expectedGeneration+1 {
		return fmt.Errorf(
			"replacement manifest generation must be %d",
			expectedGeneration+1,
		)
	}
	if err := manifest.Validate(); err != nil {
		return err
	}
	return store.withLock(false, func() error {
		current, err := store.loadUnlocked()
		if err != nil {
			return err
		}
		if current.Generation != expectedGeneration {
			return ErrGenerationConflict
		}
		if current.InstallationID != manifest.InstallationID ||
			current.CreatedAt != manifest.CreatedAt {
			return fmt.Errorf("installation identity and creation time are immutable")
		}
		if !manifest.UpdatedAt.After(current.UpdatedAt) {
			return fmt.Errorf("replacement manifest updatedAt must advance")
		}
		return store.writeUnlocked(manifest)
	})
}

func (store *Store) ApplyUpgrade(
	expectedGeneration uint64,
	expectedSchemaVersion int,
	expectedFingerprint string,
	manifest domain.InstallationManifest,
) error {
	if manifest.Generation != expectedGeneration+1 {
		return fmt.Errorf(
			"upgraded manifest generation must be %d",
			expectedGeneration+1,
		)
	}
	if manifest.SchemaVersion != domain.InstallationManifestSchemaVersion {
		return fmt.Errorf(
			"upgraded manifest schema version must be %d",
			domain.InstallationManifestSchemaVersion,
		)
	}
	if err := manifest.Validate(); err != nil {
		return err
	}
	return store.withLock(false, func() error {
		source, raw, err := store.loadUpgradeSourceUnlocked()
		if err != nil {
			return err
		}
		if source.Manifest.Generation != expectedGeneration {
			return ErrGenerationConflict
		}
		if source.Manifest.SchemaVersion != expectedSchemaVersion {
			return fmt.Errorf(
				"installation manifest schema changed from reviewed version %d to %d",
				expectedSchemaVersion,
				source.Manifest.SchemaVersion,
			)
		}
		if source.Fingerprint != expectedFingerprint {
			return fmt.Errorf(
				"installation manifest content changed after upgrade review",
			)
		}
		if source.Manifest.InstallationID != manifest.InstallationID ||
			source.Manifest.CreatedAt != manifest.CreatedAt {
			return fmt.Errorf(
				"installation identity and creation time are immutable",
			)
		}
		if !manifest.UpdatedAt.After(source.Manifest.UpdatedAt) {
			return fmt.Errorf("upgraded manifest updatedAt must advance")
		}
		if len(manifest.UpgradeHistory) !=
			len(source.Manifest.UpgradeHistory)+1 {
			return fmt.Errorf(
				"upgraded manifest must append exactly one history record",
			)
		}
		for index := range source.Manifest.UpgradeHistory {
			if !reflect.DeepEqual(
				manifest.UpgradeHistory[index],
				source.Manifest.UpgradeHistory[index],
			) {
				return fmt.Errorf(
					"upgraded manifest changed prior upgrade history",
				)
			}
		}
		record := manifest.UpgradeHistory[len(manifest.UpgradeHistory)-1]
		if record.FromSchemaVersion != source.Manifest.SchemaVersion ||
			record.ToSchemaVersion != manifest.SchemaVersion ||
			!record.AppliedAt.Equal(manifest.UpdatedAt) {
			return fmt.Errorf(
				"upgrade history does not describe the atomic schema transition",
			)
		}
		backupPath := store.UpgradeBackupPath(
			source.Manifest.SchemaVersion,
			source.Manifest.Generation,
		)
		if record.SourceBackup.Path != backupPath ||
			record.SourceBackup.Fingerprint != source.Fingerprint {
			return fmt.Errorf(
				"upgrade history does not match the reviewed source backup",
			)
		}
		expected := source.Manifest
		expected.SchemaVersion = manifest.SchemaVersion
		expected.Generation = manifest.Generation
		expected.UpdatedAt = manifest.UpdatedAt
		expected.UpgradeHistory = manifest.UpgradeHistory
		if !reflect.DeepEqual(expected, manifest) {
			return fmt.Errorf(
				"schema migration may change only manifest version, generation, " +
					"timestamp, and upgrade history",
			)
		}
		if err := ensureUpgradeBackup(
			backupPath,
			raw,
			source.Fingerprint,
		); err != nil {
			return err
		}
		return store.writeUnlocked(manifest)
	})
}

func (store *Store) loadUnlocked() (domain.InstallationManifest, error) {
	source, _, err := store.loadUpgradeSourceUnlocked()
	if err != nil {
		return domain.InstallationManifest{}, err
	}
	if source.Manifest.SchemaVersion !=
		domain.InstallationManifestSchemaVersion {
		return domain.InstallationManifest{}, fmt.Errorf(
			"%w: found schema v%d, current schema is v%d; "+
				"run docklane upgrade --dry-run",
			ErrUpgradeRequired,
			source.Manifest.SchemaVersion,
			domain.InstallationManifestSchemaVersion,
		)
	}
	return source.Manifest, nil
}

func (store *Store) loadUpgradeSourceUnlocked() (
	UpgradeSource,
	[]byte,
	error,
) {
	manifest, raw, err := store.decodeUnlocked()
	if err != nil {
		return UpgradeSource{}, nil, err
	}
	switch manifest.SchemaVersion {
	case domain.InstallationManifestSchemaVersion:
		if err := manifest.Validate(); err != nil {
			return UpgradeSource{}, nil, fmt.Errorf(
				"validate installation manifest: %w",
				err,
			)
		}
	case 1:
		if err := domain.ValidateLegacyInstallationManifestV1(manifest); err != nil {
			return UpgradeSource{}, nil, fmt.Errorf(
				"validate installation manifest schema v1: %w",
				err,
			)
		}
	default:
		return UpgradeSource{}, nil, fmt.Errorf(
			"unsupported installation manifest schema version %d",
			manifest.SchemaVersion,
		)
	}
	fingerprint := sha256.Sum256(raw)
	return UpgradeSource{
		Manifest:    manifest,
		Fingerprint: hex.EncodeToString(fingerprint[:]),
	}, raw, nil
}

func (store *Store) decodeUnlocked() (
	domain.InstallationManifest,
	[]byte,
	error,
) {
	info, err := os.Lstat(store.path)
	if errors.Is(err, os.ErrNotExist) {
		return domain.InstallationManifest{}, nil, ErrNotFound
	}
	if err != nil {
		return domain.InstallationManifest{}, nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return domain.InstallationManifest{}, nil, fmt.Errorf(
			"installation manifest cannot be a symbolic link",
		)
	}
	if !info.Mode().IsRegular() {
		return domain.InstallationManifest{}, nil, fmt.Errorf(
			"installation manifest must be a regular file",
		)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return domain.InstallationManifest{}, nil, fmt.Errorf(
			"installation manifest permissions must not allow group or world access",
		)
	}
	if info.Size() > 4<<20 {
		return domain.InstallationManifest{}, nil, fmt.Errorf(
			"installation manifest exceeds the 4 MiB limit",
		)
	}
	raw, err := os.ReadFile(store.path)
	if err != nil {
		return domain.InstallationManifest{}, nil, err
	}
	if len(raw) > 4<<20 {
		return domain.InstallationManifest{}, nil, fmt.Errorf(
			"installation manifest exceeds the 4 MiB limit",
		)
	}
	decoder := json.NewDecoder(
		io.LimitReader(bytes.NewReader(raw), 4<<20),
	)
	decoder.DisallowUnknownFields()
	var manifest domain.InstallationManifest
	if err := decoder.Decode(&manifest); err != nil {
		return domain.InstallationManifest{}, nil, fmt.Errorf(
			"decode installation manifest: %w",
			err,
		)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return domain.InstallationManifest{}, nil, err
	}
	return manifest, raw, nil
}

func (store *Store) writeUnlocked(manifest domain.InstallationManifest) error {
	directory := filepath.Dir(store.path)
	if err := os.MkdirAll(directory, 0o750); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".docklane-manifest-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	encoder := json.NewEncoder(temporary)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(manifest); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, store.path); err != nil {
		return err
	}
	directoryHandle, err := os.Open(directory)
	if err != nil {
		return err
	}
	defer directoryHandle.Close()
	return directoryHandle.Sync()
}

func (store *Store) withLock(createDirectory bool, operation func() error) error {
	directory := filepath.Dir(store.path)
	if createDirectory {
		if err := os.MkdirAll(directory, 0o750); err != nil {
			return err
		}
	}
	lock, err := os.OpenFile(store.path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if errors.Is(err, os.ErrNotExist) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	return operation()
}

func newInstallationID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("generate installation ID: %w", err)
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	encoded := hex.EncodeToString(value)
	return encoded[0:8] + "-" +
		encoded[8:12] + "-" +
		encoded[12:16] + "-" +
		encoded[16:20] + "-" +
		encoded[20:32], nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); errors.Is(err, io.EOF) {
		return nil
	} else if err != nil {
		return fmt.Errorf("decode installation manifest trailer: %w", err)
	}
	return fmt.Errorf("installation manifest contains multiple JSON values")
}

func ensureUpgradeBackup(
	path string,
	content []byte,
	expectedFingerprint string,
) error {
	info, err := os.Lstat(path)
	switch {
	case err == nil:
		if !info.Mode().IsRegular() ||
			info.Mode()&os.ModeSymlink != 0 ||
			info.Mode().Perm() != 0o600 {
			return fmt.Errorf(
				"existing upgrade backup %s is not a mode-0600 regular file",
				path,
			)
		}
		if info.Size() != int64(len(content)) {
			return fmt.Errorf(
				"existing upgrade backup %s does not match the reviewed source",
				path,
			)
		}
		existing, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		fingerprint := sha256.Sum256(existing)
		if hex.EncodeToString(fingerprint[:]) != expectedFingerprint {
			return fmt.Errorf(
				"existing upgrade backup %s does not match the reviewed source",
				path,
			)
		}
		return nil
	case !errors.Is(err, os.ErrNotExist):
		return err
	}
	directoryPath := filepath.Dir(path)
	file, err := os.CreateTemp(directoryPath, ".docklane-upgrade-backup-*")
	if err != nil {
		return err
	}
	temporaryPath := file.Name()
	defer os.Remove(temporaryPath)
	if err := file.Chmod(0o600); err != nil {
		file.Close()
		return err
	}
	if _, err := file.Write(content); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Link(temporaryPath, path); err != nil {
		if errors.Is(err, os.ErrExist) {
			return ensureUpgradeBackup(path, content, expectedFingerprint)
		}
		return err
	}
	directory, err := os.Open(directoryPath)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
