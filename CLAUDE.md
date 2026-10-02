# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

> **Note:** This project is archived upstream and no longer maintained (see top of README.md).

## What this is

kaniko builds container images from a Dockerfile entirely in userspace, without a Docker daemon. It is designed to run *as* a container image (`gcr.io/kaniko-project/executor`): it unpacks base image filesystems directly onto `/` of the container it runs in, executes Dockerfile commands there, and snapshots the root filesystem after each command to produce layers. Running the executor binary on a dev machine will mutate the host filesystem — don't do it outside a container/VM.

## Commands

The build uses vendored dependencies (`GOFLAGS=-mod=vendor` is exported by the Makefile). After changing dependencies, run `go mod vendor`.

```shell
make out/executor          # build executor binary (static, CGO_ENABLED=0)
make out/warmer            # build cache warmer binary
make images                # build executor (latest/debug/slim) and warmer images from deploy/Dockerfile

make test                  # unit tests (all pkgs except integration) + hack/boilerplate.sh + hack/gofmt.sh
go test ./pkg/executor -run TestName -v   # single unit test (add GOFLAGS=-mod=vendor if not via make)

./hack/linter.sh           # golangci-lint (config in .golangci.yaml)
find . -name "*.go" | grep -v vendor/ | xargs gofmt -l -s -w   # fix gofmt issues
```

Tests require Linux (a Vagrantfile is provided for macOS/Windows). Every Go file needs the Apache license header — `hack/boilerplate.sh` enforces it.

### Integration tests

Integration tests (`integration/`) build each `integration/dockerfiles/Dockerfile_test_*` with both `docker` and kaniko, then compare results with `container-diff`. They need `container-diff` and either a GCS bucket + image repo (`GCS_BUCKET`, `IMAGE_REPO` env vars) or local mode:

```shell
LOCAL=1 make integration-test                                   # local registry, no GCS
DOCKERFILE_PATTERN="Dockerfile_test_add*" make integration-test-run
go test ./integration -v --bucket $GCS_BUCKET --repo $IMAGE_REPO -run TestLayers/test_layer_Dockerfile_test_copy_bucket
```

Make targets `integration-test-run`, `-layers`, `-k8s`, `-misc` select subsets via `-run`.

## Architecture

**Entrypoints:** `cmd/executor/cmd/root.go` (cobra; all flags populate `config.KanikoOptions` in `pkg/config/options.go`) and `cmd/warmer` (pre-populates the base-image cache). The executor resolves the build context, then calls `executor.DoBuild` and `executor.DoPush`.

**Build flow (`pkg/executor/build.go`):**
1. `dockerfile.ParseStages` / `MakeKanikoStages` (`pkg/dockerfile`) parse the Dockerfile using moby/buildkit's `instructions` package and resolve ARGs, stage names, and `--target`.
2. `CalculateDependencies` determines which files each stage needs from earlier stages (`COPY --from`); after a non-final stage builds, those files are saved to `$KANIKO_DIR/<stage-idx>` (default `/kaniko`) and the filesystem is wiped before the next stage.
3. For each stage, a `stageBuilder` unpacks the base image to `config.RootDir` (only if some command `RequiresUnpackedFS`), then for each command: compute the composite cache key, optionally pull a cached layer, `ExecuteCommand`, and take a snapshot → layer appended to the image via go-containerregistry `mutate`.
4. `push.go` handles pushing to destinations, tarball output, digest files, and pushing cache layers.

**Commands (`pkg/commands`):** each Dockerfile instruction implements the `DockerCommand` interface (`commands.go`). Key methods governing build behavior: `MetadataOnly` (no snapshot needed), `ProvidesFilesToSnapshot`/`FilesToSnapshot` (COPY/ADD know exact changed files; RUN doesn't, forcing a full-FS snapshot), `RequiresUnpackedFS`, `CacheCommand` (returns a cache-aware variant, e.g. `CachingRunCommand`, `CachingCopyCommand`). `RunMarkerCommand` (`--use-new-run`) detects changes via mtime markers instead of full hashing. Both RUN variants go through `runCommandInExec` in `run.go`.

**RUN mounts and build secrets:** kaniko parses Dockerfiles with BuildKit's `instructions` package, which only fills in `RUN --mount` options once `RunCommand.Expand` is called with a variable expander. Before that, `instructions.GetMounts` returns mounts with no options set. Only `type=secret` is supported (`pkg/commands/run_secrets.go`); other mount types are logged and ignored. Secrets come from the `--secret id=…,src=…|env=…` flag (`config.Secrets` in `pkg/config/secrets.go`) and are passed to RUN commands through `GetCommand`. Kaniko can't bind-mount, so it writes secret files onto the real filesystem just before the command runs. Afterwards it deletes them and any directories it created, and resets the parent directory's mtime, all before the snapshot, because the default snapshot hasher includes directory mtimes.

**Snapshotting (`pkg/snapshot`):** `Snapshotter` walks the root FS, compares file hashes against a `LayeredMap` of prior layers, and writes a tar of added/changed files plus whiteouts for deletions. `--snapshot-mode` (full/redo/time) selects the hashing function. Paths in the ignore list (`pkg/util/fs_util.go`: `/kaniko`, `/proc`, `/sys`, mounted volumes, etc.) are never snapshotted or deleted — changes touching kaniko's own directory or mounts usually need ignore-list updates.

**Caching:** `pkg/executor/composite_cache.go` builds per-command cache keys from base image digest + command string + context file hashes (+ args/envs for RUN). Cached layers are stored in/retrieved from a registry repo (`pkg/cache`); base images can come from a local warmed cache dir.

**Other packages:** `pkg/buildcontext` — context sources (local dir, tar, GCS, S3, Azure Blob, git, https); `pkg/image` — base image retrieval with remote/cache fallback; `pkg/creds` — registry keychains; `pkg/util` — FS extraction, tar, command helpers (large and widely used); `pkg/timing` — `BENCHMARK_FILE` timing output.

**Tests:** unit tests live next to the code and rely on `testutil/` helpers and fakes (`pkg/executor/fakes.go`, `pkg/commands/fake_commands.go`, `pkg/fakes`). Because code operates on `config.RootDir`, tests typically redirect it to a temp dir. Tests may run as non-root, so avoid operations that need root (e.g. `chown` to uid 0) unless the test skips when not root.

`Test_stageBuilder_saveSnapshotToLayer` in `pkg/executor` fails on unmodified `main` (digest mismatch); don't mistake it for a regression.
