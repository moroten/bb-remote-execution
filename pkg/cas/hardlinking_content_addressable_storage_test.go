package cas_test

import (
	"context"
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
