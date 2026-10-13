package setup

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

const instanceStoreModel = "Amazon EC2 NVMe Instance Storage"

type blockDevice struct {
	Path        string        `json:"path"`
	Type        string        `json:"type"`
	FSType      string        `json:"fstype"`
	Model       string        `json:"model"`
	Mountpoints []*string     `json:"mountpoints"`
	Children    []blockDevice `json:"children"`
}

type lsblkResult struct {
	BlockDevices []blockDevice `json:"blockdevices"`
}

// mountInstanceStore mounts the EC2 instance-store filesystem at dataDir.
// It reuses an existing filesystem and only formats a device that appears blank.
// The caller should invoke this only when dataDir is not already a mountpoint.
func mountInstanceStore(dataDir string) error {
	mounted, err := isMountpoint(dataDir)
	if err != nil {
		return fmt.Errorf("check mountpoint %s: %w", dataDir, err)
	}
	if mounted {
		return nil
	}

	if os.Geteuid() != 0 {
		return fmt.Errorf("must run as root to mount the instance store")
	}

	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return fmt.Errorf("create mount directory %q: %w", dataDir, err)
	}

	disk, err := discoverInstanceStore()
	if err != nil {
		return err
	}

	if err := ensureDeviceNotMounted(*disk); err != nil {
		return err
	}

	devicePath, fsType, err := selectInstanceStoreFilesystem(*disk)
	if err != nil {
		return err
	}

	if fsType == "" {
		signatures, err := exec.Command(
			"wipefs", "--no-act", devicePath,
		).CombinedOutput()
		if err != nil {
			return fmt.Errorf("inspect signatures on %s: %w: %s",
				devicePath, err, strings.TrimSpace(string(signatures)))
		}
		if strings.TrimSpace(string(signatures)) != "" {
			return fmt.Errorf(
				"%s has existing disk signatures; refusing to format",
				devicePath,
			)
		}

		if err := exec.Command("mkfs.ext4", "-F", devicePath).Run(); err != nil {
			return fmt.Errorf("format %s as ext4: %w", devicePath, err)
		}
		fsType = "ext4"
	}

	if err := exec.Command("mount", devicePath, dataDir).Run(); err != nil {
            return fmt.Errorf("mount %s at %s: %w", devicePath, dataDir, err)
    }

	mounted, err = isMountpoint(dataDir)
	
	if err != nil {
		return fmt.Errorf("verify mountpoint %s: %w", dataDir, err)
	}
	if !mounted {
		return fmt.Errorf("%s is not a mountpoint after mounting", dataDir)
	}

	return nil
}

func discoverInstanceStore() (*blockDevice, error) {
	output, err := exec.Command("lsblk", "-J", "-b", "-o", "PATH,TYPE,FSTYPE,MODEL,MOUNTPOINTS").Output()
	if err != nil {
		return nil, fmt.Errorf("discover block devices with lsblk: %w", err)
	}
	var result lsblkResult
	if err := json.Unmarshal(output, &result); err != nil {
		return nil, fmt.Errorf("parse lsblk JSON: %w", err)
	}

	var found *blockDevice
	for i := range result.BlockDevices {
		device := &result.BlockDevices[i]
		if device.Type != "disk" || !strings.Contains(device.Model, instanceStoreModel) {
			continue
		}
		if found != nil {
			return nil, fmt.Errorf("multiple EC2 instance-store disks found; refusing to guess")
		}
		found = device
	}
	if found == nil {
		return nil, fmt.Errorf("no disk with model %q found", instanceStoreModel)
	}
	return found, nil
}

func ensureDeviceNotMounted(device blockDevice) error {
	for _, mountpoint := range device.Mountpoints {
		if mountpoint != nil && strings.TrimSpace(*mountpoint) != "" {
			return fmt.Errorf("instance-store device %s is already mounted at %s", device.Path, *mountpoint)
		}
	}
	for _, child := range device.Children {
		if err := ensureDeviceNotMounted(child); err != nil {
			return err
		}
	}
	return nil
}

func selectInstanceStoreFilesystem(disk blockDevice) (devicePath, fsType string, err error) {
	// Some images put a filesystem directly on the whole NVMe device.
	if disk.FSType != "" {
		return disk.Path, disk.FSType, nil
	}

	var formatted []blockDevice
	for _, child := range disk.Children {
		if child.FSType != "" {
			formatted = append(formatted, child)
		}
	}
	if len(formatted) == 1 {
		return formatted[0].Path, formatted[0].FSType, nil
	}
	if len(formatted) > 1 {
		return "", "", fmt.Errorf("multiple formatted partitions found on %s; refusing to guess", disk.Path)
	}

	// A single blank partition can be formatted without changing the partition table.
	if len(disk.Children) == 1 {
		return disk.Children[0].Path, "", nil
	}
	if len(disk.Children) > 1 {
		return "", "", fmt.Errorf("multiple partitions found on %s without a recognized filesystem; refusing to guess", disk.Path)
	}

	// No child devices and no filesystem: the whole device may be blank.
	return disk.Path, "", nil
}

func ensureFstabEntry(uuid, mountDir, fsType string) error {
	const fstabPath = "/etc/fstab"
	content, err := os.ReadFile(fstabPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", fstabPath, err)
	}

	for _, line := range strings.Split(string(content), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 || strings.HasPrefix(fields[0], "#") {
			continue
		}
		if fields[1] != mountDir {
			continue
		}
		if fields[0] == "UUID="+uuid && fields[2] == fsType {
			return nil
		}
		return fmt.Errorf("%s already has a conflicting /etc/fstab entry: %s", mountDir, line)
	}

	checkPass := "0"
	switch fsType {
	case "ext2", "ext3", "ext4":
		checkPass = "2"
	}
	entry := fmt.Sprintf("UUID=%s %s %s defaults,nofail 0 %s\n", uuid, mountDir, fsType, checkPass)

	file, err := os.OpenFile(fstabPath, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("open %s: %w", fstabPath, err)
	}
	if _, err := file.WriteString(entry); err != nil {
		_ = file.Close()
		return fmt.Errorf("append instance-store entry to %s: %w", fstabPath, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close %s after writing: %w", fstabPath, err)
	}
	return nil
}
