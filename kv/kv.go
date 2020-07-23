package kv

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"

	"github.com/cdnjs/tools/sentry"
	"github.com/cdnjs/tools/util"
	cloudflare "github.com/cloudflare/cloudflare-go"
)

var (
	filesNamespaceID    = util.GetEnv("WORKERS_KV_FILES_NAMESPACE_ID")
	versionsNamespaceID = util.GetEnv("WORKERS_KV_VERSIONS_NAMESPACE_ID")
	packagesNamespaceID = util.GetEnv("WORKERS_KV_PACKAGES_NAMESPACE_ID")
	accountID           = util.GetEnv("WORKERS_KV_ACCOUNT_ID")
	apiToken            = util.GetEnv("WORKERS_KV_API_TOKEN")
	api                 = getAPI()
)

// Represents a KV write request, consisting of
// a string key, a []byte value, and file metadata.
type writeRequest struct {
	key   string
	value []byte
	meta  *FileMetadata
}

// FileMetadata represents metadata for a
// particular KV.
type FileMetadata struct {
	ETag         string `json:"etag"`
	LastModified string `json:"last_modified"`
}

// Gets a new *cloudflare.API.
func getAPI() *cloudflare.API {
	a, err := cloudflare.NewWithAPIToken(apiToken, cloudflare.UsingAccount(accountID))
	util.Check(err)
	return a
}

// Ensure a response is successful and the error is nil.
func checkSuccess(r cloudflare.Response, err error) error {
	if err != nil {
		return err
	}
	if !r.Success {
		return fmt.Errorf("kv fail: %v", r)
	}
	return nil
}

// Read reads an entry from Workers KV.
func Read(key, namespaceID string) ([]byte, error) {
	return api.ReadWorkersKV(context.Background(), namespaceID, key)
}

// Encodes a byte array to a base64 string.
func encodeToBase64(bytes []byte) string {
	return base64.StdEncoding.EncodeToString(bytes)
}

// Encodes key-value pairs to base64 and writes them to KV
// in multiple bulk requests.
func encodeAndWriteKVBulk(ctx context.Context, kvs []*writeRequest, namespaceID string) error {
	var bulkWrites []cloudflare.WorkersKVBulkWriteRequest
	var bulkWrite []*cloudflare.WorkersKVPair
	var totalSize, totalKeys int64

	for _, kv := range kvs {
		if unencodedSize := int64(len(kv.value)); unencodedSize > util.MaxFileSize {
			util.Debugf(ctx, "ignoring oversized file: %s (%d)\n", kv.key, unencodedSize)
			continue
		}
		// Note that after encoding in base64 the size may get larger, but after decoding
		// it will be reduced, so it is okay if the size is larger than util.MaxFileSize after encoding base64.
		// However, we still need to check for the KV bulk request limit of 100MiB.
		encodedValue := encodeToBase64(kv.value)
		size := int64(len(encodedValue))
		writePair := &cloudflare.WorkersKVPair{
			Key:    kv.key,
			Value:  encodedValue,
			Base64: true,
		}
		if kv.meta != nil {
			// Marshal metadata into JSON bytes.
			bytes, err := json.Marshal(kv.meta)
			if err != nil {
				return err
			}
			metasize := int64(len(bytes))
			if metasize > util.MaxMetadataSize {
				util.Debugf(ctx, "ignoring oversized metadata: %s (%d)\n", kv.key, metasize)
				sentry.NotifyError(fmt.Errorf("oversized metadata: %s (%d) - %s\n", kv.key, metasize, bytes))
				continue
			}
			util.Debugf(ctx, "writing metadata: %s\n", bytes)
			writePair.Metadata = kv.meta
			size += metasize
		}
		if totalSize+size > util.MaxBulkWritePayload || totalKeys == util.MaxBulkKeys {
			// Create a new bulk since we are over a limit.
			bulkWrites = append(bulkWrites, bulkWrite)
			bulkWrite = []*cloudflare.WorkersKVPair{}
			totalSize = 0
			totalKeys = 0
		}
		bulkWrite = append(bulkWrite, writePair)
		totalSize += size
		totalKeys++
	}
	bulkWrites = append(bulkWrites, bulkWrite)

	for i, b := range bulkWrites {
		util.Debugf(ctx, "writing bulk %d/%d (keys=%d)...\n", i+1, len(bulkWrites), len(b))
		r, err := api.WriteWorkersKVBulk(context.Background(), namespaceID, b)
		if err = checkSuccess(r, err); err != nil {
			return err
		}
	}

	return nil
}

// InsertNewVersionToKV inserts a new version to KV.
// The `fullPathToVersion` string will be useful if the version is downloaded to
// a temporary directory, not necessarily always in `$BOT_BASE_PATH/cdnjs/ajax/libs/`.
//
// Note that this function will also compress the files, generating brotli/gzip entries
// to KV where necessary, as well as minifying js, compressing png/jpeg/css, etc.
//
// Note this function will NOT update package metadata. This will happen later to avoid
// KV race conditions updating the package's entry for latest version.
//
// For example:
// InsertNewVersionToKV("1000hz-bootstrap-validator", "0.10.0", "/tmp/1000hz-bootstrap-validator/0.10.0")
func InsertNewVersionToKV(ctx context.Context, pkg, version, fullPathToVersion string) error {
	fromVersionPaths, err := util.ListFilesInVersion(ctx, fullPathToVersion)
	if err != nil {
		return err
	}

	// write files to KV
	fromVersionPaths, err = updateKVFiles(ctx, pkg, version, fullPathToVersion, fromVersionPaths)
	if err != nil {
		return err
	}

	// write version metadata to KV
	return updateKVVersion(ctx, pkg, version, fromVersionPaths)
}