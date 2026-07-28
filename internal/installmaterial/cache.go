package installmaterial

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"docklane.local/docklane/internal/domain"
	"docklane.local/docklane/internal/installartifacts"
	"docklane.local/docklane/internal/installfiles"
)

const (
	cacheDescriptorName = "cache.json"
	maxCacheFileSize    = 16 << 20
)

type ManifestStore interface {
	Save(uint64, domain.InstallationManifest) error
}

type Materialize func() ([]installfiles.File, error)

type Coordinator struct {
	store ManifestStore
	now   func() time.Time
}

func New(store ManifestStore) (*Coordinator, error) {
	if store == nil {
		return nil, errors.New("manifest store is required")
	}
	return &Coordinator{store: store, now: time.Now}, nil
}

func (coordinator *Coordinator) PrepareArtifacts(
	ctx context.Context,
	manifest domain.InstallationManifest,
	generatedAt time.Time,
	random io.Reader,
) (domain.InstallationManifest, []installfiles.File, error) {
	if manifest.ManagedSpecification == nil {
		return manifest, nil, errors.New(
			"managed specification is required for artifact materialization",
		)
	}
	specification := *manifest.ManagedSpecification
	return coordinator.Prepare(
		ctx,
		manifest,
		func() ([]installfiles.File, error) {
			return installartifacts.MaterializeSelectedFiles(
				specification,
				manifest.ManagedArtifacts,
				generatedAt,
				random,
			)
		},
	)
}

func (coordinator *Coordinator) Prepare(
	ctx context.Context,
	manifest domain.InstallationManifest,
	materialize Materialize,
) (domain.InstallationManifest, []installfiles.File, error) {
	if err := ctx.Err(); err != nil {
		return manifest, nil, err
	}
	artifacts := fileArtifacts(manifest.ManagedArtifacts)
	if len(artifacts) == 0 {
		if manifest.MaterialCache != nil {
			return manifest, nil, errors.New(
				"material cache exists without selected file artifacts",
			)
		}
		return manifest, []installfiles.File{}, nil
	}
	if manifest.ManagedSpecification == nil {
		return manifest, nil, errors.New(
			"managed specification is required for material cache",
		)
	}
	if manifest.MaterialCache != nil {
		if manifest.MaterialCache.State != domain.MaterialCacheReady {
			return manifest, nil, fmt.Errorf(
				"material cache is %s",
				manifest.MaterialCache.State,
			)
		}
		files, err := load(
			manifest,
			*manifest.MaterialCache,
			artifacts,
		)
		return manifest, files, err
	}
	if manifest.Execution != nil {
		return manifest, nil, errors.New(
			"execution journal cannot precede material cache",
		)
	}
	if materialize == nil {
		return manifest, nil, errors.New("materializer is required")
	}
	var cache domain.InstallationMaterialCache
	var files []installfiles.File
	err := withCacheLock(manifest, func() error {
		var loadErr error
		cache, files, loadErr = loadOrphan(manifest, artifacts)
		if loadErr == nil {
			return nil
		}
		if !errors.Is(loadErr, os.ErrNotExist) {
			return loadErr
		}
		generated, err := materialize()
		if err != nil {
			return err
		}
		defer clearSensitive(generated)
		cache, err = create(manifest, artifacts, generated)
		if err != nil {
			return err
		}
		files, err = load(manifest, cache, artifacts)
		return err
	})
	if err != nil {
		clearSensitive(files)
		return manifest, nil, fmt.Errorf("prepare material cache: %w", err)
	}
	next, err := coordinator.checkpoint(
		manifest,
		func(candidate *domain.InstallationManifest) {
			candidate.MaterialCache = copyCache(&cache)
		},
	)
	if err != nil {
		clearSensitive(files)
		return manifest, nil, fmt.Errorf(
			"record material cache: %w",
			err,
		)
	}
	return next, files, nil
}

func (coordinator *Coordinator) Load(
	manifest domain.InstallationManifest,
) ([]installfiles.File, error) {
	if manifest.MaterialCache == nil ||
		manifest.MaterialCache.State != domain.MaterialCacheReady {
		return nil, errors.New("ready material cache is required")
	}
	return load(
		manifest,
		*manifest.MaterialCache,
		fileArtifacts(manifest.ManagedArtifacts),
	)
}

func (coordinator *Coordinator) Clear(
	ctx context.Context,
	manifest domain.InstallationManifest,
) (domain.InstallationManifest, error) {
	if err := ctx.Err(); err != nil {
		return manifest, err
	}
	if manifest.MaterialCache == nil {
		return manifest, nil
	}
	if manifest.MaterialCache.State == domain.MaterialCacheCleared {
		return manifest, nil
	}
	if manifest.Execution == nil ||
		(manifest.Execution.Phase != domain.ExecutionComplete &&
			manifest.Execution.Phase != domain.ExecutionRolledBack &&
			manifest.Execution.Phase != domain.ExecutionFailed) {
		return manifest, errors.New(
			"material cache can only be cleared after terminal execution",
		)
	}
	current := manifest
	if current.MaterialCache.State == domain.MaterialCacheReady {
		var err error
		current, err = coordinator.checkpoint(
			current,
			func(candidate *domain.InstallationManifest) {
				candidate.MaterialCache.State = domain.MaterialCacheClearing
			},
		)
		if err != nil {
			return manifest, fmt.Errorf(
				"mark material cache clearing: %w",
				err,
			)
		}
	}
	if current.MaterialCache.State != domain.MaterialCacheClearing {
		return current, fmt.Errorf(
			"cannot clear material cache in state %s",
			current.MaterialCache.State,
		)
	}
	if err := withCacheLock(current, func() error {
		return remove(current, *current.MaterialCache)
	}); err != nil {
		return current, fmt.Errorf("remove material cache: %w", err)
	}
	cleared, err := coordinator.checkpoint(
		current,
		func(candidate *domain.InstallationManifest) {
			candidate.MaterialCache.State = domain.MaterialCacheCleared
		},
	)
	if err != nil {
		return current, fmt.Errorf(
			"record cleared material cache: %w",
			err,
		)
	}
	return cleared, nil
}

func (coordinator *Coordinator) checkpoint(
	current domain.InstallationManifest,
	change func(*domain.InstallationManifest),
) (domain.InstallationManifest, error) {
	next := cloneManifest(current)
	change(&next)
	next.Generation++
	now := coordinator.now().UTC()
	if !now.After(current.UpdatedAt) {
		now = current.UpdatedAt.Add(time.Nanosecond)
	}
	next.UpdatedAt = now
	if err := coordinator.store.Save(current.Generation, next); err != nil {
		return current, err
	}
	return next, nil
}

func create(
	manifest domain.InstallationManifest,
	artifacts []domain.InstallationArtifact,
	files []installfiles.File,
) (domain.InstallationMaterialCache, error) {
	cache, err := cacheMetadata(manifest, artifacts, files)
	if err != nil {
		return domain.InstallationMaterialCache{}, err
	}
	root := filepath.Dir(cache.Directory)
	if err := ensureCacheRoot(
		manifest.ManagedSpecification.Paths.StateDirectory,
		root,
	); err != nil {
		return domain.InstallationMaterialCache{}, err
	}
	if _, err := os.Lstat(cache.Directory); err == nil {
		return domain.InstallationMaterialCache{}, fmt.Errorf(
			"material cache already exists: %s",
			cache.Directory,
		)
	} else if !errors.Is(err, os.ErrNotExist) {
		return domain.InstallationMaterialCache{}, err
	}
	staging := filepath.Join(
		root,
		"."+manifest.InstallationID+".preparing",
	)
	if err := removeStaging(staging); err != nil {
		return domain.InstallationMaterialCache{}, err
	}
	if err := os.Mkdir(staging, 0o700); err != nil {
		return domain.InstallationMaterialCache{}, err
	}
	keepStaging := false
	defer func() {
		if !keepStaging {
			os.RemoveAll(staging)
		}
	}()
	fileByID := map[string]installfiles.File{}
	for _, file := range files {
		fileByID[file.ID] = file
	}
	for _, entry := range cache.Entries {
		file := fileByID[entry.ArtifactID]
		stagingPath := filepath.Join(staging, filepath.Base(entry.CachePath))
		if err := writePrivateFile(stagingPath, file.Content); err != nil {
			return domain.InstallationMaterialCache{}, err
		}
	}
	encoded, err := json.MarshalIndent(cache, "", "  ")
	if err != nil {
		return domain.InstallationMaterialCache{}, err
	}
	encoded = append(encoded, '\n')
	if err := writePrivateFile(
		filepath.Join(staging, cacheDescriptorName),
		encoded,
	); err != nil {
		return domain.InstallationMaterialCache{}, err
	}
	if err := syncDirectory(staging); err != nil {
		return domain.InstallationMaterialCache{}, err
	}
	if err := os.Rename(staging, cache.Directory); err != nil {
		return domain.InstallationMaterialCache{}, err
	}
	keepStaging = true
	if err := syncDirectory(root); err != nil {
		return domain.InstallationMaterialCache{}, err
	}
	return cache, nil
}

func loadOrphan(
	manifest domain.InstallationManifest,
	artifacts []domain.InstallationArtifact,
) (
	domain.InstallationMaterialCache,
	[]installfiles.File,
	error,
) {
	directory := cacheDirectory(manifest)
	descriptor, err := readDescriptor(directory)
	if err != nil {
		return domain.InstallationMaterialCache{}, nil, err
	}
	files, err := load(manifest, descriptor, artifacts)
	if err != nil {
		return domain.InstallationMaterialCache{}, nil, err
	}
	return descriptor, files, nil
}

func load(
	manifest domain.InstallationManifest,
	cache domain.InstallationMaterialCache,
	artifacts []domain.InstallationArtifact,
) ([]installfiles.File, error) {
	if manifest.ManagedSpecification == nil {
		return nil, errors.New("managed specification is required")
	}
	if cache.State != domain.MaterialCacheReady {
		return nil, fmt.Errorf("material cache is %s", cache.State)
	}
	if err := cache.Validate(
		manifest.InstallationID,
		manifest.ManagedSpecification.Paths.StateDirectory,
	); err != nil {
		return nil, err
	}
	if err := cache.ValidateArtifacts(artifacts); err != nil {
		return nil, err
	}
	info, err := os.Lstat(cache.Directory)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() ||
		info.Mode().Perm() != 0o700 ||
		!ownedByCurrentUser(info) {
		return nil, errors.New("material cache directory is unsafe")
	}
	expectedNames := map[string]bool{cacheDescriptorName: true}
	for _, entry := range cache.Entries {
		expectedNames[filepath.Base(entry.CachePath)] = true
	}
	entries, err := os.ReadDir(cache.Directory)
	if err != nil {
		return nil, err
	}
	if len(entries) != len(expectedNames) {
		return nil, errors.New("material cache contains unexpected entries")
	}
	for _, entry := range entries {
		if !expectedNames[entry.Name()] {
			return nil, errors.New("material cache contains unexpected entries")
		}
	}
	descriptor, err := readDescriptor(cache.Directory)
	if err != nil {
		return nil, err
	}
	if !sameCache(descriptor, cache) {
		return nil, errors.New("material cache descriptor changed")
	}
	files := make([]installfiles.File, 0, len(cache.Entries))
	for _, entry := range cache.Entries {
		content, err := readPrivateFile(entry.CachePath)
		if err != nil {
			clearSensitive(files)
			return nil, err
		}
		if sha256Hex(content) != entry.Fingerprint {
			clear(content)
			clearSensitive(files)
			return nil, fmt.Errorf(
				"cached material %s fingerprint changed",
				entry.ArtifactID,
			)
		}
		files = append(files, installfiles.File{
			ID:        entry.ArtifactID,
			Target:    entry.Target,
			Mode:      fs.FileMode(entry.Mode),
			Content:   content,
			Sensitive: entry.Sensitive,
		})
	}
	return files, nil
}

func remove(
	manifest domain.InstallationManifest,
	cache domain.InstallationMaterialCache,
) error {
	if err := cache.Validate(
		manifest.InstallationID,
		manifest.ManagedSpecification.Paths.StateDirectory,
	); err != nil {
		return err
	}
	info, err := os.Lstat(cache.Directory)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() ||
		info.Mode().Perm() != 0o700 ||
		!ownedByCurrentUser(info) {
		return errors.New("material cache directory is unsafe")
	}
	allowed := map[string]domain.InstallationMaterialCacheEntry{}
	for _, entry := range cache.Entries {
		allowed[filepath.Base(entry.CachePath)] = entry
	}
	entries, err := os.ReadDir(cache.Directory)
	if err != nil {
		return err
	}
	for _, directoryEntry := range entries {
		if directoryEntry.Name() == cacheDescriptorName {
			descriptor, err := readDescriptor(cache.Directory)
			if err != nil {
				return err
			}
			expected := cache
			expected.State = domain.MaterialCacheReady
			if !sameCache(descriptor, expected) {
				return errors.New("material cache descriptor changed")
			}
			continue
		}
		entry, exists := allowed[directoryEntry.Name()]
		if !exists {
			return errors.New("material cache contains unexpected entries")
		}
		content, err := readPrivateFile(entry.CachePath)
		if err != nil {
			return err
		}
		actual := sha256Hex(content)
		clear(content)
		if actual != entry.Fingerprint {
			return fmt.Errorf(
				"cached material %s fingerprint changed",
				entry.ArtifactID,
			)
		}
	}
	for _, entry := range cache.Entries {
		if err := removeRegularOrMissing(entry.CachePath); err != nil {
			return err
		}
	}
	if err := removeRegularOrMissing(
		filepath.Join(cache.Directory, cacheDescriptorName),
	); err != nil {
		return err
	}
	remaining, err := os.ReadDir(cache.Directory)
	if err != nil {
		return err
	}
	if len(remaining) != 0 {
		return errors.New("material cache directory is not empty")
	}
	if err := os.Remove(cache.Directory); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(cache.Directory))
}

func cacheMetadata(
	manifest domain.InstallationManifest,
	artifacts []domain.InstallationArtifact,
	files []installfiles.File,
) (domain.InstallationMaterialCache, error) {
	directory := cacheDirectory(manifest)
	if err := validateMaterializedFiles(artifacts, files); err != nil {
		return domain.InstallationMaterialCache{}, err
	}
	fileByID := map[string]installfiles.File{}
	for _, file := range files {
		fileByID[file.ID] = file
	}
	cache := domain.InstallationMaterialCache{
		SchemaVersion: domain.InstallationMaterialCacheSchemaVersion,
		State:         domain.MaterialCacheReady,
		Directory:     directory,
		Entries: make(
			[]domain.InstallationMaterialCacheEntry,
			0,
			len(artifacts),
		),
	}
	position := 0
	for _, artifact := range artifacts {
		file := fileByID[artifact.ID]
		cache.Entries = append(
			cache.Entries,
			domain.InstallationMaterialCacheEntry{
				ArtifactID: artifact.ID,
				Target:     artifact.Target,
				CachePath: filepath.Join(
					directory,
					fmt.Sprintf("%03d-%s.material", position, artifact.ID),
				),
				Mode:        uint32(file.Mode.Perm()),
				Fingerprint: sha256Hex(file.Content),
				Sensitive:   file.Sensitive,
			},
		)
		position++
	}
	fingerprint, err := domain.MaterialCacheInventoryFingerprint(cache.Entries)
	if err != nil {
		return domain.InstallationMaterialCache{}, err
	}
	cache.InventoryFingerprint = fingerprint
	if err := cache.Validate(
		manifest.InstallationID,
		manifest.ManagedSpecification.Paths.StateDirectory,
	); err != nil {
		return domain.InstallationMaterialCache{}, err
	}
	return cache, nil
}

func validateMaterializedFiles(
	artifacts []domain.InstallationArtifact,
	files []installfiles.File,
) error {
	if len(files) != len(artifacts) {
		return fmt.Errorf(
			"materialized files cover %d of %d selected file artifacts",
			len(files),
			len(artifacts),
		)
	}
	fileByID := map[string]installfiles.File{}
	for _, file := range files {
		if fileByID[file.ID].ID != "" {
			return fmt.Errorf("duplicate materialized file ID %q", file.ID)
		}
		fileByID[file.ID] = file
	}
	for _, artifact := range artifacts {
		file, exists := fileByID[artifact.ID]
		if !exists ||
			file.Target != artifact.Target ||
			uint32(file.Mode.Perm()) != artifact.Mode ||
			file.Sensitive != artifact.Sensitive ||
			file.Content == nil ||
			len(file.Content) > maxCacheFileSize {
			return fmt.Errorf(
				"materialized file %s does not match selected artifact",
				artifact.ID,
			)
		}
		if artifact.Fingerprint != "" &&
			sha256Hex(file.Content) != artifact.Fingerprint {
			return fmt.Errorf(
				"materialized file %s content changed",
				artifact.ID,
			)
		}
	}
	return nil
}

func fileArtifacts(
	artifacts []domain.InstallationArtifact,
) []domain.InstallationArtifact {
	files := make([]domain.InstallationArtifact, 0, len(artifacts))
	for _, artifact := range artifacts {
		if artifact.Kind != domain.ArtifactContainerSpec {
			files = append(files, artifact)
		}
	}
	return files
}

func cacheDirectory(manifest domain.InstallationManifest) string {
	return filepath.Join(
		manifest.ManagedSpecification.Paths.StateDirectory,
		".material-cache",
		manifest.InstallationID,
	)
}

func ensureCacheRoot(stateDirectory string, root string) error {
	state, err := os.Lstat(stateDirectory)
	if err != nil {
		return err
	}
	if state.Mode()&os.ModeSymlink != 0 || !state.IsDir() ||
		!ownedByCurrentUser(state) {
		return errors.New("state directory is not a real directory")
	}
	info, err := os.Lstat(root)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.Mkdir(root, 0o700); err != nil {
			return err
		}
		return syncDirectory(stateDirectory)
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() ||
		info.Mode().Perm() != 0o700 ||
		!ownedByCurrentUser(info) {
		return errors.New("material cache root is unsafe")
	}
	return nil
}

func removeStaging(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() ||
		info.Mode().Perm() != 0o700 ||
		!ownedByCurrentUser(info) {
		return errors.New("material cache staging path is unsafe")
	}
	return os.RemoveAll(path)
}

func withCacheLock(
	manifest domain.InstallationManifest,
	operation func() error,
) error {
	if manifest.ManagedSpecification == nil {
		return errors.New("managed specification is required")
	}
	root := filepath.Join(
		manifest.ManagedSpecification.Paths.StateDirectory,
		".material-cache",
	)
	if err := ensureCacheRoot(
		manifest.ManagedSpecification.Paths.StateDirectory,
		root,
	); err != nil {
		return err
	}
	lockPath := filepath.Join(root, manifest.InstallationID+".lock")
	lock, err := os.OpenFile(
		lockPath,
		os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW,
		0o600,
	)
	if err != nil {
		return err
	}
	defer lock.Close()
	info, err := lock.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 ||
		!ownedByCurrentUser(info) {
		return errors.New("material cache lock is unsafe")
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	return operation()
}

func writePrivateFile(path string, content []byte) error {
	file, err := os.OpenFile(
		path,
		os.O_CREATE|os.O_EXCL|os.O_WRONLY,
		0o600,
	)
	if err != nil {
		return err
	}
	if _, err := io.Copy(file, bytes.NewReader(content)); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}

func readPrivateFile(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() ||
		info.Mode().Perm() != 0o600 ||
		info.Size() > maxCacheFileSize ||
		!ownedByCurrentUser(info) {
		return nil, errors.New("cached material file is unsafe")
	}
	return os.ReadFile(path)
}

func readDescriptor(
	directory string,
) (domain.InstallationMaterialCache, error) {
	path := filepath.Join(directory, cacheDescriptorName)
	content, err := readPrivateFile(path)
	if err != nil {
		return domain.InstallationMaterialCache{}, err
	}
	defer clear(content)
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	var cache domain.InstallationMaterialCache
	if err := decoder.Decode(&cache); err != nil {
		return domain.InstallationMaterialCache{}, err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return domain.InstallationMaterialCache{}, errors.New(
				"material cache descriptor contains multiple JSON values",
			)
		}
		return domain.InstallationMaterialCache{}, err
	}
	return cache, nil
}

func removeRegularOrMissing(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return errors.New("material cache entry changed type")
	}
	if !ownedByCurrentUser(info) {
		return errors.New("material cache entry ownership changed")
	}
	return os.Remove(path)
}

func ownedByCurrentUser(info fs.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uint32(os.Geteuid())
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func clearSensitive(files []installfiles.File) {
	for index := range files {
		if files[index].Sensitive {
			clear(files[index].Content)
		}
	}
}

func sha256Hex(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

func sameCache(
	left domain.InstallationMaterialCache,
	right domain.InstallationMaterialCache,
) bool {
	leftEncoded, leftErr := json.Marshal(left)
	rightEncoded, rightErr := json.Marshal(right)
	return leftErr == nil &&
		rightErr == nil &&
		bytes.Equal(leftEncoded, rightEncoded)
}

func copyCache(
	cache *domain.InstallationMaterialCache,
) *domain.InstallationMaterialCache {
	if cache == nil {
		return nil
	}
	copied := *cache
	copied.Entries = append(
		[]domain.InstallationMaterialCacheEntry(nil),
		cache.Entries...,
	)
	return &copied
}

func cloneManifest(
	manifest domain.InstallationManifest,
) domain.InstallationManifest {
	cloned := manifest
	cloned.MaterialCache = copyCache(manifest.MaterialCache)
	return cloned
}

func ClearFiles(files []installfiles.File) {
	clearSensitive(files)
}
