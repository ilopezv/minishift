/*
Copyright (C) 2018 Red Hat, Inc.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package hostfolder

import (
	"errors"
	"fmt"
	"github.com/docker/machine/libmachine/drivers"
	"github.com/docker/machine/libmachine/state"
	"github.com/golang/glog"
	miniConfig "github.com/minishift/minishift/pkg/minishift/config"
	"github.com/minishift/minishift/pkg/minishift/hostfolder/config"
	"strings"
)

type MountInfo struct {
	Name       string
	Type       string
	Source     string
	MountPoint string
	Mounted    bool
}

// Manager is the central point for all operations around managing hostfolders.
type Manager struct {
	instanceConfig     *miniConfig.InstanceConfigType
	allInstancesConfig *miniConfig.GlobalConfigType
}

// NewAddOnManager creates a new add-on manager for the specified add-on directory.
func NewManager(instanceConfig *miniConfig.InstanceConfigType, allInstancesConfig *miniConfig.GlobalConfigType) (*Manager, error) {
	return &Manager{
		instanceConfig:     instanceConfig,
		allInstancesConfig: allInstancesConfig}, nil
}

// ExistAny returns true if at least one host folder configuration exists, false otherwise.
func (m *Manager) ExistAny() bool {
	return len(m.instanceConfig.HostFolders) > 0 ||
		len(m.allInstancesConfig.HostFolders) > 0
}

// Exist returns true if the host folder with the specified name exist, false otherwise.
func (m *Manager) Exist(name string) bool {
	return m.getHostFolder(name) != nil
}

// Add adds teh specified host folder to the configuration. Depending on the allInstances flag the configuration is either
// saved to the instance configuration or the global all instances configuration.
func (m *Manager) Add(hostFolder HostFolder, allInstances bool) {
	if allInstances {
		m.allInstancesConfig.HostFolders = append(m.allInstancesConfig.HostFolders, hostFolder.Config())
		m.allInstancesConfig.Write()
	} else {
		m.instanceConfig.HostFolders = append(m.instanceConfig.HostFolders, hostFolder.Config())
		m.instanceConfig.Write()
	}
}

// Remove removes the specified host folder from the configuration. If the host folder does not exist an error is returned.
func (m *Manager) Remove(name string) error {
	if !m.Exist(name) {
		return fmt.Errorf("no host folder defined with name '%s'", name)
	}

	m.instanceConfig.HostFolders = m.removeFromHostFolders(name, miniConfig.InstanceConfig.HostFolders)
	m.instanceConfig.Write()

	m.allInstancesConfig.HostFolders = m.removeFromHostFolders(name, miniConfig.AllInstancesConfig.HostFolders)
	m.allInstancesConfig.Write()

	return nil
}

// List returns a list of MountInfo instances for the configured host folders. If an error occurs nil is returned
// together with the error.
func (m *Manager) List(driver drivers.Driver) ([]MountInfo, error) {
	var isRunning bool
	if driver != nil && drivers.MachineInState(driver, state.Running)() {
		isRunning = true
	} else {
		isRunning = false
	}

	if !m.ExistAny() {
		return nil, errors.New("no host folders defined")
	}

	procMounts := ""
	if isRunning {
		cmd := fmt.Sprint("cat /proc/mounts")
		procMounts, _ = drivers.RunSSHCommandFromDriver(driver, cmd)
	}

	hostfolders := miniConfig.AllInstancesConfig.HostFolders
	hostfolders = append(hostfolders, miniConfig.InstanceConfig.HostFolders...)
	var mounts []MountInfo
	for _, hostFolder := range hostfolders {

		source := ""
		switch hostFolder.Type {
		case CIFS.String():
			source = hostFolder.Options[config.UncPath]
		case SSHFS.String():
			source = hostFolder.Options[config.Source]
		}

		mounted := false
		if isRunning && strings.Contains(procMounts, hostFolder.MountPoint()) {
			mounted = true
		}

		mount := MountInfo{
			Name:       hostFolder.Name,
			Type:       hostFolder.Type,
			Source:     source,
			MountPoint: hostFolder.MountPoint(),
			Mounted:    mounted,
		}

		mounts = append(mounts, mount)
	}

	return mounts, nil
}

// Mount mounts the host folder specified by name into the running VM. nil is returned on success.
// An error is returned, if the VM is not running, the specified host folder does not exist or the mount fails.
func (m *Manager) Mount(driver drivers.Driver, name string) error {
	if !m.isHostRunning(driver) {
		return errors.New("host is in the wrong state")
	}

	hostFolder := m.getHostFolder(name)
	if hostFolder == nil {
		return fmt.Errorf("no host folder with name '%s' defined", name)
	}

	m.ensureMountPointExists(driver, hostFolder.Config())

	mounted, err := m.isHostFolderMounted(driver, hostFolder.Config())
	if mounted {
		if err != nil {
			glog.Error(err.Error())
		}
		glog.Info("SSH server established")
		return fmt.Errorf("host folder is already mounted")
	}

	err = hostFolder.Mount(driver)
	if err != nil {
		return err
	}
	return nil
}

// MountAll mounts all defined host folders.
func (m *Manager) MountAll(driver drivers.Driver) error {
	if !m.isHostRunning(driver) {
		return errors.New("host is in the wrong state")
	}

	if !m.ExistAny() {
		return errors.New("no host folders defined")
	}

	hostFolderConfigs := m.allInstancesConfig.HostFolders
	hostFolderConfigs = append(hostFolderConfigs, m.instanceConfig.HostFolders...)
	for _, hostFolderConfig := range hostFolderConfigs {
		m.Mount(driver, hostFolderConfig.Name)
	}
	return nil
}

// Umount umounts the host folder specified by name. nil is returned on success.
// An error is returned, if the VM is not running, the specified host folder does not exist or the mount fails.
func (m *Manager) Umount(driver drivers.Driver, name string) error {
	if !m.isHostRunning(driver) {
		return errors.New("host is in the wrong state")
	}

	if !m.ExistAny() {
		return errors.New("no host folders defined")
	}

	hostFolder := m.getHostFolder(name)
	if hostFolder == nil {
		return fmt.Errorf("no host folder with the name '%s' defined", name)
	}

	mounted, err := m.isHostFolderMounted(driver, hostFolder.Config())
	if !mounted {
		if err != nil {
			return fmt.Errorf("error umouting hostfolder '%s': %s", name, err)
		}
		return nil
	}

	err = hostFolder.Umount(driver)
	if err != nil {
		return err
	}

	return nil
}

func (m *Manager) getHostFolder(name string) HostFolder {
	config := m.getHostFolderConfig(name, miniConfig.InstanceConfig.HostFolders)
	if config != nil {
		return m.hostFolderForConfig(config)
	}

	config = m.getHostFolderConfig(name, miniConfig.AllInstancesConfig.HostFolders)
	if config != nil {
		return m.hostFolderForConfig(config)
	}

	return nil
}

func (m *Manager) hostFolderForConfig(config *config.HostFolderConfig) HostFolder {
	switch config.Type {
	case CIFS.String():
		return NewCifsHostFolder(*config)
	case SSHFS.String():
		return NewSSHFSHostFolder(*config, m.allInstancesConfig)
	default:
		return nil
	}
}

func (m *Manager) getHostFolderConfig(name string, hostFolderConfigs []config.HostFolderConfig) *config.HostFolderConfig {
	for i := range hostFolderConfigs {
		hostFolderConfig := hostFolderConfigs[i]
		if hostFolderConfig.Name == name {
			return &hostFolderConfig
		}
	}

	return nil
}

func (m *Manager) isHostRunning(driver drivers.Driver) bool {
	return drivers.MachineInState(driver, state.Running)()
}

func (m *Manager) ensureMountPointExists(driver drivers.Driver, hostFolder config.HostFolderConfig) error {
	cmd := fmt.Sprintf("sudo mkdir -p %s", hostFolder.MountPoint())

	if _, err := drivers.RunSSHCommandFromDriver(driver, cmd); err != nil {
		return err
	}

	return nil
}

func (m *Manager) removeFromHostFolders(name string, hostfolders []config.HostFolderConfig) []config.HostFolderConfig {
	for i := range hostfolders {

		hostFolder := hostfolders[i]

		if hostFolder.Name == name {
			hostfolders = append(hostfolders[:i], hostfolders[i+1:]...)
			break
		}
	}
	return hostfolders
}

func (m *Manager) isHostFolderMounted(driver drivers.Driver, hostFolderConfig config.HostFolderConfig) (bool, error) {
	cmd := fmt.Sprintf(
		"if grep -qs %s /proc/mounts; then echo '1'; else echo '0'; fi", hostFolderConfig.MountPoint())
	out, err := drivers.RunSSHCommandFromDriver(driver, cmd)
	if err != nil {
		return false, err
	}

	if strings.Trim(out, "\n") == "0" {
		return false, nil
	}

	return true, nil
}
