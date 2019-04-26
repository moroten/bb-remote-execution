package environment

import (
	"context"
	"math"
	"os"
	"path"
	"time"

	"github.com/buildbarn/bb-remote-execution/pkg/proto/runner"
	"github.com/buildbarn/bb-storage/pkg/cas"
	"github.com/buildbarn/bb-storage/pkg/filesystem"
	"github.com/buildbarn/bb-storage/pkg/util"
	"github.com/prometheus/client_golang/prometheus"
)

var (
	inputProviderDurationSeconds = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: "buildbarn",
			Subsystem: "environment",
			Name:      "input_provider_duration_seconds",
			Help:      "Amount of time spent per input providing step, in seconds.",
			Buckets:   prometheus.ExponentialBuckets(0.001, math.Pow(10.0, 1.0/3.0), 6*3+1),
		},
		[]string{"step"})
	inputProviderDurationSecondsGetInputRootDigest = inputProviderDurationSeconds.WithLabelValues("GetInputRootDigest")
	inputProviderDurationSecondsPrepareFilesystem  = inputProviderDurationSeconds.WithLabelValues("PrepareFilesystem")
)

func init() {
	prometheus.MustRegister(inputProviderDurationSeconds)
}

type inputProviderManager struct {
	base                      Manager
	contentAddressableStorage cas.ContentAddressableStorageReader
}

// NewInputProviderManager is a Manager that writes all needed input files
// to the build directory provided by the wrapped Manager, and removes the
// directory tree afterwards.
func NewInputProviderManager(base Manager, contentAddressableStorage cas.ContentAddressableStorageReader) Manager {
	return &inputProviderManager{
		base:                      base,
		contentAddressableStorage: contentAddressableStorage,
	}
}

func (em *inputProviderManager) Acquire(actionDigest *util.Digest, platformProperties map[string]string) (ManagedEnvironment, error) {
	environment, err := em.base.Acquire(actionDigest, platformProperties)
	if err != nil {
		return nil, err
	}
	return &inputProviderEnvironment{
		ManagedEnvironment:        environment,
		contentAddressableStorage: em.contentAddressableStorage,
		actionDigest:              actionDigest,
	}, nil
}

type inputProviderEnvironment struct {
	ManagedEnvironment
	contentAddressableStorage cas.ContentAddressableStorageReader
	actionDigest              *util.Digest
}

func (e *inputProviderEnvironment) Run(ctx context.Context, request *runner.RunRequest) (*runner.RunResponse, error) {
	timeStart := time.Now()

	action, err := e.contentAddressableStorage.GetAction(ctx, e.actionDigest)
	if err != nil {
		return nil, util.StatusWrap(err, "Failed to obtain action")
	}
	inputRootDigest, err := e.actionDigest.NewDerivedDigest(action.InputRootDigest)
	if err != nil {
		return nil, util.StatusWrap(err, "Failed to extract digest for input root")
	}

	timeAfterGetInputRootDigest := time.Now()
	inputProviderDurationSecondsGetInputRootDigest.Observe(
		timeAfterGetInputRootDigest.Sub(timeStart).Seconds())

	buildDirectory := e.ManagedEnvironment.GetBuildDirectory()
	if err := e.createInputDirectory(ctx, inputRootDigest, buildDirectory, []string{"."}); err != nil {
		return nil, err
	}

	timeAfterPrepareFilesytem := time.Now()
	inputProviderDurationSecondsPrepareFilesystem.Observe(
		timeAfterPrepareFilesytem.Sub(timeAfterGetInputRootDigest).Seconds())

	return e.ManagedEnvironment.Run(ctx, request)
}

func (e *inputProviderEnvironment) createInputDirectory(ctx context.Context, digest *util.Digest, inputDirectory filesystem.Directory, components []string) error {
	// Obtain directory.
	directory, err := e.contentAddressableStorage.GetDirectory(ctx, digest)
	if err != nil {
		return util.StatusWrapf(err, "Failed to obtain input directory %#v", path.Join(components...))
	}

	// Create children.
	for _, file := range directory.Files {
		childComponents := append(components, file.Name)
		childDigest, err := digest.NewDerivedDigest(file.Digest)
		if err != nil {
			return util.StatusWrapf(err, "Failed to extract digest for input file %#v", path.Join(childComponents...))
		}
		if err := e.contentAddressableStorage.GetFile(ctx, childDigest, inputDirectory, file.Name, file.IsExecutable); err != nil {
			return util.StatusWrapf(err, "Failed to obtain input file %#v", path.Join(childComponents...))
		}
	}
	for _, directory := range directory.Directories {
		childComponents := append(components, directory.Name)
		if err := inputDirectory.Mkdir(directory.Name, 0777); err != nil && !os.IsExist(err) {
			return util.StatusWrapf(err, "Failed to create input directory %#v", path.Join(childComponents...))
		}
		childDigest, err := digest.NewDerivedDigest(directory.Digest)
		if err != nil {
			return util.StatusWrapf(err, "Failed to extract digest for input directory %#v", path.Join(childComponents...))
		}
		childDirectory, err := inputDirectory.Enter(directory.Name)
		if err != nil {
			return util.StatusWrapf(err, "Failed to enter input directory %#v", path.Join(childComponents...))
		}
		err = e.createInputDirectory(ctx, childDigest, childDirectory, childComponents)
		childDirectory.Close()
		if err != nil {
			return err
		}
	}
	for _, symlink := range directory.Symlinks {
		childComponents := append(components, symlink.Name)
		if err := inputDirectory.Symlink(symlink.Target, symlink.Name); err != nil {
			return util.StatusWrapf(err, "Failed to create input symlink %#v", path.Join(childComponents...))
		}
	}
	return nil
}
