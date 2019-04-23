package environment_test

import (
	"context"
	"os"
	"runtime"
	"testing"

	remoteexecution "github.com/bazelbuild/remote-apis/build/bazel/remote/execution/v2"
	"github.com/buildbarn/bb-remote-execution/internal/mock"
	"github.com/buildbarn/bb-remote-execution/pkg/environment"
	"github.com/buildbarn/bb-remote-execution/pkg/proto/runner"
	"github.com/buildbarn/bb-storage/pkg/util"
	"github.com/golang/mock/gomock"
	"github.com/stretchr/testify/require"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestActionDigestSubdirectoryManagerAcquireFailure(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	// Failure to create environment should simply be forwarded.
	baseManager := mock.NewMockManager(ctrl)
	baseManager.EXPECT().Acquire(
		util.MustNewDigest(
			"debian8",
			&remoteexecution.Digest{
				Hash:      "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
				SizeBytes: 0,
			}),
		map[string]string{
			"container-image": "ubuntu:latest",
		}).Return(nil, status.Error(codes.Internal, "No space left on device"))

	manager := environment.NewActionDigestSubdirectoryManager(baseManager, util.DigestKeyWithoutInstance)
	_, err := manager.Acquire(
		util.MustNewDigest(
			"debian8",
			&remoteexecution.Digest{
				Hash:      "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
				SizeBytes: 0,
			}),
		map[string]string{
			"container-image": "ubuntu:latest",
		})
	require.Equal(t, status.Error(codes.Internal, "No space left on device"), err)
}

func TestActionDigestSubdirectoryManagerMkdirFailure(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	// Failure to create a build subdirectory is always an internal error.
	baseManager := mock.NewMockManager(ctrl)
	baseEnvironment := mock.NewMockManagedEnvironment(ctrl)
	baseManager.EXPECT().Acquire(
		util.MustNewDigest(
			"debian8",
			&remoteexecution.Digest{
				Hash:      "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
				SizeBytes: 0,
			}),
		map[string]string{
			"container-image": "ubuntu:latest",
		}).Return(baseEnvironment, nil)
	rootDirectory := mock.NewMockDirectory(ctrl)
	baseEnvironment.EXPECT().GetBuildDirectory().Return(rootDirectory).AnyTimes()
	rootDirectory.EXPECT().Mkdir("e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855-0", os.FileMode(0777)).Return(
		status.Error(codes.AlreadyExists, "Directory already exists"))
	baseEnvironment.EXPECT().Release()

	manager := environment.NewActionDigestSubdirectoryManager(baseManager, util.DigestKeyWithoutInstance)
	_, err := manager.Acquire(
		util.MustNewDigest(
			"debian8",
			&remoteexecution.Digest{
				Hash:      "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
				SizeBytes: 0,
			}),
		map[string]string{
			"container-image": "ubuntu:latest",
		})
	require.Equal(t, status.Error(codes.Internal, "Failed to create build subdirectory \"e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855-0\": Directory already exists"), err)
}

func TestActionDigestSubdirectoryManagerEnterFailure(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	// Failure to enter a build subdirectory is always an internal error.
	baseManager := mock.NewMockManager(ctrl)
	baseEnvironment := mock.NewMockManagedEnvironment(ctrl)
	baseManager.EXPECT().Acquire(
		util.MustNewDigest(
			"debian8",
			&remoteexecution.Digest{
				Hash:      "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
				SizeBytes: 0,
			}),
		map[string]string{
			"container-image": "ubuntu:latest",
		}).Return(baseEnvironment, nil)
	rootDirectory := mock.NewMockDirectory(ctrl)
	baseEnvironment.EXPECT().GetBuildDirectory().Return(rootDirectory).AnyTimes()
	rootDirectory.EXPECT().Mkdir("e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855-0", os.FileMode(0777)).Return(nil)
	rootDirectory.EXPECT().Enter("e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855-0").Return(nil, status.Error(codes.ResourceExhausted, "Out of file descriptors"))
	rootDirectory.EXPECT().Remove("e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855-0").Return(nil)
	baseEnvironment.EXPECT().Release()

	manager := environment.NewActionDigestSubdirectoryManager(baseManager, util.DigestKeyWithoutInstance)
	_, err := manager.Acquire(
		util.MustNewDigest(
			"debian8",
			&remoteexecution.Digest{
				Hash:      "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
				SizeBytes: 0,
			}),
		map[string]string{
			"container-image": "ubuntu:latest",
		})
	require.Equal(t, status.Error(codes.Internal, "Failed to enter build subdirectory \"e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855-0\": Out of file descriptors"), err)
}

func TestActionDigestSubdirectoryManagerSuccessWithSameDigestTwice(t *testing.T) {
	ctrl, ctx := gomock.WithContext(context.Background(), t)
	defer ctrl.Finish()

	// Successful build in a subdirectory.
	baseManager := mock.NewMockManager(ctrl)
	baseEnvironment := mock.NewMockManagedEnvironment(ctrl)
	baseManager.EXPECT().Acquire(
		util.MustNewDigest(
			"debian8",
			&remoteexecution.Digest{
				Hash:      "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
				SizeBytes: 0,
			}),
		map[string]string{
			"container-image": "ubuntu:latest",
		}).Return(baseEnvironment, nil).Times(2)
	rootDirectory := mock.NewMockDirectory(ctrl)
	baseEnvironment.EXPECT().GetBuildDirectory().Return(rootDirectory).AnyTimes()
	rootDirectory.EXPECT().Mkdir("e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855-0", os.FileMode(0777)).Return(nil).Times(2)
	subDirectory := mock.NewMockDirectory(ctrl)
	rootDirectory.EXPECT().Enter("e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855-0").Return(subDirectory, nil).Times(2)
	baseEnvironment.EXPECT().Run(ctx, &runner.RunRequest{
		Arguments: []string{"ls", "-l"},
		EnvironmentVariables: map[string]string{
			"PATH": "/bin",
		},
		WorkingDirectory: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855-0/some/sub/directory",
		StdoutPath:       "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855-0/.stdout.txt",
		StderrPath:       "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855-0/.stderr.txt",
	}).Return(&runner.RunResponse{
		ExitCode: 123,
	}, nil)
	subDirectory.EXPECT().Close().Times(2)
	rootDirectory.EXPECT().RemoveAll("e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855-0").Return(nil).Times(2)
	baseEnvironment.EXPECT().Release().Times(2)

	manager := environment.NewActionDigestSubdirectoryManager(baseManager, util.DigestKeyWithoutInstance)
	environment1, err := manager.Acquire(
		util.MustNewDigest(
			"debian8",
			&remoteexecution.Digest{
				Hash:      "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
				SizeBytes: 0,
			}),
		map[string]string{
			"container-image": "ubuntu:latest",
		})
	require.NoError(t, err)
	require.Equal(t, subDirectory, environment1.GetBuildDirectory())
	response, err := environment1.Run(ctx, &runner.RunRequest{
		Arguments: []string{"ls", "-l"},
		EnvironmentVariables: map[string]string{
			"PATH": "/bin",
		},
		WorkingDirectory: "some/sub/directory",
		StdoutPath:       ".stdout.txt",
		StderrPath:       ".stderr.txt",
	})
	require.NoError(t, err)
	require.Equal(t, &runner.RunResponse{
		ExitCode: 123,
	}, response)

	// Before releasing environment1, try to acquire the same directory
	var environment2 environment.ManagedEnvironment
	go func() {
		environment2, err = manager.Acquire(
			util.MustNewDigest(
				"debian8",
				&remoteexecution.Digest{
					Hash:      "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
					SizeBytes: 0,
				}),
			map[string]string{
				"container-image": "ubuntu:latest",
			})
	}()
	runtime.Gosched()
	// environment2 should be waiting for enviroment1 to be released
	require.Nil(t, environment2)
	environment1.Release()
	for i := 0; i < 100; i++ { // Don't know why it doesn't always work the first time
		runtime.Gosched()
		// environment2 should now be available
		if environment2 != nil {
			break
		}
	}
	require.NotNil(t, environment2)
	environment2.Release()
}
