/* Copyright 2016-2026 nix <https://keybase.io/nixn>

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License. */

package src

import (
	"sync"
	"sync/atomic"
)

type RWMutexCounted struct {
	sync.RWMutex
	count atomic.Int32
}

func (m *RWMutexCounted) RLock() {
	m.RWMutex.RLock()
	m.count.Add(1)
}

func (m *RWMutexCounted) RUnlock() {
	m.count.Add(-1)
	m.RWMutex.RUnlock()
}

func (m *RWMutexCounted) Lock() {
	m.RWMutex.Lock()
	m.count.Add(1)
}

func (m *RWMutexCounted) Unlock() {
	m.count.Add(-1)
	m.RWMutex.Unlock()
}

func (m *RWMutexCounted) TryLock() bool {
	locked := m.RWMutex.TryLock()
	if locked {
		m.count.Add(1)
	}
	return locked
}

func (m *RWMutexCounted) TryRLock() bool {
	locked := m.RWMutex.TryRLock()
	if locked {
		m.count.Add(1)
	}
	return locked
}

func (m *RWMutexCounted) Count() int32 {
	return m.count.Load()
}
