package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	_ "net/http/pprof"
	"net/url"
	"os"
	"runtime"
	"time"

	remoteexecution "github.com/bazelbuild/remote-apis/build/bazel/remote/execution/v2"
	re_blobstore "github.com/buildbarn/bb-remote-execution/pkg/blobstore"
	"github.com/buildbarn/bb-remote-execution/pkg/builder"
	"github.com/buildbarn/bb-remote-execution/pkg/configuration/bb_worker"
	cas_re "github.com/buildbarn/bb-remote-execution/pkg/cas"
	"github.com/buildbarn/bb-remote-execution/pkg/environment"
	"github.com/buildbarn/bb-remote-execution/pkg/proto/scheduler"
	"github.com/buildbarn/bb-storage/pkg/ac"
	"github.com/buildbarn/bb-storage/pkg/blobstore"
	blobstore_configuration "github.com/buildbarn/bb-storage/pkg/blobstore/configuration"
	"github.com/buildbarn/bb-storage/pkg/cas"
	"github.com/buildbarn/bb-storage/pkg/filesystem"
	"github.com/buildbarn/bb-storage/pkg/util"
	"github.com/grpc-ecosystem/go-grpc-prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"google.golang.org/grpc"
)

func main() {
	// To ease privilege separation, clear the umask. This process
	// either writes files into directories that can easily be
	// closed off, or creates files with the appropriate mode to be
	// secure.
	clearUmask()

	if len(os.Args) != 2 {
		log.Fatal("Usage: bb_worker bb_worker.jsonnet")
	}

	workerConfiguration, err := configuration.GetWorkerConfiguration(os.Args[1])
	if err != nil {
		log.Fatalf("Failed to read configuration from %s: %s", os.Args[1], err)
	}

	browserURL, err := url.Parse(workerConfiguration.BrowserUrl)
	if err != nil {
		log.Fatal("Failed to parse browser URL: ", err)
	}

	// Web server for metrics and profiling.
	http.Handle("/metrics", promhttp.Handler())
	go func() {
		log.Fatal(http.ListenAndServe(workerConfiguration.MetricsListenAddress, nil))
	}()

	// Storage access.
	contentAddressableStorageBlobAccess, actionCacheBlobAccess, err := blobstore_configuration.CreateBlobAccessObjectsFromConfig(workerConfiguration.Blobstore)
	if err != nil {
		log.Fatal("Failed to create blob access: ", err)
	}

	// Directories where builds take place.
	buildDirectory, err := filesystem.NewLocalDirectory(workerConfiguration.BuildDirectoryPath)
	if err != nil {
		log.Fatal("Failed to open cache directory: ", err)
	}

	// On-disk caching of content for efficient linking into build environments.
	cacheDirectory, err := filesystem.NewLocalDirectory(workerConfiguration.CacheDirectoryPath)
	if err != nil {
		log.Fatal("Failed to open cache directory: ", err)
	}
	if err := cacheDirectory.RemoveAllChildren(); err != nil {
		log.Fatal("Failed to clear cache directory: ", err)
	}

	// Cached read access to the Content Addressable Storage. All
	// workers make use of the same cache, to increase the hit rate.
	hardlinkingCas, err := cas_re.NewHardlinkingContentAddressableStorage(
		cas.NewBlobAccessContentAddressableStorage(
			re_blobstore.NewExistencePreconditionBlobAccess(contentAddressableStorageBlobAccess),
			workerConfiguration.MaximumMessageSizeBytes),
		util.DigestKeyWithoutInstance, cacheDirectory,
		int(workerConfiguration.MaximumCacheFileCount), int64(workerConfiguration.MaximumCacheSizeBytes))
	if err != nil {
		log.Fatal("Failed to create hard linking CAS: ", err)
	}
	contentAddressableStorageReader := cas_re.NewDirectoryCachingContentAddressableStorage(
		hardlinkingCas, util.DigestKeyWithoutInstance, int(workerConfiguration.MaximumMemoryCachedDirectories))
	actionCache := ac.NewBlobAccessActionCache(actionCacheBlobAccess)

	// Create connection with scheduler.
	schedulerConnection, err := grpc.Dial(
		workerConfiguration.SchedulerAddress,
		grpc.WithInsecure(),
		grpc.WithUnaryInterceptor(grpc_prometheus.UnaryClientInterceptor),
		grpc.WithStreamInterceptor(grpc_prometheus.StreamClientInterceptor))
	if err != nil {
		log.Fatal("Failed to create scheduler RPC client: ", err)
	}
	schedulerClient := scheduler.NewSchedulerClient(schedulerConnection)

	// Execute commands using a separate runner process. Due to the
	// interaction between threads, forking and execve() returning
	// ETXTBSY, concurrent execution of build actions can only be
	// used in combination with a runner process. Having a separate
	// runner process also makes it possible to apply privilege
	// separation.
	runnerConnection, err := grpc.Dial(
		workerConfiguration.RunnerAddress,
		grpc.WithInsecure(),
		grpc.WithUnaryInterceptor(grpc_prometheus.UnaryClientInterceptor),
		grpc.WithStreamInterceptor(grpc_prometheus.StreamClientInterceptor))
	if err != nil {
		log.Fatal("Failed to create runner RPC client: ", err)
	}

	// Build environment capable of executing one action at a time.
	// The build takes place in the root of the build directory.
	environmentManager := environment.NewCleanBuildDirectoryManager(
		environment.NewSingletonManager(
			environment.NewRemoteExecutionEnvironment(runnerConnection, buildDirectory)))

	// Create a per-action subdirectory in the build directory named
	// after the action digest, so that multiple actions may be run
	// concurrently within the same environment.
	// TODO(edsch): It might make sense to disable this if
	// concurrency is disabled to improve action cache hit rate, but
	// only if there are no other workers in the same cluster that
	// have concurrency enabled.
	subdirectoryFormat := util.DigestKeyWithoutInstance
	if runtime.GOOS == "windows" {
		// Try to keep the paths as short as possible
		subdirectoryFormat = util.DigestKeyShort
	}
	environmentManager = environment.NewInputProviderManager(
		environment.NewActionDigestSubdirectoryManager(
			environment.NewConcurrentManager(environmentManager),
			subdirectoryFormat),
		contentAddressableStorageReader)

	for i := uint64(0); i < workerConfiguration.Concurrency; i++ {
		go func() {
			// Per-worker separate writer of the Content
			// Addressable Storage that batches writes after
			// completing the build action.
			blobAccessWriter, contentAddressableStorageFlusher := re_blobstore.NewBatchedStoreBlobAccess(
				re_blobstore.NewExistencePreconditionBlobAccess(contentAddressableStorageBlobAccess),
				util.DigestKeyWithoutInstance, 100)
			blobAccessWriter = blobstore.NewMetricsBlobAccess(
				blobAccessWriter,
				"cas_batched_store")
			contentAddressableStorageWriter := cas.NewBlobAccessContentAddressableStorage(
				blobAccessWriter,
				workerConfiguration.MaximumMessageSizeBytes)
			contentAddressableStorage := cas_re.NewReadWriteDecouplingContentAddressableStorage(
				contentAddressableStorageReader,
				contentAddressableStorageWriter)
			buildExecutor := builder.NewStorageFlushingBuildExecutor(
				builder.NewCachingBuildExecutor(
					builder.NewLocalBuildExecutor(
						contentAddressableStorage,
						environmentManager),
					contentAddressableStorageWriter,
					actionCache,
					browserURL),
				contentAddressableStorageFlusher)

			// Repeatedly ask the scheduler for work.
			for {
				err := subscribeAndExecute(schedulerClient, buildExecutor, browserURL)
				log.Print("Failed to subscribe and execute: ", err)
				time.Sleep(time.Second * 3)
			}
		}()
	}
	select {}
}

func subscribeAndExecute(schedulerClient scheduler.SchedulerClient, buildExecutor builder.BuildExecutor, browserURL *url.URL) error {
	stream, err := schedulerClient.GetWork(context.Background())
	if err != nil {
		return err
	}
	defer stream.CloseSend()

	capabilities := &remoteexecution.Platform{} // TODO: Fill in with real capabilities from command line
	for {
		messageToScheduler := &scheduler.GetWorkMessageToScheduler{
			Content: &scheduler.GetWorkMessageToScheduler_WorkerCapabilities{WorkerCapabilities: capabilities},
		}
		if err := stream.Send(messageToScheduler); err != nil {
			return err
		}
		// Receive a request which is satisfied by the just sent capabilities
		request, err := stream.Recv()
		if err != nil {
			return err
		}

		// Print URL of the action into the log before execution.
		actionURL, err := browserURL.Parse(
			fmt.Sprintf(
				"/action/%s/%s/%d/",
				request.InstanceName,
				request.ActionDigest.Hash,
				request.ActionDigest.SizeBytes))
		if err != nil {
			return err
		}
		log.Print("Action: ", actionURL.String())

		response, _ := buildExecutor.Execute(stream.Context(), request)
		log.Print("ExecuteResponse: ", response)
		messageToScheduler = &scheduler.GetWorkMessageToScheduler{
			Content: &scheduler.GetWorkMessageToScheduler_ExecuteResponse{ExecuteResponse: response},
		}
		if err := stream.Send(messageToScheduler); err != nil {
			return err
		}
	}
}
