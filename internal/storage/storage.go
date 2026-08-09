package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"itchgrep/internal/logging"
	"itchgrep/pkg/models"
	"os"
	"path/filepath"
	"sync"
	"time"

	"cloud.google.com/go/storage"
	"github.com/mholt/archiver/v4"
	"google.golang.org/api/option"
)

const (
	BucketName         = "itchgrep-data"
	DataFileName       = "assets.json"
	TagsFileName       = "tags.json"
	CheckpointFileName = "checkpoint.json"
	IndexDirName       = "index.bleve"
	IndexArchiveName   = "index.bleve.gz.tar"
)

var ArchiveFormat = archiver.CompressedArchive{
	Compression: archiver.Gz{},
	Archival:    archiver.Tar{},
}

// sharedClient, sharedClientErr, and clientOnce implement a package-level,
// lazily-initialised GCS client. createClient used to be called on every
// exported function (i.e. on every HTTP request, via cache.IsCacheExpired),
// creating a fresh client and TLS connection each time and racing on
// os.Setenv. Now the client (and the env var write) is created exactly once
// and the error from that one attempt is cached and returned to every
// caller.
var (
	clientOnce      sync.Once
	sharedClient    *storage.Client
	sharedClientErr error
)

// The caller's ctx is deliberately not used to construct the shared client:
// storage.NewClient ties token refresh to the ctx it is given, so binding it to
// whichever request happened to initialise the client first would break every
// later caller once that request finished.
func createClient(context.Context) (*storage.Client, error) {
	clientOnce.Do(func() {
		ctx := context.Background()

		local := os.Getenv("RUN_LOCAL") == "true"
		logging.Info("RUN_LOCAL: %v", local)
		test := os.Getenv("RUN_TEST") == "true"
		logging.Info("RUN_TEST: %v", test)

		if local {
			if !test {
				os.Setenv("STORAGE_EMULATOR_HOST", "http://fake-gcs-server:4443") // name of the docker container
				logging.Info("Using address: http://fake-gcs-server:4443")
				sharedClient, sharedClientErr = storage.NewClient(
					ctx,
					option.WithEndpoint("http://fake-gcs-server:4443/storage/v1/"),
					storage.WithJSONReads())
			} else { // if we are running tests, this is not running in a container
				os.Setenv("STORAGE_EMULATOR_HOST", "http://localhost:4443")
				logging.Info("Using address: http://localhost:4443")
				sharedClient, sharedClientErr = storage.NewClient(
					ctx,
					option.WithEndpoint("http://localhost:4443/storage/v1/"),
					storage.WithJSONReads())
			}
		} else {
			logging.Info("Using production GCS client.")
			sharedClient, sharedClientErr = storage.NewClient(ctx)
		}
	})
	return sharedClient, sharedClientErr
}

// PutAssets writes the provided assets to a Google Cloud Storage bucket as a JSON file.
func PutAssets(assets []models.Asset) error {
	ctx := context.Background()

	client, err := createClient(ctx)
	if err != nil {
		return fmt.Errorf("storage.NewClient: %v", err)
	}

	bkt := client.Bucket(BucketName)

	// Convert assets slice to JSON
	assetsJSON, err := json.Marshal(assets)
	if err != nil {
		return fmt.Errorf("json.Marshal: %v", err)
	}

	// Create a new writer to write the JSON data
	obj := bkt.Object(DataFileName)
	w := obj.NewWriter(ctx)
	if _, err := w.Write(assetsJSON); err != nil {
		return fmt.Errorf("Writer.Write: %v", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("Writer.Close: %v", err)
	}

	return nil
}

// GetAssets fetches the assets JSON file from the Google Cloud Storage bucket and unmarshals it into a slice of Assets.
func GetAssets() ([]models.Asset, error) {
	ctx := context.Background()
	client, err := createClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("storage.NewClient: %v", err)
	}

	bkt := client.Bucket(BucketName)
	obj := bkt.Object(DataFileName)
	r, err := obj.NewReader(ctx)
	if err != nil {
		return nil, fmt.Errorf("Object.NewReader: %v", err)
	}
	defer r.Close()

	data, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("io.ReadAll: %v", err)
	}

	var assets []models.Asset
	if err := json.Unmarshal(data, &assets); err != nil {
		return nil, fmt.Errorf("json.Unmarshal: %v", err)
	}

	return assets, nil
}

// PutTags caches the discovered tag universe. Discovery costs several hundred
// requests to itch.io and the tag set barely moves between scrapes, so it is
// persisted rather than rebuilt every run.
func PutTags(tags []models.Tag) error {
	ctx := context.Background()

	client, err := createClient(ctx)
	if err != nil {
		return fmt.Errorf("storage.NewClient: %v", err)
	}

	tagsJSON, err := json.Marshal(tags)
	if err != nil {
		return fmt.Errorf("json.Marshal: %v", err)
	}

	w := client.Bucket(BucketName).Object(TagsFileName).NewWriter(ctx)
	if _, err := w.Write(tagsJSON); err != nil {
		return fmt.Errorf("Writer.Write: %v", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("Writer.Close: %v", err)
	}
	return nil
}

// Checkpoint is a partially-complete crawl, written periodically so that a run
// killed part way through is resumable instead of worthless. A full crawl is
// ~75 minutes and lives entirely in memory until it publishes.
type Checkpoint struct {
	Assets      []models.Asset
	DoneSlices  []string // Slice.Label() values already finished
	MaxRootRank int64    // so slice-only assets keep ranking behind root-ranked ones
	TotalAssets int64    // catalogue size when the checkpoint was taken
}

// PutCheckpoint overwrites the crawl checkpoint.
func PutCheckpoint(cp Checkpoint) error {
	ctx := context.Background()

	client, err := createClient(ctx)
	if err != nil {
		return fmt.Errorf("storage.NewClient: %v", err)
	}

	cpJSON, err := json.Marshal(cp)
	if err != nil {
		return fmt.Errorf("json.Marshal: %v", err)
	}

	w := client.Bucket(BucketName).Object(CheckpointFileName).NewWriter(ctx)
	if _, err := w.Write(cpJSON); err != nil {
		return fmt.Errorf("Writer.Write: %v", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("Writer.Close: %v", err)
	}
	return nil
}

// GetCheckpoint returns the stored checkpoint and when it was written. A
// missing checkpoint is the normal case - it just means the last crawl
// finished cleanly - so callers treat the error as "start fresh".
func GetCheckpoint() (Checkpoint, time.Time, error) {
	ctx := context.Background()
	client, err := createClient(ctx)
	if err != nil {
		return Checkpoint{}, time.Time{}, fmt.Errorf("storage.NewClient: %v", err)
	}

	obj := client.Bucket(BucketName).Object(CheckpointFileName)
	attrs, err := obj.Attrs(ctx)
	if err != nil {
		return Checkpoint{}, time.Time{}, fmt.Errorf("Object.Attrs: %v", err)
	}

	r, err := obj.NewReader(ctx)
	if err != nil {
		return Checkpoint{}, time.Time{}, fmt.Errorf("Object.NewReader: %v", err)
	}
	defer r.Close()

	data, err := io.ReadAll(r)
	if err != nil {
		return Checkpoint{}, time.Time{}, fmt.Errorf("io.ReadAll: %v", err)
	}

	var cp Checkpoint
	if err := json.Unmarshal(data, &cp); err != nil {
		return Checkpoint{}, time.Time{}, fmt.Errorf("json.Unmarshal: %v", err)
	}
	return cp, attrs.Updated, nil
}

// DeleteCheckpoint removes the checkpoint after a crawl publishes. A stale
// checkpoint left behind would make the next run resume a crawl that already
// completed, so failing to delete it is worth logging but not fatal.
func DeleteCheckpoint() error {
	ctx := context.Background()
	client, err := createClient(ctx)
	if err != nil {
		return fmt.Errorf("storage.NewClient: %v", err)
	}
	return client.Bucket(BucketName).Object(CheckpointFileName).Delete(ctx)
}

// GetTags returns the cached tag universe and when it was written. A missing
// cache is not an error condition the caller should fail on - it just means
// discovery has to run - so callers check the error and rediscover.
func GetTags() ([]models.Tag, time.Time, error) {
	ctx := context.Background()
	client, err := createClient(ctx)
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("storage.NewClient: %v", err)
	}

	obj := client.Bucket(BucketName).Object(TagsFileName)
	attrs, err := obj.Attrs(ctx)
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("Object.Attrs: %v", err)
	}

	r, err := obj.NewReader(ctx)
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("Object.NewReader: %v", err)
	}
	defer r.Close()

	data, err := io.ReadAll(r)
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("io.ReadAll: %v", err)
	}

	var tags []models.Tag
	if err := json.Unmarshal(data, &tags); err != nil {
		return nil, time.Time{}, fmt.Errorf("json.Unmarshal: %v", err)
	}
	return tags, attrs.Updated, nil
}

func GetAssetsUpdateTime() (time.Time, error) {
	ctx := context.Background()
	client, err := createClient(ctx)
	if err != nil {
		return time.Time{}, fmt.Errorf("storage.NewClient: %v", err)
	}

	bkt := client.Bucket(BucketName)
	objAttrs, err := bkt.Object(DataFileName).Attrs(ctx)
	if err != nil {
		return time.Time{}, fmt.Errorf("Object.Attrs: %v", err)
	}

	return objAttrs.Updated, nil
}

// PutFS writes the provided directory or file to a Google Cloud Storage
// bucket as a compressed archive.
func PutFS(dirPath, nameInStorage string) error {
	ctx := context.Background()

	client, err := createClient(ctx)
	if err != nil {
		return fmt.Errorf("storage.NewClient: %v", err)
	}

	bkt := client.Bucket(BucketName)

	// COMPRESSING INDEX DIRECTORY
	fileMapping, _ := archiver.FilesFromDisk(nil, map[string]string{
		dirPath: filepath.Base(dirPath),
	})

	// create temp dir for the archive file to be created in
	archiveFileHandle, err := os.Create(nameInStorage)
	if err != nil {
		return fmt.Errorf("os.Create: %v", err)
	}
	defer os.RemoveAll(nameInStorage)

	err = ArchiveFormat.Archive(context.Background(), archiveFileHandle, fileMapping)
	if err != nil {
		return fmt.Errorf("format.Archive: %v", err)
	}
	archiveFileHandle.Close()

	// Reopen the archive and stream it to GCS rather than reading it fully
	// into memory: on Cloud Run the filesystem is an in-memory tmpfs, so an
	// os.ReadFile here would count the archive against the memory limit a
	// second time on top of the tmpfs copy.
	archiveFile, err := os.Open(nameInStorage)
	if err != nil {
		return fmt.Errorf("os.Open: %v", err)
	}
	defer archiveFile.Close()

	// Create a new writer to write the index archive file
	obj := bkt.Object(nameInStorage)
	w := obj.NewWriter(ctx)
	written, err := io.Copy(w, archiveFile)
	if err != nil {
		w.Close()
		return fmt.Errorf("io.Copy: %v", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("Writer.Close: %v", err)
	}

	logging.Debug("Archive size: %d", written)

	return nil
}

// GetFS fetches the directory from the Google Cloud Storage bucket and
// extracts it to the local filesystem. It returns the path of the file or
// directory in the archive.
// Returns an empty string if the archive is empty.
func GetFS(nameInStorage, targetPath string) (string, error) {
	ctx := context.Background()
	client, err := createClient(ctx)
	if err != nil {
		return "", fmt.Errorf("storage.NewClient: %v", err)
	}

	bkt := client.Bucket(BucketName)
	obj := bkt.Object(nameInStorage)
	r, err := obj.NewReader(ctx)
	if err != nil {
		return "", fmt.Errorf("Object.NewReader: %v", err)
	}
	defer r.Close()

	// we check what the first file/directory is in the archive, and return
	// that path, since there can only ever be one root directory or file.
	rootDir := ""
	rootFile := ""
	// nil as the third argument to Extract means that all files will be extracted
	err = ArchiveFormat.Extract(context.Background(), r, nil, func(ctx context.Context, file archiver.File) error {
		rel := filepath.Clean(file.NameInArchive)
		abs := filepath.Join(targetPath, rel)

		mode := file.Mode()

		switch {
		case mode.IsRegular():
			f, err := os.Create(abs)
			if err != nil {
				return err
			}
			defer f.Close()
			fReader, err := file.Open()
			if err != nil {
				return err
			}
			_, err = io.Copy(f, fReader)
			if rootFile == "" && rootDir == "" {
				rootFile = abs
			}
			return err
		case mode.IsDir():
			if rootDir == "" && rootFile == "" {
				rootDir = abs
			}
			return os.MkdirAll(abs, 0o755)
		default:
			return fmt.Errorf("archive contained entry %s of unsupported file type %v", file.Name(), mode)
		}
	})
	if err != nil {
		return "", fmt.Errorf("Extract: %v", err)
	}

	// if the first extraction is a directory, return that, otherwise return
	// the first file
	if rootDir != "" {
		return rootDir, nil
	} else {
		return rootFile, nil
	}
}
