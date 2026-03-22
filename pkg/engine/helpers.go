package engine

import (
	"archive/tar"
	"bufio"
	"context"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	dockertypes "github.com/docker/docker/api/types"
)

// ── Naming ────────────────────────────────────────────────────────────────────

func instanceImageTag(stackName, serviceName, mode string) string {
	if stackName != "" {
		return "shipyard/" + sanitize(stackName) + "/" + sanitize(serviceName) + ":" + mode
	}
	return "shipyard/" + sanitize(serviceName) + ":" + mode
}

func instanceContainerName(stackName, serviceName, mode string, index int) string {
	base := "shipyard_"
	if stackName != "" {
		base += sanitize(stackName) + "_"
	}
	base += sanitize(serviceName) + "_" + mode
	if index > 0 {
		base += "_" + strconv.Itoa(index)
	}
	return base
}

func stackNetwork(stackName string) string {
	if stackName == "" {
		return ""
	}
	return "shipyard_" + sanitize(stackName)
}

func sanitize(s string) string {
	s = strings.ToLower(s)
	s = strings.ReplaceAll(s, " ", "_")
	s = strings.ReplaceAll(s, "-", "_")
	return s
}

func trimOutput(s string) string {
	return strings.TrimSpace(s)
}

// ── Docker build helpers ──────────────────────────────────────────────────────

func dockerBuildOptions(tag, dockerfile string, args map[string]*string) dockertypes.ImageBuildOptions {
	return dockertypes.ImageBuildOptions{
		Tags:       []string{tag},
		Dockerfile: dockerfile,
		BuildArgs:  args,
		Remove:     true,
	}
}

func tarDir(srcDir string) (io.Reader, error) {
	pr, pw := io.Pipe()
	go func() {
		tw := tar.NewWriter(pw)
		err := filepath.Walk(srcDir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(srcDir, path)
			if err != nil {
				return err
			}
			hdr, err := tar.FileInfoHeader(info, info.Name())
			if err != nil {
				return err
			}
			hdr.Name = rel
			if err := tw.WriteHeader(hdr); err != nil {
				return err
			}
			if info.Mode().IsRegular() {
				f, err := os.Open(path)
				if err != nil {
					return err
				}
				defer f.Close()
				_, err = io.Copy(tw, f)
				return err
			}
			return nil
		})
		tw.Close()
		pw.CloseWithError(err)
	}()
	return pr, nil
}

// ── Log streaming ─────────────────────────────────────────────────────────────

// pipeToChannel reads lines from a reader and sends them to a LogLine channel.
// Used by Compose, Kubernetes, Nomad, and Podman runners.
func pipeToChannel(ctx context.Context, r io.Reader, id, stream string, ch chan<- LogLine) {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return
		case ch <- LogLine{InstanceID: id, Stream: stream, Text: scanner.Text() + "\n"}:
		}
	}
}

// streamDockerLogs reads Docker's multiplexed log stream (8-byte header format).
// Used by the DockerRunner.
func streamDockerLogs(ctx context.Context, reader io.Reader, id string, ch chan<- LogLine) {
	hdr := make([]byte, 8)
	buf := bufio.NewReader(reader)
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		if _, err := io.ReadFull(buf, hdr); err != nil {
			return
		}
		stream := "stdout"
		if hdr[0] == 2 {
			stream = "stderr"
		}
		size := int(hdr[4])<<24 | int(hdr[5])<<16 | int(hdr[6])<<8 | int(hdr[7])
		payload := make([]byte, size)
		if _, err := io.ReadFull(buf, payload); err != nil {
			return
		}
		ch <- LogLine{InstanceID: id, Stream: stream, Text: string(payload)}
	}
}

// ── Misc helpers ──────────────────────────────────────────────────────────────

func parseMemory(mem string) (int64, error) {
	if len(mem) < 2 {
		return 0, nil
	}
	unit := mem[len(mem)-1]
	val, err := strconv.ParseFloat(mem[:len(mem)-1], 64)
	if err != nil {
		return 0, err
	}
	switch unit {
	case 'm', 'M':
		return int64(val * 1024 * 1024), nil
	case 'g', 'G':
		return int64(val * 1024 * 1024 * 1024), nil
	case 'k', 'K':
		return int64(val * 1024), nil
	}
	return int64(val), nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// strPtrMap converts map[string]string to map[string]*string as required by the Docker SDK.
func strPtrMap(m map[string]string) map[string]*string {
	out := make(map[string]*string, len(m))
	for k, v := range m {
		val := v
		out[k] = &val
	}
	return out
}
