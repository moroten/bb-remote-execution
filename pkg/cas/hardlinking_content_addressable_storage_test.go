package cas_test

import (
	"context"
	"fmt"
	"testing"

	remoteexecution "github.com/bazelbuild/remote-apis/build/bazel/remote/execution/v2"
	"github.com/buildbarn/bb-remote-execution/internal/mock"
	"github.com/buildbarn/bb-remote-execution/pkg/cas"
	"github.com/buildbarn/bb-storage/pkg/util"
	"github.com/golang/mock/gomock"
	"github.com/stretchr/testify/require"
)

func newTestHash(hash3Digits string) string {
	return "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b" + hash3Digits
}

func newTestDigest(hash3Digits string, sizeBytes int64) *util.Digest {
	return util.MustNewDigest(
		"debian8",
		&remoteexecution.Digest{
			Hash:      newTestHash(hash3Digits),
			SizeBytes: sizeBytes,
		})
}

func TestHardlinkingContentAddressableStorageGetFileSuccess(t *testing.T) {
	ctrl, ctx := gomock.WithContext(context.Background(), t)
	defer ctrl.Finish()

	destDirectory := mock.NewMockDirectory(ctrl)

	subDirectory := mock.NewMockDirectory(ctrl)

	rootDirectory := mock.NewMockDirectory(ctrl)
	rootDirectory.EXPECT().Mkdir(gomock.Any(), gomock.Any()).Times(256).Return(nil)

	rootDirectory.EXPECT().Enter("e3").Return(subDirectory, nil)
	destDirectory.EXPECT().Link("foo.txt", subDirectory, newTestHash("001")+"-7+x").Return(nil)
	subDirectory.EXPECT().Close().Return(nil)

	baseCas := mock.NewMockContentAddressableStorageReader(ctrl)
	baseCas.EXPECT().GetFile(ctx, newTestDigest("001", 7), destDirectory, "foo.txt", true).Return(nil)

	cas, err := cas.NewHardlinkingContentAddressableStorage(
		baseCas,
		util.DigestKeyWithoutInstance,
		rootDirectory,
		1,  // maxFiles
		10, // maxSize
	)
	require.NoError(t, err)

	err = cas.GetFile(ctx, newTestDigest("001", 7), destDirectory, "foo.txt", true)
	require.NoError(t, err)
}

func TestHardlinkingContentAddressableStorageGetFileMaxCountOverflow(t *testing.T) {
	ctrl, ctx := gomock.WithContext(context.Background(), t)
	defer ctrl.Finish()

	destDirectory := mock.NewMockDirectory(ctrl)

	subDirectory := mock.NewMockDirectory(ctrl)

	rootDirectory := mock.NewMockDirectory(ctrl)
	rootDirectory.EXPECT().Mkdir(gomock.Any(), gomock.Any()).Times(256).Return(nil)

	rootDirectory.EXPECT().Enter("e3").AnyTimes().Return(subDirectory, nil)
	destDirectory.EXPECT().Link("foo.txt", subDirectory, gomock.Any()).AnyTimes().Return(nil)
	subDirectory.EXPECT().Close().AnyTimes().Return(nil)

	baseCas := mock.NewMockContentAddressableStorageReader(ctrl)
	baseCas.EXPECT().GetFile(ctx, gomock.Any(), destDirectory, "foo.txt", true).AnyTimes().Return(nil)

	cas, err := cas.NewHardlinkingContentAddressableStorage(
		baseCas,
		util.DigestKeyWithoutInstance,
		rootDirectory,
		100, // maxFiles
		1e9, // maxSize
	)
	require.NoError(t, err)

	// Add 100 files
	for i := 0; i < 100; i++ {
		err = cas.GetFile(ctx, newTestDigest(fmt.Sprintf("%03d", i), 7), destDirectory, "foo.txt", true)
		require.NoError(t, err)
	}
	// Get 0 and 2 again so that 1 is to be removed when getting 999.
	subDirectory.EXPECT().Link(newTestHash("002")+"-7+x", destDirectory, "foo.txt").Return(nil)
	err = cas.GetFile(ctx, newTestDigest("002", 7), destDirectory, "foo.txt", true)
	require.NoError(t, err)
	subDirectory.EXPECT().Link(newTestHash("000")+"-7+x", destDirectory, "foo.txt").Return(nil)
	err = cas.GetFile(ctx, newTestDigest("000", 7), destDirectory, "foo.txt", true)
	require.NoError(t, err)
	// Get 999 and expect 1 to be removed.
	subDirectory.EXPECT().Remove(newTestHash("001") + "-7+x").Return(nil)
	err = cas.GetFile(ctx, newTestDigest("999", 7), destDirectory, "foo.txt", true)
	require.NoError(t, err)
}

func TestHardlinkingContentAddressableStorageGetFileSpaceOverflow(t *testing.T) {
	ctrl, ctx := gomock.WithContext(context.Background(), t)
	defer ctrl.Finish()

	destDirectory := mock.NewMockDirectory(ctrl)

	subDirectory := mock.NewMockDirectory(ctrl)

	rootDirectory := mock.NewMockDirectory(ctrl)
	rootDirectory.EXPECT().Mkdir(gomock.Any(), gomock.Any()).Times(256).Return(nil)

	rootDirectory.EXPECT().Enter("e3").AnyTimes().Return(subDirectory, nil)
	destDirectory.EXPECT().Link("foo.txt", subDirectory, gomock.Any()).AnyTimes().Return(nil)
	subDirectory.EXPECT().Close().AnyTimes().Return(nil)

	baseCas := mock.NewMockContentAddressableStorageReader(ctrl)
	baseCas.EXPECT().GetFile(ctx, gomock.Any(), destDirectory, "foo.txt", true).AnyTimes().Return(nil)

	cas, err := cas.NewHardlinkingContentAddressableStorage(
		baseCas,
		util.DigestKeyWithoutInstance,
		rootDirectory,
		1e9, // maxFiles
		700, // maxSize
	)
	require.NoError(t, err)

	// Add 100 files
	for i := 0; i < 100; i++ {
		err = cas.GetFile(ctx, newTestDigest(fmt.Sprintf("%03d", i), 7), destDirectory, "foo.txt", true)
		require.NoError(t, err)
	}
	// Get 0, 1 and 3 again so that 2 is to be removed when getting 999.
	subDirectory.EXPECT().Link(newTestHash("003")+"-7+x", destDirectory, "foo.txt").Return(nil)
	err = cas.GetFile(ctx, newTestDigest("003", 7), destDirectory, "foo.txt", true)
	require.NoError(t, err)
	subDirectory.EXPECT().Link(newTestHash("000")+"-7+x", destDirectory, "foo.txt").Return(nil)
	err = cas.GetFile(ctx, newTestDigest("000", 7), destDirectory, "foo.txt", true)
	require.NoError(t, err)
	subDirectory.EXPECT().Link(newTestHash("001")+"-7+x", destDirectory, "foo.txt").Return(nil)
	err = cas.GetFile(ctx, newTestDigest("001", 7), destDirectory, "foo.txt", true)
	require.NoError(t, err)
	// Get 999 and expect 2 to be removed.
	subDirectory.EXPECT().Remove(newTestHash("002") + "-7+x").Return(nil)
	err = cas.GetFile(ctx, newTestDigest("999", 1), destDirectory, "foo.txt", true)
	require.NoError(t, err)
}
