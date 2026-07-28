package installmanifest

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"docklane.local/docklane/internal/domain"
)

var (
	ErrNotFound           = errors.New("installation manifest not found")
	ErrAlreadyExists      = errors.New("installation manifest already exists")
	ErrGenerationConflict = errors.New("installation manifest generation changed")
)

type Store struct {
	path string
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

func (store *Store) loadUnlocked() (domain.InstallationManifest, error) {
	info, err := os.Lstat(store.path)
	if errors.Is(err, os.ErrNotExist) {
		return domain.InstallationManifest{}, ErrNotFound
	}
	if err != nil {
		return domain.InstallationManifest{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return domain.InstallationManifest{}, fmt.Errorf(
			"installation manifest cannot be a symbolic link",
		)
	}
	if !info.Mode().IsRegular() {
		return domain.InstallationManifest{}, fmt.Errorf(
			"installation manifest must be a regular file",
		)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return domain.InstallationManifest{}, fmt.Errorf(
			"installation manifest permissions must not allow group or world access",
		)
	}
	if info.Size() > 4<<20 {
		return domain.InstallationManifest{}, fmt.Errorf(
			"installation manifest exceeds the 4 MiB limit",
		)
	}
	file, err := os.Open(store.path)
	if err != nil {
		return domain.InstallationManifest{}, err
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, 4<<20))
	decoder.DisallowUnknownFields()
	var manifest domain.InstallationManifest
	if err := decoder.Decode(&manifest); err != nil {
		return domain.InstallationManifest{}, fmt.Errorf(
			"decode installation manifest: %w",
			err,
		)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return domain.InstallationManifest{}, err
	}
	if err := manifest.Validate(); err != nil {
		return domain.InstallationManifest{}, fmt.Errorf(
			"validate installation manifest: %w",
			err,
		)
	}
	return manifest, nil
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
