package loop

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// FileStore 把运行状态落成单个 JSON 文件。
//
// 为什么用文件而不是数据库：一个运行状态几十 KB，
// 写入频率是每轮一次（几十秒一次）。引入 DB 的复杂度
// 换来的是这里完全不需要的能力。
type FileStore struct {
	Dir string
	mu  sync.Mutex
}

// NewFileStore 在 dir 下建立检查点目录。
func NewFileStore(dir string) (*FileStore, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("loop: create checkpoint dir: %w", err)
	}
	return &FileStore{Dir: dir}, nil
}

func (s *FileStore) path(id string) string {
	// id 来自内部生成，不会含路径分隔符；仍然过一遍 Clean 防止意外
	return filepath.Join(s.Dir, filepath.Base(id)+".json")
}

// Save 原子写入：先写临时文件再 rename。
//
// 直接覆盖的话，进程在写一半时崩溃会留下损坏的 JSON，
// 而这份 JSON 正是恢复用的 —— 等于崩溃的同时把救命绳也弄断了。
func (s *FileStore) Save(ctx context.Context, st *State) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	data, err := json.Marshal(st)
	if err != nil {
		return fmt.Errorf("loop: marshal state: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	final := s.path(st.ID)
	tmp, err := os.CreateTemp(s.Dir, ".state-*")
	if err != nil {
		return fmt.Errorf("loop: create temp: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("loop: write temp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("loop: sync temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("loop: close temp: %w", err)
	}
	if err := os.Rename(tmpName, final); err != nil {
		return fmt.Errorf("loop: rename: %w", err)
	}
	return nil
}

// Load 读取检查点。文件不存在返回 ErrNotFound。
func (s *FileStore) Load(ctx context.Context, id string) (*State, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(s.path(id))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("loop: read state: %w", err)
	}
	var st State
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, fmt.Errorf("loop: corrupt state file: %w", err)
	}
	return &st, nil
}

// Delete 清理已完成的运行，避免目录无限增长。
func (s *FileStore) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	err := os.Remove(s.path(id))
	if err != nil && os.IsNotExist(err) {
		return nil
	}
	return err
}
