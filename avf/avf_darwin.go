//go:build darwin

package avf

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/Code-Hex/vz/v3"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/kballard/go-shellquote"
	"github.com/leighmcculloch/silo/backend"
	"github.com/moby/term"
)

const (
	// Default kernel and initrd URLs - these are Ubuntu cloud kernel images
	defaultKernelURL = "https://cloud-images.ubuntu.com/releases/24.04/release/unpacked/ubuntu-24.04-server-cloudimg-arm64-vmlinuz-generic"
	defaultInitrdURL = "https://cloud-images.ubuntu.com/releases/24.04/release/unpacked/ubuntu-24.04-server-cloudimg-arm64-initrd-generic"

	// VM configuration
	defaultMemoryMB = 4096
	defaultCPUs     = 4
)

// Backend implements the backend.Backend interface using Apple Virtualization Framework
type Backend struct {
	dockerCli *client.Client
	cacheDir  string
}

// NewBackend creates a new AVF backend
func NewBackend() (*Backend, error) {
	// Create Docker client for building images
	dockerCli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("failed to create Docker client: %w", err)
	}

	// Set up cache directory for kernel, initrd, and rootfs
	cacheDir := filepath.Join(os.Getenv("HOME"), ".cache", "silo", "avf")
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create cache directory: %w", err)
	}

	return &Backend{
		dockerCli: dockerCli,
		cacheDir:  cacheDir,
	}, nil
}

// Name returns the backend name
func (b *Backend) Name() string {
	return "avf"
}

// Close closes the backend resources
func (b *Backend) Close() error {
	return b.dockerCli.Close()
}

// Build builds the VM image for the given tool
func (b *Backend) Build(ctx context.Context, opts backend.BuildOptions) error {
	// Step 1: Build Docker image using existing Dockerfile
	if err := b.buildDockerImage(ctx, opts); err != nil {
		return fmt.Errorf("failed to build Docker image: %w", err)
	}

	// Step 2: Export rootfs from Docker image
	rootfsPath := filepath.Join(b.cacheDir, opts.Target+"-rootfs")
	if err := b.exportRootfs(ctx, opts.Target, rootfsPath); err != nil {
		return fmt.Errorf("failed to export rootfs: %w", err)
	}

	// Step 3: Download kernel and initrd if not already cached
	if err := b.ensureKernelAndInitrd(ctx); err != nil {
		return fmt.Errorf("failed to download kernel/initrd: %w", err)
	}

	return nil
}

// buildDockerImage builds a Docker image using the provided Dockerfile
func (b *Backend) buildDockerImage(ctx context.Context, opts backend.BuildOptions) error {
	// Create a tar archive with the Dockerfile
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)

	dockerfileContent := []byte(opts.Dockerfile)
	if err := tw.WriteHeader(&tar.Header{
		Name: "Dockerfile",
		Size: int64(len(dockerfileContent)),
		Mode: 0644,
	}); err != nil {
		return fmt.Errorf("failed to write tar header: %w", err)
	}

	if _, err := tw.Write(dockerfileContent); err != nil {
		return fmt.Errorf("failed to write Dockerfile to tar: %w", err)
	}

	if err := tw.Close(); err != nil {
		return fmt.Errorf("failed to close tar: %w", err)
	}

	// Convert build args
	buildArgs := make(map[string]*string)
	for k, v := range opts.BuildArgs {
		v := v
		buildArgs[k] = &v
	}

	// Build the image with AVF-specific tag
	imageName := "silo-avf-" + opts.Target
	resp, err := b.dockerCli.ImageBuild(ctx, &buf, types.ImageBuildOptions{
		Dockerfile: "Dockerfile",
		Target:     opts.Target,
		BuildArgs:  buildArgs,
		Tags:       []string{imageName},
		Remove:     true,
	})
	if err != nil {
		return fmt.Errorf("failed to build image: %w", err)
	}
	defer resp.Body.Close()

	// Read build output
	output, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read build output: %w", err)
	}

	if opts.OnProgress != nil {
		opts.OnProgress(string(output))
	}

	return nil
}

// exportRootfs exports the rootfs from a Docker image
func (b *Backend) exportRootfs(ctx context.Context, target, rootfsPath string) error {
	imageName := "silo-avf-" + target

	// Create a temporary container to export from
	containerResp, err := b.dockerCli.ContainerCreate(ctx, &container.Config{
		Image: imageName,
	}, nil, nil, nil, "")
	if err != nil {
		return fmt.Errorf("failed to create container: %w", err)
	}
	defer b.dockerCli.ContainerRemove(ctx, containerResp.ID, container.RemoveOptions{Force: true})

	// Export the container filesystem
	reader, err := b.dockerCli.ContainerExport(ctx, containerResp.ID)
	if err != nil {
		return fmt.Errorf("failed to export container: %w", err)
	}
	defer reader.Close()

	// Remove old rootfs if exists
	os.RemoveAll(rootfsPath)
	if err := os.MkdirAll(rootfsPath, 0755); err != nil {
		return fmt.Errorf("failed to create rootfs directory: %w", err)
	}

	// Extract tar to rootfs directory
	if err := extractTar(reader, rootfsPath); err != nil {
		return fmt.Errorf("failed to extract rootfs: %w", err)
	}

	// Create necessary device nodes and directories for Linux boot
	if err := b.setupRootfs(rootfsPath); err != nil {
		return fmt.Errorf("failed to setup rootfs: %w", err)
	}

	return nil
}

// extractTar extracts a tar archive to a directory
func extractTar(reader io.Reader, dest string) error {
	tr := tar.NewReader(reader)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}

		target := filepath.Join(dest, header.Name)

		// Prevent path traversal
		if !strings.HasPrefix(filepath.Clean(target), filepath.Clean(dest)) {
			continue
		}

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, os.FileMode(header.Mode)); err != nil {
				return err
			}
		case tar.TypeReg:
			dir := filepath.Dir(target)
			if err := os.MkdirAll(dir, 0755); err != nil {
				return err
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(header.Mode))
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return err
			}
			f.Close()
		case tar.TypeSymlink:
			dir := filepath.Dir(target)
			if err := os.MkdirAll(dir, 0755); err != nil {
				return err
			}
			os.Remove(target) // Remove if exists
			if err := os.Symlink(header.Linkname, target); err != nil {
				// Ignore symlink errors, some targets may not exist
				continue
			}
		case tar.TypeLink:
			dir := filepath.Dir(target)
			if err := os.MkdirAll(dir, 0755); err != nil {
				return err
			}
			linkTarget := filepath.Join(dest, header.Linkname)
			os.Remove(target)
			if err := os.Link(linkTarget, target); err != nil {
				// Ignore link errors
				continue
			}
		}
	}
	return nil
}

// setupRootfs sets up the rootfs with necessary directories and files
func (b *Backend) setupRootfs(rootfsPath string) error {
	// Create necessary directories
	dirs := []string{"dev", "proc", "sys", "run", "tmp", "mnt", "mnt/shared"}
	for _, dir := range dirs {
		if err := os.MkdirAll(filepath.Join(rootfsPath, dir), 0755); err != nil {
			return err
		}
	}

	// Create a simple init script that will be run after kernel boot
	initScript := `#!/bin/bash
# Mount essential filesystems
mount -t proc proc /proc
mount -t sysfs sys /sys
mount -t devtmpfs dev /dev
mkdir -p /dev/pts
mount -t devpts devpts /dev/pts

# Set up networking
ip link set lo up

# Mount 9p shares
for tag in $(cat /sys/bus/virtio/drivers/9pnet_virtio/*/mount_tag 2>/dev/null | tr '\0' '\n'); do
    mountpoint="/mnt/${tag}"
    mkdir -p "$mountpoint"
    mount -t 9p -o trans=virtio,version=9p2000.L "$tag" "$mountpoint"
done

# Execute the real init or command
exec "$@"
`
	initPath := filepath.Join(rootfsPath, "silo-init")
	if err := os.WriteFile(initPath, []byte(initScript), 0755); err != nil {
		return err
	}

	return nil
}

// ensureKernelAndInitrd downloads kernel and initrd if not cached
func (b *Backend) ensureKernelAndInitrd(ctx context.Context) error {
	kernelPath := filepath.Join(b.cacheDir, "vmlinuz")
	initrdPath := filepath.Join(b.cacheDir, "initrd")

	// Download kernel if not exists
	if _, err := os.Stat(kernelPath); os.IsNotExist(err) {
		if err := downloadFile(ctx, defaultKernelURL, kernelPath); err != nil {
			return fmt.Errorf("failed to download kernel: %w", err)
		}
	}

	// Download initrd if not exists
	if _, err := os.Stat(initrdPath); os.IsNotExist(err) {
		if err := downloadFile(ctx, defaultInitrdURL, initrdPath); err != nil {
			return fmt.Errorf("failed to download initrd: %w", err)
		}
	}

	return nil
}

// downloadFile downloads a file from URL to local path
func downloadFile(ctx context.Context, url, dest string) error {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, resp.Status)
	}

	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer f.Close()

	_, err = io.Copy(f, resp.Body)
	return err
}

// Run runs the tool in a VM
func (b *Backend) Run(ctx context.Context, opts backend.RunOptions) error {
	rootfsPath := filepath.Join(b.cacheDir, opts.Tool+"-rootfs")
	kernelPath := filepath.Join(b.cacheDir, "vmlinuz")
	initrdPath := filepath.Join(b.cacheDir, "initrd")

	// Verify required files exist
	for _, path := range []string{rootfsPath, kernelPath, initrdPath} {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			return fmt.Errorf("required file not found: %s (run build first)", path)
		}
	}

	// Build the command to execute
	var fullCmd []string
	if len(opts.Command) > 0 {
		fullCmd = append(opts.Command, opts.Args...)
	} else {
		fullCmd = opts.Args
	}

	// Build the init command with prehooks
	var initCmd string
	if len(opts.Prehooks) > 0 {
		var script strings.Builder
		for _, hook := range opts.Prehooks {
			script.WriteString(hook)
			script.WriteString(" && ")
		}
		script.WriteString("exec ")
		script.WriteString(shellquote.Join(fullCmd...))
		initCmd = "/bin/bash -c " + shellquote.Join(script.String())
	} else if len(fullCmd) > 0 {
		initCmd = shellquote.Join(fullCmd...)
	} else {
		initCmd = "/bin/bash"
	}

	// Build kernel command line
	var cmdline strings.Builder
	cmdline.WriteString("console=hvc0 ")
	cmdline.WriteString("root=/dev/vda rw ")
	cmdline.WriteString("init=/silo-init ")
	cmdline.WriteString("-- " + initCmd)

	// Add environment variables to kernel command line
	for _, env := range opts.Env {
		cmdline.WriteString(" " + env)
	}

	// Create VM configuration
	vmConfig, err := b.createVMConfig(kernelPath, initrdPath, rootfsPath, cmdline.String(), opts)
	if err != nil {
		return fmt.Errorf("failed to create VM config: %w", err)
	}

	// Create and start the VM
	vm, err := vz.NewVirtualMachine(vmConfig)
	if err != nil {
		return fmt.Errorf("failed to create VM: %w", err)
	}

	// Handle context cancellation
	go func() {
		<-ctx.Done()
		vm.Stop()
	}()

	// Set up terminal handling
	if opts.TTY {
		if f, ok := opts.Stdin.(*os.File); ok {
			fd := f.Fd()
			if term.IsTerminal(fd) {
				oldState, err := term.MakeRaw(fd)
				if err != nil {
					return fmt.Errorf("failed to set raw terminal: %w", err)
				}
				defer term.RestoreTerminal(fd, oldState)

				// Handle terminal resize signals
				sigCh := make(chan os.Signal, 1)
				signal.Notify(sigCh, syscall.SIGWINCH)
				defer signal.Stop(sigCh)
			}
		}
	}

	// Start the VM
	if err := vm.Start(); err != nil {
		return fmt.Errorf("failed to start VM: %w", err)
	}

	// Wait for the VM to finish
	<-vm.StateChangedNotify()
	for vm.State() == vz.VirtualMachineStateRunning {
		<-vm.StateChangedNotify()
	}

	return nil
}

// createVMConfig creates the VM configuration
func (b *Backend) createVMConfig(kernelPath, initrdPath, rootfsPath, cmdline string, opts backend.RunOptions) (*vz.VirtualMachineConfiguration, error) {
	// Create boot loader
	bootLoader, err := vz.NewLinuxBootLoader(kernelPath,
		vz.WithCommandLine(cmdline),
		vz.WithInitrd(initrdPath),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create boot loader: %w", err)
	}

	// Create VM configuration
	config, err := vz.NewVirtualMachineConfiguration(bootLoader, uint(defaultCPUs), uint64(defaultMemoryMB)*1024*1024)
	if err != nil {
		return nil, fmt.Errorf("failed to create VM configuration: %w", err)
	}

	// Create virtio console for terminal
	serialPort, err := vz.NewVirtioConsoleDeviceSerialPortConfiguration(
		vz.NewFileHandleSerialPortAttachment(os.Stdin, os.Stdout),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create serial port: %w", err)
	}

	consoleConfig, err := vz.NewVirtioConsoleDeviceConfiguration()
	if err != nil {
		return nil, fmt.Errorf("failed to create console config: %w", err)
	}
	consoleConfig.SetPorts(
		vz.NewVirtioConsolePortConfigurationArray(serialPort),
	)
	config.SetConsoleDevices(
		vz.NewVirtioConsoleDeviceConfigurationArray(consoleConfig),
	)

	// Create root disk from rootfs
	diskPath := filepath.Join(b.cacheDir, opts.Tool+"-disk.img")
	if err := b.createDiskImage(rootfsPath, diskPath); err != nil {
		return nil, fmt.Errorf("failed to create disk image: %w", err)
	}

	diskAttachment, err := vz.NewDiskImageStorageDeviceAttachment(diskPath, false)
	if err != nil {
		return nil, fmt.Errorf("failed to create disk attachment: %w", err)
	}

	blockDevice, err := vz.NewVirtioBlockDeviceConfiguration(diskAttachment)
	if err != nil {
		return nil, fmt.Errorf("failed to create block device: %w", err)
	}
	config.SetStorageDevices(
		vz.NewVirtioBlockDeviceConfigurationArray(blockDevice),
	)

	// Create 9p shares for mounts
	var shares []*vz.VirtioFileSystemDeviceConfiguration
	shareIndex := 0

	// Add all mounts as 9p shares
	allMounts := append(opts.MountsRW, opts.MountsRO...)
	for _, mountPath := range allMounts {
		if _, err := os.Stat(mountPath); os.IsNotExist(err) {
			continue
		}

		// Create unique tag for this share
		tag := fmt.Sprintf("share%d", shareIndex)
		shareIndex++

		// Hash the path to create a safe mount point name
		hash := sha256.Sum256([]byte(mountPath))
		hashStr := hex.EncodeToString(hash[:8])

		shareConfig, err := vz.NewSharedDirectory(mountPath, false)
		if err != nil {
			continue
		}

		singleDirShare, err := vz.NewSingleDirectoryShare(shareConfig)
		if err != nil {
			continue
		}

		fsConfig, err := vz.NewVirtioFileSystemDeviceConfiguration(tag)
		if err != nil {
			continue
		}
		fsConfig.SetDirectoryShare(singleDirShare)

		// Store mapping for later use in init script
		_ = hashStr // Could be used for mount point naming

		shares = append(shares, fsConfig)
	}

	if len(shares) > 0 {
		config.SetDirectorySharingDevices(
			vz.NewVirtioFileSystemDeviceConfigurationArray(shares...),
		)
	}

	// Create entropy device for random numbers
	entropyConfig, err := vz.NewVirtioEntropyDeviceConfiguration()
	if err != nil {
		return nil, fmt.Errorf("failed to create entropy device: %w", err)
	}
	config.SetEntropyDevices(
		vz.NewVirtioEntropyDeviceConfigurationArray(entropyConfig),
	)

	// Validate configuration
	valid, err := config.Validate()
	if err != nil {
		return nil, fmt.Errorf("failed to validate VM config: %w", err)
	}
	if !valid {
		return nil, fmt.Errorf("VM configuration is invalid")
	}

	return config, nil
}

// createDiskImage creates an ext4 disk image from a rootfs directory
func (b *Backend) createDiskImage(rootfsPath, diskPath string) error {
	// Calculate approximate size needed (rootfs size + 500MB overhead)
	var size int64 = 2 * 1024 * 1024 * 1024 // 2GB default

	// Check if disk image already exists and is newer than rootfs
	diskInfo, diskErr := os.Stat(diskPath)
	rootfsInfo, rootfsErr := os.Stat(rootfsPath)
	if diskErr == nil && rootfsErr == nil {
		if diskInfo.ModTime().After(rootfsInfo.ModTime()) {
			// Disk image is newer, no need to recreate
			return nil
		}
	}

	// Create sparse disk image
	f, err := os.Create(diskPath)
	if err != nil {
		return err
	}
	if err := f.Truncate(size); err != nil {
		f.Close()
		return err
	}
	f.Close()

	// Format as ext4
	cmd := exec.Command("mkfs.ext4", "-F", "-d", rootfsPath, diskPath)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("mkfs.ext4 failed: %w: %s", err, output)
	}

	return nil
}

// compressRootfs compresses the rootfs using gzip
func compressRootfs(rootfsPath string) (string, error) {
	compressedPath := rootfsPath + ".tar.gz"

	f, err := os.Create(compressedPath)
	if err != nil {
		return "", err
	}
	defer f.Close()

	gw := gzip.NewWriter(f)
	defer gw.Close()

	tw := tar.NewWriter(gw)
	defer tw.Close()

	err = filepath.Walk(rootfsPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		relPath, err := filepath.Rel(rootfsPath, path)
		if err != nil {
			return err
		}

		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		header.Name = relPath

		if err := tw.WriteHeader(header); err != nil {
			return err
		}

		if !info.IsDir() {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if _, err := tw.Write(data); err != nil {
				return err
			}
		}

		return nil
	})

	return compressedPath, err
}
