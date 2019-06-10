package environment_test

import (
	"context"
	"os"
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

func newTestHash(hash3Digits string) string {
	return "0000000000000000000000000000000000000000000000000000000000000" + hash3Digits
}

func newTestRemoteDigest(hash3Digits string, sizeBytes int64) *remoteexecution.Digest {
	return &remoteexecution.Digest{
		Hash:      newTestHash(hash3Digits),
		SizeBytes: sizeBytes,
	}
}

func newTestUtilDigest(hash3Digits string, sizeBytes int64) *util.Digest {
	return util.MustNewDigest(
		"ubuntu1804",
		newTestRemoteDigest(hash3Digits, sizeBytes))
}

func TestLocalBuildExecutorMissingInputDirectoryDigest(t *testing.T) {
	ctrl, ctx := gomock.WithContext(context.Background(), t)
	defer ctrl.Finish()

	contentAddressableStorage := mock.NewMockContentAddressableStorage(ctrl)
	contentAddressableStorage.EXPECT().GetAction(ctx, newTestUtilDigest("555", 7)).
		Return(&remoteexecution.Action{
			// CommandDigest not used
			InputRootDigest: newTestRemoteDigest("777", 42),
		}, nil)
	contentAddressableStorage.EXPECT().GetDirectory(ctx, newTestUtilDigest("777", 42)).
		Return(&remoteexecution.Directory{
			Directories: []*remoteexecution.DirectoryNode{
				{Name: "Hello", Digest: newTestRemoteDigest("888", 123)},
			},
		}, nil)
	contentAddressableStorage.EXPECT().GetDirectory(ctx, newTestUtilDigest("888", 123)).
		Return(&remoteexecution.Directory{
			Directories: []*remoteexecution.DirectoryNode{
				{Name: "World"}, // Missing the digest
			},
		}, nil)
	baseEnvironmentManager := mock.NewMockManager(ctrl)
	baseEnvironment := mock.NewMockManagedEnvironment(ctrl)
	baseEnvironmentManager.EXPECT().Acquire(newTestUtilDigest("555", 7), map[string]string{}).Return(baseEnvironment, nil)
	buildDirectory := mock.NewMockDirectory(ctrl)
	buildDirectory.EXPECT().Mkdir("Hello", os.FileMode(0777)).Return(nil)
	helloDirectory := mock.NewMockDirectory(ctrl)
	buildDirectory.EXPECT().Enter("Hello").Return(helloDirectory, nil)
	helloDirectory.EXPECT().Close()
	helloDirectory.EXPECT().Mkdir("World", os.FileMode(0777)).Return(nil)
	baseEnvironment.EXPECT().GetBuildDirectory().Return(buildDirectory)
	baseEnvironment.EXPECT().Release()

	inputProviderManager := environment.NewInputProviderManager(baseEnvironmentManager, contentAddressableStorage)
	environment, err := inputProviderManager.Acquire(newTestUtilDigest("555", 7), map[string]string{})
	require.NoError(t, err)
	response, err := environment.Run(ctx, &runner.RunRequest{
		// No members used anyway.
	})
	require.Equal(t, status.ErrorProto(status.New(codes.InvalidArgument, "Failed to extract digest for input directory \"Hello/World\": No digest provided").Proto()), err)
	require.Nil(t, response)
	environment.Release()
}

func TestLocalBuildExecutorInputRootNotInStorage(t *testing.T) {
	ctrl, ctx := gomock.WithContext(context.Background(), t)
	defer ctrl.Finish()

	contentAddressableStorage := mock.NewMockContentAddressableStorage(ctrl)
	contentAddressableStorage.EXPECT().GetAction(ctx, newTestUtilDigest("555", 7)).
		Return(&remoteexecution.Action{
			// CommandDigest not needed
			InputRootDigest: newTestRemoteDigest("777", 42),
		}, nil)
	contentAddressableStorage.EXPECT().GetDirectory(ctx, newTestUtilDigest("777", 42)).
		Return(nil, status.Error(codes.Internal, "Storage is offline"))
	baseEnvironmentManager := mock.NewMockManager(ctrl)
	baseEnvironment := mock.NewMockManagedEnvironment(ctrl)
	baseEnvironmentManager.EXPECT().Acquire(newTestUtilDigest("555", 7), map[string]string{}).
		Return(baseEnvironment, nil)
	buildDirectory := mock.NewMockDirectory(ctrl)
	baseEnvironment.EXPECT().GetBuildDirectory().Return(buildDirectory)
	baseEnvironment.EXPECT().Release()

	inputProviderManager := environment.NewInputProviderManager(baseEnvironmentManager, contentAddressableStorage)
	environment, err := inputProviderManager.Acquire(newTestUtilDigest("555", 7), map[string]string{})
	require.NoError(t, err)
	response, err := environment.Run(ctx, &runner.RunRequest{
		// No members used anyway.
	})
	require.Equal(t, status.ErrorProto(status.New(codes.Internal, "Failed to obtain input directory \".\": Storage is offline").Proto()), err)
	require.Nil(t, response)
	environment.Release()
}

// TestInputProviderManagerSuccess tests a full invocation of a simple build step.
func TestInputProviderManagerSuccess(t *testing.T) {
	ctrl, ctx := gomock.WithContext(context.Background(), t)
	defer ctrl.Finish()

	// File system operations that should occur against the build directory.
	buildDirectory := mock.NewMockDirectory(ctrl)
	buildDirectory.EXPECT().Mkdir("sub", os.FileMode(0777)).Return(nil)
	subDirectory := mock.NewMockDirectory(ctrl)
	buildDirectory.EXPECT().Enter("sub").Return(subDirectory, nil)
	subDirectory.EXPECT().Close()

	// Read operations against the Content Addressable Storage.
	contentAddressableStorage := mock.NewMockContentAddressableStorageReader(ctrl)
	contentAddressableStorage.EXPECT().GetAction(ctx, newTestUtilDigest("001", 123)).
		Return(&remoteexecution.Action{
			// CommandDigest not used
			InputRootDigest: newTestRemoteDigest("003", 345),
		}, nil)

	contentAddressableStorage.EXPECT().GetDirectory(ctx, newTestUtilDigest("003", 345)).
		Return(&remoteexecution.Directory{
			Directories: []*remoteexecution.DirectoryNode{
				{Name: "sub", Digest: newTestRemoteDigest("004", 456)},
			},
		}, nil)
	contentAddressableStorage.EXPECT().GetDirectory(ctx, newTestUtilDigest("004", 456)).
		Return(&remoteexecution.Directory{
			Files: []*remoteexecution.FileNode{
				{Name: "hello.cc", Digest: newTestRemoteDigest("005", 567)},
			},
		}, nil)
	contentAddressableStorage.EXPECT().GetFile(ctx, newTestUtilDigest("005", 567), buildDirectory, "hello.cc", false).
		Return(nil)

	// Command execution.
	baseEnvironmentManager := mock.NewMockManager(ctrl)
	baseEnvironment := mock.NewMockManagedEnvironment(ctrl)
	baseEnvironmentManager.EXPECT().Acquire(
		newTestUtilDigest("001", 123),
		map[string]string{
			"container-image": "ubuntu:latest",
		}).Return(baseEnvironment, nil)
	baseEnvironment.EXPECT().GetBuildDirectory().Return(buildDirectory)
	baseEnvironment.EXPECT().Run(ctx, &runner.RunRequest{
		Arguments: []string{"ls", "-l"},
		EnvironmentVariables: map[string]string{
			"PATH": "/bin",
		},
		WorkingDirectory: "some/sub/directory",
		StdoutPath:       ".stdout.txt",
		StderrPath:       ".stderr.txt",
	}).Return(&runner.RunResponse{
		ExitCode: 0,
	}, nil)
	baseEnvironment.EXPECT().Release()

	inputProviderManager := environment.NewInputProviderManager(baseEnvironmentManager, contentAddressableStorage)
	environment, err := inputProviderManager.Acquire(
		newTestUtilDigest("001", 123),
		map[string]string{
			"container-image": "ubuntu:latest",
		})
	require.NoError(t, err)
	response, err := environment.Run(ctx, &runner.RunRequest{
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
		ExitCode: 0,
	}, response)
	environment.Release()
}
