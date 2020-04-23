package util

import (
	"fmt"
	"sync"
)

type InstanceNameHolder struct {
	creatingInstanceNames map[string]bool
	creatingInstanceMutex sync.Mutex
}

func NewInstanceNameHolder() *InstanceNameHolder {
	names := make(map[string]bool)
	return &InstanceNameHolder {
		creatingInstanceNames: names,
	}
}

func (c *InstanceNameHolder) SetCreating(name string) error {
	c.creatingInstanceMutex.Lock()
	defer c.creatingInstanceMutex.Unlock()
	if _, ok := c.creatingInstanceNames[name]; ok {
		return fmt.Errorf("Instance %s is about to create in other Create request", name)
	}
	c.creatingInstanceNames[name] = true
	return nil
}

func (c *InstanceNameHolder) IsCreating(name string) bool {
	_, ok := c.creatingInstanceNames[name]
	return ok
}

func (c *InstanceNameHolder) UnsetCreating(name string) {
	c.creatingInstanceMutex.Lock()
	defer c.creatingInstanceMutex.Unlock()
	delete(c.creatingInstanceNames, name)
}
