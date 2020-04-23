package util

import (
	"fmt"
	"sync"
	"github.com/golang/glog"
)

type OperateResourceQueue struct {
	name string
	resources map[string]bool
	queueMutex sync.Mutex
}

func NewOperateResourceQueue(name string) *OperateResourceQueue {
	rs := make(map[string]bool)
	return &OperateResourceQueue {
		name: name,
		resources: rs,
	}
}

func (q *OperateResourceQueue) Queue(name string) error {
	if _, ok := q.resources[name]; ok {
		return fmt.Errorf("Duplicate Queue called for %s in %s", name, q.name)
	}
	q.resources[name] = false
	q.queueMutex.Lock()
	q.resources[name] = true
	return nil
}

func (q *OperateResourceQueue) Show() {
	if len(q.resources) == 0 {
		glog.V(4).Infof("No resources Operating/Pending in %s", q.name)
	}
	for name, operating := range q.resources {
		state := "Pending"
		if operating {
			state = "Operating"
		}
		glog.V(4).Infof("Resource %s %s in %s", name, state, q.name)
	}
}

func (q *OperateResourceQueue) UnsetQueue(name string) {
	operating, ok := q.resources[name]
	if !ok || !operating {
		return
	}
	delete(q.resources, name)
	q.queueMutex.Unlock()
}
