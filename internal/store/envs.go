package store

import (
	"database/sql"
	"errors"
)

// Env Python 虚拟环境。
type Env struct {
	ID            int64  `json:"id"`
	Name          string `json:"name"`
	PythonVersion string `json:"python_version"`
	Interpreter   string `json:"interpreter"`
	ScriptsDir    string `json:"scripts_dir"`
	Source        string `json:"source"` // local | download | official
	Status        string `json:"status"` // creating | ready | error
	CreatedAt     string `json:"created_at"`
}

const envCols = `id, name, python_version, interpreter, scripts_dir, source, status, created_at`

func scanEnv(row interface{ Scan(...any) error }) (*Env, error) {
	var e Env
	err := row.Scan(&e.ID, &e.Name, &e.PythonVersion, &e.Interpreter, &e.ScriptsDir, &e.Source, &e.Status, &e.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &e, nil
}

// ListEnvs 全部环境。
func (s *Store) ListEnvs() ([]*Env, error) {
	rows, err := s.db.Query(`SELECT ` + envCols + ` FROM envs ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Env
	for rows.Next() {
		e, err := scanEnv(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// GetEnv 按 id 取环境。
func (s *Store) GetEnv(id int64) (*Env, error) {
	row := s.db.QueryRow(`SELECT `+envCols+` FROM envs WHERE id=?`, id)
	e, err := scanEnv(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return e, err
}

// GetEnvByName 按名称取环境。
func (s *Store) GetEnvByName(name string) (*Env, error) {
	row := s.db.QueryRow(`SELECT `+envCols+` FROM envs WHERE name=?`, name)
	e, err := scanEnv(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return e, err
}

// CreateEnv 新建环境记录。
func (s *Store) CreateEnv(e *Env) (int64, error) {
	res, err := s.db.Exec(`INSERT INTO envs (name, python_version, interpreter, scripts_dir, source, status, created_at)
		VALUES (?,?,?,?,?,?,?)`,
		e.Name, e.PythonVersion, e.Interpreter, e.ScriptsDir, e.Source, e.Status, nowStr())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// FinishEnvCreate 环境创建结束：写入解释器/Scripts 目录与最终状态。
func (s *Store) FinishEnvCreate(id int64, interpreter, scriptsDir, version, status string) error {
	_, err := s.db.Exec(`UPDATE envs SET interpreter=?, scripts_dir=?, python_version=?, status=? WHERE id=?`,
		interpreter, scriptsDir, version, status, id)
	return err
}

// SetEnvStatus 更新环境状态。
func (s *Store) SetEnvStatus(id int64, status string) error {
	_, err := s.db.Exec(`UPDATE envs SET status=? WHERE id=?`, status, id)
	return err
}

// DeleteEnv 删除环境记录。
func (s *Store) DeleteEnv(id int64) error {
	_, err := s.db.Exec(`DELETE FROM envs WHERE id=?`, id)
	return err
}
