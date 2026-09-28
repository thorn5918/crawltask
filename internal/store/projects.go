package store

import (
	"database/sql"
	"errors"
)

// Project 项目（对应 projects/ 下一个文件夹）。
type Project struct {
	Name      string `json:"name"`
	Tags      string `json:"tags"`
	CreatedAt string `json:"created_at"`
}

// ListProjects 全部项目记录（含标签）。
func (s *Store) ListProjects() ([]*Project, error) {
	rows, err := s.db.Query(`SELECT name, tags, created_at FROM projects ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Project
	for rows.Next() {
		var p Project
		if err := rows.Scan(&p.Name, &p.Tags, &p.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, &p)
	}
	return out, rows.Err()
}

// GetProject 按名称取项目。
func (s *Store) GetProject(name string) (*Project, error) {
	row := s.db.QueryRow(`SELECT name, tags, created_at FROM projects WHERE name=?`, name)
	var p Project
	err := row.Scan(&p.Name, &p.Tags, &p.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// UpsertProject 新建或更新项目（标签）。
func (s *Store) UpsertProject(name, tags string) error {
	_, err := s.db.Exec(`INSERT INTO projects (name, tags, created_at) VALUES (?,?,?)
		ON CONFLICT(name) DO UPDATE SET tags=excluded.tags`, name, tags, nowStr())
	return err
}

// DeleteProject 删除项目记录。
func (s *Store) DeleteProject(name string) error {
	_, err := s.db.Exec(`DELETE FROM projects WHERE name=?`, name)
	return err
}
