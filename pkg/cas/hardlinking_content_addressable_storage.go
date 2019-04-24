package cas

import (
	"container/list"
	"context"
	"fmt"
	"sync"

	"github.com/buildbarn/bb-storage/pkg/cas"
	"github.com/buildbarn/bb-storage/pkg/filesystem"
	"github.com/buildbarn/bb-storage/pkg/util"
	"github.com/prometheus/client_golang/prometheus"
)

var (
	hardlinkingContentAddressableStorageOperationsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "buildbarn",
			Subsystem: "cas",
			Name:      "hardlinking_content_addressable_storage_operations_total",
			Help:      "Total number of operations against the hardlinking content addressable storage.",
		},
		[]string{"result"})
	hardlinkingContentAddressableStorageOperationsTotalHit  = hardlinkingContentAddressableStorageOperationsTotal.WithLabelValues("Hit")
	hardlinkingContentAddressableStorageOperationsTotalMiss = hardlinkingContentAddressableStorageOperationsTotal.WithLabelValues("Miss")
)

func init() {
	prometheus.MustRegister(hardlinkingContentAddressableStorageOperationsTotal)
}

type fileEntry struct {
	key  string // File name in the cache folder
	size int64  // Size in bytes
}

type hardlinkingContentAddressableStorage struct {
	cas.ContentAddressableStorageReader

	lock sync.RWMutex

	digestKeyFormat util.DigestKeyFormat
	cacheDirectory  filesystem.Directory
	maxFiles        int
	maxSize         int64

	filesPresentList      *list.List // FIFO of fileEntry with push back and pop front
	filesPresentMap       map[string]*list.Element
	filesPresentTotalSize int64
}

// NewHardlinkingContentAddressableStorage is an adapter for
// ContentAddressableStorageReader that stores files in an internal directory. After
// successfully downloading files at the target location, they are hardlinked
// into the cache. Future calls for the same file will hardlink them from the
// cache to the target location. This reduces the amount of network traffic
// needed.
func NewHardlinkingContentAddressableStorage(
	base cas.ContentAddressableStorageReader, digestKeyFormat util.DigestKeyFormat, cacheDirectory filesystem.Directory,
	maxFiles int, maxSize int64) (cas.ContentAddressableStorageReader, error) {

	ret := &hardlinkingContentAddressableStorage{
		ContentAddressableStorageReader: base,

		digestKeyFormat: digestKeyFormat,
		cacheDirectory:  cacheDirectory,
		maxFiles:        maxFiles,
		maxSize:         maxSize,

		filesPresentList: list.New(),
		filesPresentMap:  map[string]*list.Element{},
	}
	err := ret.makeSubDirectories()
	return ret, err
}

const cacheSubDirectoryNameLength = 2
const cacheSubDirectoryNameFormat = "%02x"

func getSubDirectoryName(key string) string {
	return key[:cacheSubDirectoryNameLength]
}

func (cas *hardlinkingContentAddressableStorage) makeSubDirectories() error {
	for i := 0; i < (1 << (4 * cacheSubDirectoryNameLength)); i++ {
		subDirectoryName := fmt.Sprintf(cacheSubDirectoryNameFormat, i)
		if err := cas.cacheDirectory.Mkdir(subDirectoryName, 0777); err != nil {
			return err
		}
	}
	return nil
}

func (cas *hardlinkingContentAddressableStorage) makeSpace(size int64) error {
	for cas.filesPresentList.Len() > 0 && (cas.filesPresentList.Len() >= cas.maxFiles || cas.filesPresentTotalSize+size > cas.maxSize) {
		// Remove oldest file from disk.
		element := cas.filesPresentList.Front()
		file := element.Value.(*fileEntry)

		subDirectoryName := getSubDirectoryName(file.key)
		cacheSubDirectory, err := cas.cacheDirectory.Enter(subDirectoryName)
		if err != nil {
			return err
		}
		err = cacheSubDirectory.Remove(file.key)
		cacheSubDirectory.Close()
		if err != nil {
			return err
		}

		// Remove file from bookkeeping.
		cas.filesPresentTotalSize -= file.size
		delete(cas.filesPresentMap, file.key)
		cas.filesPresentList.Remove(element)
	}
	return nil
}

func (cas *hardlinkingContentAddressableStorage) GetFile(ctx context.Context, digest *util.Digest, directory filesystem.Directory, name string, isExecutable bool) error {
	key := digest.GetKey(cas.digestKeyFormat)
	if isExecutable {
		key += "+x"
	} else {
		key += "-x"
	}
	subDirectoryName := getSubDirectoryName(key)
	cacheSubDirectory, err := cas.cacheDirectory.Enter(subDirectoryName)
	if err != nil {
		return err
	}
	defer cacheSubDirectory.Close()

	// If the file is present in the cache, hardlink it to the destination.
	cas.lock.RLock()
	if element, ok := cas.filesPresentMap[key]; ok {
		cas.filesPresentList.MoveToBack(element) // Move it last in the FIFO
		err := cacheSubDirectory.Link(key, directory, name)
		cas.lock.RUnlock()
		hardlinkingContentAddressableStorageOperationsTotalHit.Inc()
		return err
	}
	cas.lock.RUnlock()
	hardlinkingContentAddressableStorageOperationsTotalMiss.Inc()

	// Download the file at the intended location.
	if err := cas.ContentAddressableStorageReader.GetFile(ctx, digest, directory, name, isExecutable); err != nil {
		return err
	}

	// Hardlink the file into the cache.
	cas.lock.Lock()
	defer cas.lock.Unlock()
	if _, ok := cas.filesPresentMap[key]; !ok {
		sizeBytes := digest.GetSizeBytes()
		if err := cas.makeSpace(sizeBytes); err != nil {
			return err
		}
		if err := directory.Link(name, cacheSubDirectory, key); err != nil {
			return err
		}
		element := cas.filesPresentList.PushBack(&fileEntry{key: key, size: sizeBytes})
		cas.filesPresentMap[key] = element
		cas.filesPresentTotalSize += sizeBytes
	}
	return nil
}
