package orchestrator

import (
	"archive/tar"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/client"
	"github.com/shipyard/shipyard/pkg/shipfile"
)

// BuildResult holds the outcome of a Docker image build.
type BuildResult struct {
	ImageID    string
	ImageTag   string
	BuildLogs  string
}

// Builder builds Docker images using the Docker SDK.
type Builder struct {
	docker *client.Client
}

// NewBuilder creates a Builder connected to the local Docker daemon.
func NewBuilder() (*Builder, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("builder: failed to connect to Docker daemon: %w", err)
	}
	return &Builder{docker: cli}, nil
}

// Build builds a Docker image from the given source context directory
// and the build config in the ResolvedMode.
// contextDir is the absolute path to the directory containing the Dockerfile.
func (b *Builder) Build(ctx context.Context, contextDir string, mode *shipfile.ResolvedMode, imageTag string) (*BuildResult, error) {
	dockerfile := mode.Build.Dockerfile
	if dockerfile == "" {
		dockerfile = "Dockerfile"
	}

	// Create a tar archive of the build context directory.
	tarReader, err := tarDirectory(contextDir)
	if err != nil {
		return nil, fmt.Errorf("builder: failed to tar build context: %w", err)
	}

	// Build args from the mode config.
	buildArgs := make(map[string]*string)
	for k, v := range mode.Build.Args {
		val := v
		buildArgs[k] = &val
	}

	options := types.ImageBuildOptions{
		Tags:       []string{imageTag},
		Dockerfile: dockerfile,
		BuildArgs:  buildArgs,
		Remove:     true, // remove intermediate containers after build
		NoCache:    false,
	}

	resp, err := b.docker.ImageBuild(ctx, tarReader, options)
	if err != nil {
		return nil, fmt.Errorf("builder: image build failed: %w", err)
	}
	defer resp.Body.Close()

	// Docker streams build output as JSON lines. We must scan them all to
	// detect build failures — ImageBuild returns nil error even when the
	// Dockerfile RUN commands fail.
	var buildLogs strings.Builder
	decoder := json.NewDecoder(resp.Body)
	for decoder.More() {
		var msg struct {
			Stream string `json:"stream"`
			Error  string `json:"error"`
		}
		if err := decoder.Decode(&msg); err != nil {
			break
		}
		if msg.Stream != "" {
			buildLogs.WriteString(msg.Stream)
		}
		if msg.Error != "" {
			return nil, fmt.Errorf("builder: image build failed: %s", strings.TrimSpace(msg.Error))
		}
	}

	return &BuildResult{
		ImageTag:  imageTag,
		BuildLogs: buildLogs.String(),
	}, nil
}

// tarDirectory creates an in-memory tar archive of all files in a directory.
// This is what Docker expects as the build context.
func tarDirectory(srcDir string) (io.Reader, error) {
	pr, pw := io.Pipe()

	go func() {
		tw := tar.NewWriter(pw)
		err := filepath.Walk(srcDir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}

			// Compute path relative to srcDir for the tar header.
			relPath, err := filepath.Rel(srcDir, path)
			if err != nil {
				return err
			}

			// For symlinks, read the actual link target so Docker doesn't
			// reject the build context with "forbidden path outside build context".
			linkTarget := ""
			if info.Mode()&os.ModeSymlink != 0 {
				linkTarget, err = os.Readlink(path)
				if err != nil {
					return err
				}
			}

			header, err := tar.FileInfoHeader(info, linkTarget)
			if err != nil {
				return err
			}
			header.Name = relPath

			if err := tw.WriteHeader(header); err != nil {
				return err
			}

			// Only write content for regular files.
			if info.Mode().IsRegular() {
				f, err := os.Open(path)
				if err != nil {
					return err
				}
				defer f.Close()
				if _, err := io.Copy(tw, f); err != nil {
					return err
				}
			}
			return nil
		})

		tw.Close()
		pw.CloseWithError(err)
	}()

	return pr, nil
}