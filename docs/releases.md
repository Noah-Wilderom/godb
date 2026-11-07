# Releases

Push a version tag such as `v0.1.0` to publish the Linux binary archives and
`ghcr.io/noah-wilderom/godb:v0.1.0`. The image supports Linux AMD64 and ARM64.
Stable releases also update `latest`; prereleases such as `v0.1.0-rc.1` do not.
The manual Release workflow takes an existing tag; it does not create one.

The workflow explicitly uses the free, open source GoReleaser distribution,
pinned to v2.20.0. Its `dockers_v2` support reuses the release binaries, so the
root `Dockerfile` only packages them. Use the snapshot command below to build
images locally; a plain `docker build .` does not have GoReleaser's binary context.

Registry authentication uses the workflow's built-in `GITHUB_TOKEN` with
`packages: write`; no Docker Hub credentials or GoReleaser Pro license are needed.
GHCR container storage and bandwidth are currently free, according to
[GitHub's billing documentation](https://docs.github.com/en/billing/concepts/product-billing/github-packages).
GitHub Actions usage follows the repository's plan and allowances.

New GHCR packages default to private. After the first release, set the package's
visibility to public in GitHub Packages if anonymous pulls are desired.

## Running the container

The container runs the CLI, which reads and writes local database files. Mount a
host directory to persist those files. On Linux, pass your user and group IDs
so newly created files belong to you:

```sh
docker run --rm \
  --user "$(id -u):$(id -g)" \
  --mount "type=bind,source=$PWD,target=/var/lib/godb" \
  ghcr.io/noah-wilderom/godb:v0.1.0 database create example.db
```

## Checking a release locally

With GoReleaser v2.20.0, Docker, and Buildx installed:

```sh
goreleaser check
goreleaser release --snapshot --clean
```

A snapshot builds local images with architecture suffixes without publishing.
`--skip=publish` on a regular release and `goreleaser build` do not build v2 Docker
images. See [GoReleaser's Docker documentation](https://goreleaser.com/customization/package/dockers_v2/).
